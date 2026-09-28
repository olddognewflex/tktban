// Package model is the pure board model — no I/O, no TUI. It turns tkt's
// normalized ticket dicts (decoded JSON, here map[string]any) into ordered
// columns of cards. Kept dependency-free so it is trivially testable.
package model

import (
	"sort"
	"strings"
	"time"
)

// Ticket is a normalized ticket as decoded from tkt's --json output. It mirrors
// the loosely-typed dict the Python uses, so field access is by key with a
// safe-default helper.
type Ticket = map[string]any

// PriorityRank ranks priority by meaning, not lexicographically. tkt stores
// priority as a free string and sorts it lexicographically (which is wrong:
// "Medium" > "Highest"), so the board owns the order here. Higher = more
// urgent; unknown/empty sinks to 0.
var PriorityRank = map[string]int{
	"Highest": 5,
	"High":    4,
	"Medium":  3,
	"Low":     2,
	"Lowest":  1,
}

// Unmapped is the role/lane label for tickets whose status_role isn't a
// configured board role.
const Unmapped = "(unmapped)"

func priorityRank(priority string) int {
	return PriorityRank[priority]
}

// RolePair is one configured board role and its provider lane label, in column
// order. Order matters (it drives column order), so roles are carried as an
// ordered slice rather than a map.
type RolePair struct {
	Role string
	Lane string
}

// Card is a single ticket rendered on the board.
type Card struct {
	Key           string
	Summary       string
	Assignee      string
	Priority      string
	StatusRole    string
	BlockerCount  int
	LaneHuman     string // human time-in-current-lane, e.g. "6h 10m" (empty = unknown)
	Due           string // optional dates, "YYYY-MM-DD" or "" when unset
	Scheduled     string
	Completed     string
	AgentStatus   string // agent execution state: ""|idle|processing|waiting|done|blocked
	AgentStatusAt string // when AgentStatus was written, ISO "2006-01-02T15:04:05Z" or ""
	RunState      string // `tkt agents` run state: ""|running|stalled|dead|blocked|halted
	RunUpdated    string // when the run last wrote RunState, ISO like AgentStatusAt, or ""
}

// CardFromTicket builds a Card from a normalized ticket dict.
func CardFromTicket(t Ticket) Card {
	return Card{
		Key:           getStr(t, "key"),
		Summary:       getStr(t, "summary"),
		Assignee:      getStr(t, "assignee"),
		Priority:      getStr(t, "priority"),
		StatusRole:    getStr(t, "status_role"),
		BlockerCount:  unresolvedBlockers(t),
		LaneHuman:     getStr(t, "lane_human"),
		Due:           getStr(t, "due"),
		Scheduled:     getStr(t, "scheduled"),
		Completed:     getStr(t, "completed"),
		AgentStatus:   getStr(t, "agent_status"),
		AgentStatusAt: getStr(t, "agent_status_at"),
		RunState:      getStr(t, "run_state"),
		RunUpdated:    getStr(t, "run_updated"),
	}
}

// AgentSource says which of a card's three agent signals its badge came from.
type AgentSource int

const (
	SourceNone        AgentSource = iota // no badge
	SourceHerdr                          // herdr's live pane status
	SourceRun                            // `tkt agents` run state
	SourceFrontmatter                    // the ticket's agent_status
)

// AgentNeedsYou is the merged status for an agent waiting on a human (herdr
// saw a permission prompt or question). The frontmatter has no such state.
const AgentNeedsYou = "needs_you"

// LiveAbsent is MergeAgent's live argument when herdr is on but has no agent
// pane for the ticket.
const LiveAbsent = "absent"

// AgentView is a card's merged agent state: a frontmatter-style status (plus
// AgentNeedsYou) and where it came from.
type AgentView struct {
	Status string
	Source AgentSource
}

// MergeAgent reconciles a card's agent signals, most live first; the first
// rule that matches wins.
//
// live is herdr's pane status ("" = live off, LiveAbsent = on but no pane),
// run is the `tkt agents` state ("" = none), front is the frontmatter
// agent_status.
//
//  1. herdr working → processing; herdr blocked → needs you.
//  2. run running/stalled → processing (an idle herdr pane does not beat a
//     live run); run blocked → blocked; run halted → waiting.
//  3. A frontmatter processing is hidden when a live source says nothing is
//     working: herdr is on and quiet, or the run is dead.
//  4. Otherwise the frontmatter as-is; idle and "" are no badge.
func MergeAgent(live, run, front string) AgentView {
	switch live {
	case "working":
		return AgentView{"processing", SourceHerdr}
	case "blocked":
		return AgentView{AgentNeedsYou, SourceHerdr}
	}
	switch run {
	case "running", "stalled":
		return AgentView{"processing", SourceRun}
	case "blocked":
		return AgentView{"blocked", SourceRun}
	case "halted":
		return AgentView{"waiting", SourceRun}
	}
	if front == "processing" && (live != "" || run == "dead") {
		return AgentView{}
	}
	if front == "" || front == "idle" {
		return AgentView{}
	}
	return AgentView{front, SourceFrontmatter}
}

// EffectiveRun is the run state MergeAgent should see for c. tkt never cleans
// up run dirs, so a halted or blocked run lingers after a human has finished
// the ticket by hand: when the frontmatter status was written strictly after
// the run's (both timestamps parse), the newer signal wins and the run is
// ignored. A running or stalled run is live and always kept.
func EffectiveRun(c Card) string {
	if (c.RunState != "halted" && c.RunState != "blocked") || c.AgentStatus == "" {
		return c.RunState
	}
	front, ferr := time.Parse(time.RFC3339, c.AgentStatusAt)
	run, rerr := time.Parse(time.RFC3339, c.RunUpdated)
	if ferr == nil && rerr == nil && front.After(run) {
		return ""
	}
	return c.RunState
}

// sortKey returns the ordering tuple: priority DESC (negated rank), then key ASC.
func (c Card) less(other Card) bool {
	pa, pb := -priorityRank(c.Priority), -priorityRank(other.Priority)
	if pa != pb {
		return pa < pb
	}
	return c.Key < other.Key
}

// Column is a board column: a role, its display lane, and its sorted cards.
type Column struct {
	Role  string // canonical role key, or Unmapped
	Lane  string // provider's literal lane label (display title)
	Cards []Card
}

// KeyPrefix is the project prefix of a ticket key — the part before the first
// '-' ("TKB-1" -> "TKB"). Returns the whole key if there is no '-'.
func KeyPrefix(key string) string {
	prefix, _, _ := strings.Cut(key, "-")
	return prefix
}

// FilterTickets narrows a ticket list by assignee and/or key prefix. Both
// filters are case-insensitive and optional; an empty string disables that
// filter, so no arguments returns the list unchanged.
//
//   - assignee: exact match against the ticket's assignee.
//   - prefix: matches the ticket key's project prefix ("TKB" matches "TKB-1").
func FilterTickets(tickets []Ticket, assignee, prefix string) []Ticket {
	a := strings.ToLower(strings.TrimSpace(assignee))
	p := strings.ToLower(strings.TrimSpace(prefix))
	if a == "" && p == "" {
		return tickets
	}
	out := make([]Ticket, 0, len(tickets))
	for _, t := range tickets {
		if a != "" && strings.ToLower(getStr(t, "assignee")) != a {
			continue
		}
		if p != "" && strings.ToLower(KeyPrefix(getStr(t, "key"))) != p {
			continue
		}
		out = append(out, t)
	}
	return out
}

// BuildBoard groups tickets into columns in roles order.
//
// roles is the ordered role→lane map from `tkt cfg board.roles --json`. Each
// card lands in the column whose role == its status_role. Tickets whose role
// isn't configured go into a trailing Unmapped column (only if any exist), so
// nothing is silently dropped. Each column is sorted by priority then key.
func BuildBoard(roles []RolePair, tickets []Ticket) []Column {
	columns := make([]Column, len(roles))
	byRole := make(map[string]int, len(roles))
	for i, rp := range roles {
		columns[i] = Column{Role: rp.Role, Lane: rp.Lane}
		byRole[rp.Role] = i
	}
	unmapped := Column{Role: Unmapped, Lane: Unmapped}

	for _, t := range tickets {
		card := CardFromTicket(t)
		if i, ok := byRole[card.StatusRole]; ok {
			columns[i].Cards = append(columns[i].Cards, card)
		} else {
			unmapped.Cards = append(unmapped.Cards, card)
		}
	}

	for i := range columns {
		sortCards(columns[i].Cards)
	}
	if len(unmapped.Cards) > 0 {
		sortCards(unmapped.Cards)
		columns = append(columns, unmapped)
	}
	return columns
}

func sortCards(cards []Card) {
	sort.SliceStable(cards, func(i, j int) bool { return cards[i].less(cards[j]) })
}

// ---- helpers ----

func getStr(t Ticket, key string) string {
	if v, ok := t[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func unresolvedBlockers(t Ticket) int {
	raw, ok := t["blocked_by"]
	if !ok {
		return 0
	}
	list, ok := raw.([]any)
	if !ok {
		return 0
	}
	n := 0
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if resolved, _ := m["resolved"].(bool); !resolved {
			n++
		}
	}
	return n
}
