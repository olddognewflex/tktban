// Package model is the pure board model — no I/O, no TUI. It turns tkt's
// normalized ticket dicts (decoded JSON, here map[string]any) into ordered
// columns of cards. Kept dependency-free so it is trivially testable.
package model

import (
	"sort"
	"strings"
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
	Key          string
	Summary      string
	Assignee     string
	Priority     string
	StatusRole   string
	BlockerCount int
	LaneHuman    string // human time-in-current-lane, e.g. "6h 10m" (empty = unknown)
	Due          string // optional dates, "YYYY-MM-DD" or "" when unset
	Scheduled    string
	Completed    string
	AgentStatus  string // agent execution state: ""|idle|processing|waiting|done|blocked
}

// CardFromTicket builds a Card from a normalized ticket dict.
func CardFromTicket(t Ticket) Card {
	return Card{
		Key:          getStr(t, "key"),
		Summary:      getStr(t, "summary"),
		Assignee:     getStr(t, "assignee"),
		Priority:     getStr(t, "priority"),
		StatusRole:   getStr(t, "status_role"),
		BlockerCount: unresolvedBlockers(t),
		LaneHuman:    getStr(t, "lane_human"),
		Due:          getStr(t, "due"),
		Scheduled:    getStr(t, "scheduled"),
		Completed:    getStr(t, "completed"),
		AgentStatus:  getStr(t, "agent_status"),
	}
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
