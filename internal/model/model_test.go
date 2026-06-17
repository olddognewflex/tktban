package model

import (
	"reflect"
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
