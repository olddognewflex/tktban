package model

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

var roles = []RolePair{
	{"backlog", "Backlog"},
	{"todo", "To Do"},
	{"in_progress", "In Progress"},
	{"review", "In Review"},
	{"done", "Done"},
	{"blocked", "Blocked"},
}

func ticket(key, role string) Ticket {
	return Ticket{
		"key":         key,
		"summary":     "summary " + key,
		"assignee":    "",
		"priority":    "",
		"status_role": role,
		"blocked_by":  []any{},
	}
}

func columnsByRole(cols []Column) map[string]Column {
	m := make(map[string]Column, len(cols))
	for _, c := range cols {
		m[c.Role] = c
	}
	return m
}

func keys(cards []Card) []string {
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.Key
	}
	return out
}

func TestPriorityRankOrder(t *testing.T) {
	if !(priorityRank("Highest") > priorityRank("High") && priorityRank("High") > priorityRank("Medium")) {
		t.Fatal("Highest > High > Medium expected")
	}
	if !(priorityRank("Medium") > priorityRank("Low") && priorityRank("Low") > priorityRank("Lowest")) {
		t.Fatal("Medium > Low > Lowest expected")
	}
	if priorityRank("") != 0 || priorityRank("Bogus") != 0 {
		t.Fatal("empty/unknown priority must rank 0")
	}
}

func TestCardBlockerCountCountsOnlyUnresolved(t *testing.T) {
	d := ticket("TKT-1", "todo")
	d["blocked_by"] = []any{
		map[string]any{"key": "TKT-2", "resolved": true},
		map[string]any{"key": "TKT-3", "resolved": false},
		map[string]any{"key": "TKT-4", "resolved": false},
	}
	if got := CardFromTicket(d).BlockerCount; got != 2 {
		t.Fatalf("blocker count = %d, want 2", got)
	}
}

func TestCardCarriesAgentStatus(t *testing.T) {
	d := ticket("TKT-1", "todo")
	d["agent_status"] = "processing"
	if got := CardFromTicket(d).AgentStatus; got != "processing" {
		t.Fatalf("agent status = %q, want %q", got, "processing")
	}
	// Missing field round-trips to empty (no agent engaged).
	if got := CardFromTicket(ticket("TKT-2", "todo")).AgentStatus; got != "" {
		t.Fatalf("absent agent_status = %q, want empty", got)
	}
}

func TestColumnsFollowRoleOrder(t *testing.T) {
	cols := BuildBoard(roles, nil)
	for i, rp := range roles {
		if cols[i].Role != rp.Role || cols[i].Lane != rp.Lane {
			t.Fatalf("col %d = %+v, want %+v", i, cols[i], rp)
		}
	}
}

func TestGroupingByStatusRole(t *testing.T) {
	tickets := []Ticket{ticket("TKT-1", "todo"), ticket("TKT-2", "done"), ticket("TKT-3", "todo")}
	cols := columnsByRole(BuildBoard(roles, tickets))
	got := map[string]bool{}
	for _, k := range keys(cols["todo"].Cards) {
		got[k] = true
	}
	if !got["TKT-1"] || !got["TKT-3"] || len(got) != 2 {
		t.Fatalf("todo cards = %v", keys(cols["todo"].Cards))
	}
	if !reflect.DeepEqual(keys(cols["done"].Cards), []string{"TKT-2"}) {
		t.Fatalf("done cards = %v", keys(cols["done"].Cards))
	}
	if len(cols["backlog"].Cards) != 0 {
		t.Fatal("backlog should be empty")
	}
}

func TestSortPriorityDescThenKeyAsc(t *testing.T) {
	tickets := []Ticket{
		withPriority(ticket("TKT-3", "todo"), "Low"),
		withPriority(ticket("TKT-1", "todo"), "Highest"),
		withPriority(ticket("TKT-2", "todo"), "Highest"),
		withPriority(ticket("TKT-4", "todo"), ""),
	}
	cols := columnsByRole(BuildBoard(roles, tickets))
	want := []string{"TKT-1", "TKT-2", "TKT-3", "TKT-4"}
	if got := keys(cols["todo"].Cards); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestUnmappedRoleBucketedNotDropped(t *testing.T) {
	tickets := []Ticket{ticket("TKT-1", "todo"), ticket("TKT-7", "Archived")}
	cols := BuildBoard(roles, tickets)
	last := cols[len(cols)-1]
	if last.Role != Unmapped {
		t.Fatalf("last col role = %q, want %q", last.Role, Unmapped)
	}
	if !reflect.DeepEqual(keys(last.Cards), []string{"TKT-7"}) {
		t.Fatalf("unmapped cards = %v", keys(last.Cards))
	}
}

func TestNoUnmappedColumnWhenAllMapped(t *testing.T) {
	cols := BuildBoard(roles, []Ticket{ticket("TKT-1", "todo")})
	if len(cols) != len(roles) {
		t.Fatalf("col count = %d, want %d", len(cols), len(roles))
	}
	for _, c := range cols {
		if c.Role == Unmapped {
			t.Fatal("no unmapped column expected")
		}
	}
}

func TestCardReadsLaneHumanWithDefault(t *testing.T) {
	if CardFromTicket(ticket("TKT-1", "todo")).LaneHuman != "" {
		t.Fatal("default lane_human should be empty")
	}
	d := ticket("TKT-1", "todo")
	d["lane_human"] = "6h 10m"
	if CardFromTicket(d).LaneHuman != "6h 10m" {
		t.Fatal("lane_human not read")
	}
}

func TestBuildBoardPassesLaneHumanThrough(t *testing.T) {
	d := ticket("TKT-1", "todo")
	d["lane_human"] = "1h 23m"
	cols := columnsByRole(BuildBoard(roles, []Ticket{d}))
	if cols["todo"].Cards[0].LaneHuman != "1h 23m" {
		t.Fatal("lane_human not passed through")
	}
}

func TestKeyPrefix(t *testing.T) {
	cases := map[string]string{"TKB-1": "TKB", "TKT-42": "TKT", "NODASH": "NODASH", "": ""}
	for in, want := range cases {
		if got := KeyPrefix(in); got != want {
			t.Fatalf("KeyPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func board() []Ticket {
	return []Ticket{
		withAssignee(ticket("TKB-1", "todo"), "alice"),
		withAssignee(ticket("TKB-2", "todo"), "alex"),
		withAssignee(ticket("TKT-1", "todo"), "alice"),
		withAssignee(ticket("TKT-2", "todo"), ""),
	}
}

func TestFilterNoArgsReturnsUnchanged(t *testing.T) {
	b := board()
	got := FilterTickets(b, "", "")
	if len(got) != len(b) || &got[0] != &b[0] {
		t.Fatal("no-arg filter must return the same slice unchanged")
	}
}

func TestFilterByAssignee(t *testing.T) {
	if got := keys(cards(FilterTickets(board(), "alice", ""))); !reflect.DeepEqual(got, []string{"TKB-1", "TKT-1"}) {
		t.Fatalf("got %v", got)
	}
}

func TestFilterByPrefix(t *testing.T) {
	if got := keys(cards(FilterTickets(board(), "", "TKB"))); !reflect.DeepEqual(got, []string{"TKB-1", "TKB-2"}) {
		t.Fatalf("got %v", got)
	}
}

func TestFilterByAssigneeAndPrefix(t *testing.T) {
	if got := keys(cards(FilterTickets(board(), "alice", "TKB"))); !reflect.DeepEqual(got, []string{"TKB-1"}) {
		t.Fatalf("got %v", got)
	}
}

func TestFilterIsCaseInsensitive(t *testing.T) {
	if got := keys(cards(FilterTickets(board(), "ALICE", "tkb"))); !reflect.DeepEqual(got, []string{"TKB-1"}) {
		t.Fatalf("got %v", got)
	}
}

func TestFilterPrefixIsExactNotSubstring(t *testing.T) {
	if got := FilterTickets(board(), "", "TK"); len(got) != 0 {
		t.Fatalf("prefix 'TK' must match nothing, got %d", len(got))
	}
}

func TestFilterWhitespaceOnlyArgsDisableFilter(t *testing.T) {
	b := board()
	got := FilterTickets(b, "  ", "  ")
	if len(got) != len(b) || &got[0] != &b[0] {
		t.Fatal("whitespace-only args must disable filtering")
	}
}

// ---- test helpers ----

func withPriority(t Ticket, p string) Ticket { t["priority"] = p; return t }
func withAssignee(t Ticket, a string) Ticket { t["assignee"] = a; return t }

// cards turns filtered tickets into Cards so we can reuse keys().
func cards(tickets []Ticket) []Card {
	out := make([]Card, len(tickets))
	for i, t := range tickets {
		out[i] = CardFromTicket(t)
	}
	return out
}

func TestCardFromTicketReadsAgentStatusAtAndRunState(t *testing.T) {
	d := ticket("TKT-1", "todo")
	d["agent_status_at"] = "2026-09-27T06:30:16Z"
	d["run_state"] = "running"
	d["run_updated"] = "2026-09-28T10:00:00Z"
	c := CardFromTicket(d)
	if c.AgentStatusAt != "2026-09-27T06:30:16Z" || c.RunState != "running" || c.RunUpdated != "2026-09-28T10:00:00Z" {
		t.Fatalf("card = %+v", c)
	}
	d["agent_status_at"] = nil // tkt list --json sends null when unset
	if c := CardFromTicket(d); c.AgentStatusAt != "" {
		t.Fatalf("null agent_status_at = %q, want empty", c.AgentStatusAt)
	}
}

// TKB-26: every combination of herdr × run × frontmatter. A row's field is a
// "|"-separated set of values, or "*" for all of them; the rows must not
// overlap, and together they must cover every combination, so each case is
// pinned exactly once.
func TestMergeAgent(t *testing.T) {
	// herdr: off, no pane, and every status it reports, plus one it might.
	lives := []string{"", LiveAbsent, "working", "blocked", "idle", "done", "unknown", "surprise"}
	runs := []string{"", "running", "stalled", "dead", "blocked", "halted"}
	fronts := []string{"", "processing", "waiting", "done", "blocked", "idle"}

	const quiet = LiveAbsent + "|idle|done|unknown|surprise" // herdr on, nothing working
	none := AgentView{}
	front := func(s string) AgentView { return AgentView{s, SourceFrontmatter} }

	rows := []struct {
		live, run, front string
		want             AgentView
	}{
		// 1. herdr working / blocked beat everything.
		{"working", "*", "*", AgentView{"processing", SourceHerdr}},
		{"blocked", "*", "*", AgentView{AgentNeedsYou, SourceHerdr}},

		// 2. a run beats the frontmatter, and a quiet or absent pane.
		{"|" + quiet, "running|stalled", "*", AgentView{"processing", SourceRun}},
		{"|" + quiet, "blocked", "*", AgentView{"blocked", SourceRun}},
		{"|" + quiet, "halted", "*", AgentView{"waiting", SourceRun}},

		// 3. idle and empty frontmatter never badge.
		{"|" + quiet, "|dead", "|idle", none},

		// 4. live off, run alive or absent: processing shows (today's behaviour).
		{"", "", "processing", front("processing")},
		// ...but herdr quiet (TKB-22) or a dead run hides it.
		{"", "dead", "processing", none},
		{quiet, "|dead", "processing", none},

		// 5. otherwise the frontmatter as-is.
		{"|" + quiet, "|dead", "waiting", front("waiting")},
		{"|" + quiet, "|dead", "done", front("done")},
		{"|" + quiet, "|dead", "blocked", front("blocked")},
	}

	match := func(pat, v string) bool {
		return pat == "*" || slices.Contains(strings.Split(pat, "|"), v)
	}
	for _, l := range lives {
		for _, r := range runs {
			for _, f := range fronts {
				hits := 0
				for _, row := range rows {
					if !match(row.live, l) || !match(row.run, r) || !match(row.front, f) {
						continue
					}
					hits++
					if got := MergeAgent(l, r, f); got != row.want {
						t.Errorf("MergeAgent(%q, %q, %q) = %+v, want %+v", l, r, f, got, row.want)
					}
				}
				if hits != 1 {
					t.Errorf("(%q, %q, %q) matched %d rows, want exactly 1", l, r, f, hits)
				}
			}
		}
	}
}

// TKB-26: a lingering halted/blocked run yields to a strictly newer
// frontmatter status; a live run, or any doubt about the timestamps, keeps it.
func TestEffectiveRun(t *testing.T) {
	const older, newer = "2026-09-28T09:00:00Z", "2026-09-28T10:00:00Z"
	cases := []struct {
		run, runAt, front, frontAt string
		want                       string
	}{
		{"halted", older, "done", newer, ""},
		{"blocked", older, "waiting", newer, ""},
		{"halted", newer, "done", older, "halted"},       // run is newer
		{"halted", older, "done", older, "halted"},       // a tie is not newer
		{"blocked", older, "", newer, "blocked"},         // no frontmatter status
		{"halted", "", "done", newer, "halted"},          // run time missing
		{"halted", older, "done", "", "halted"},          // front time missing
		{"blocked", "garbage", "done", newer, "blocked"}, // unparseable run
		{"blocked", older, "done", "garbage", "blocked"}, // unparseable front
		{"running", older, "done", newer, "running"},     // live runs are kept
		{"stalled", older, "done", newer, "stalled"},
		{"dead", older, "done", newer, "dead"},
		{"", "", "done", newer, ""},
	}
	for _, c := range cases {
		card := Card{RunState: c.run, RunUpdated: c.runAt, AgentStatus: c.front, AgentStatusAt: c.frontAt}
		if got := EffectiveRun(card); got != c.want {
			t.Errorf("EffectiveRun(%+v) = %q, want %q", c, got, c.want)
		}
	}
}
