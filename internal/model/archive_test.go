package model

import (
	"reflect"
	"testing"
)

const day = 86400.0

func laneSecs(pairs map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for k, v := range pairs {
		if v == nil {
			out[k] = nil
			continue
		}
		out[k] = map[string]any{"key": k, "seconds": v}
	}
	return out
}

func TestArchiveCandidatesAtThreshold(t *testing.T) {
	tickets := []Ticket{ticket("TKT-1", "done")}
	got := ArchiveCandidates(tickets, laneSecs(map[string]any{"TKT-1": 7 * day}), 7)
	if !reflect.DeepEqual(got, []string{"TKT-1"}) {
		t.Fatalf("at threshold = %v, want [TKT-1]", got)
	}
}

func TestArchiveCandidatesJustBelowThreshold(t *testing.T) {
	tickets := []Ticket{ticket("TKT-1", "done")}
	if got := ArchiveCandidates(tickets, laneSecs(map[string]any{"TKT-1": 7*day - 1}), 7); got != nil {
		t.Fatalf("just below threshold = %v, want none", got)
	}
}

func TestArchiveCandidatesOnlyDone(t *testing.T) {
	tickets := []Ticket{
		ticket("TKT-1", "todo"),
		ticket("TKT-2", "done"),
		ticket("TKT-3", "archived"),
		ticket("TKT-4", "in_progress"),
	}
	lane := laneSecs(map[string]any{
		"TKT-1": 30 * day, "TKT-2": 30 * day, "TKT-3": 30 * day, "TKT-4": 30 * day,
	})
	if got := ArchiveCandidates(tickets, lane, 7); !reflect.DeepEqual(got, []string{"TKT-2"}) {
		t.Fatalf("candidates = %v, want [TKT-2]", got)
	}
}

func TestArchiveCandidatesDisabled(t *testing.T) {
	tickets := []Ticket{ticket("TKT-1", "done")}
	lane := laneSecs(map[string]any{"TKT-1": 365 * day})
	for _, days := range []float64{0, -1} {
		if got := ArchiveCandidates(tickets, lane, days); got != nil {
			t.Fatalf("days=%v: candidates = %v, want none", days, got)
		}
	}
}

func TestArchiveCandidatesMissingOrBadLaneData(t *testing.T) {
	tickets := []Ticket{
		ticket("TKT-1", "done"), // no entry at all
		ticket("TKT-2", "done"), // nil entry (no lane history)
		ticket("TKT-3", "done"), // seconds missing
		ticket("TKT-4", "done"), // seconds not a number
		ticket("TKT-5", "done"), // seconds zero (backend without lane time)
	}
	lane := map[string]map[string]any{
		"TKT-2": nil,
		"TKT-3": {"key": "TKT-3", "human": "9d"},
		"TKT-4": {"key": "TKT-4", "seconds": "999999999"},
		"TKT-5": {"key": "TKT-5", "seconds": 0.0},
	}
	if got := ArchiveCandidates(tickets, lane, 7); got != nil {
		t.Fatalf("candidates = %v, want none", got)
	}
	if got := ArchiveCandidates(tickets, nil, 7); got != nil {
		t.Fatalf("nil lane map: candidates = %v, want none", got)
	}
}

func TestArchiveCandidatesSorted(t *testing.T) {
	tickets := []Ticket{ticket("TKT-9", "done"), ticket("TKT-10", "done"), ticket("TKT-2", "done")}
	lane := laneSecs(map[string]any{"TKT-9": 8 * day, "TKT-10": 8 * day, "TKT-2": 8 * day})
	want := []string{"TKT-10", "TKT-2", "TKT-9"}
	if got := ArchiveCandidates(tickets, lane, 7); !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestHasRole(t *testing.T) {
	roles := []RolePair{{"todo", "To Do"}, {"done", "Done"}, {"archived", "Archived"}}
	if !HasRole(roles, RoleArchived) {
		t.Fatal("archived role not found")
	}
	if HasRole(roles[:2], RoleArchived) {
		t.Fatal("archived role found where not configured")
	}
	if HasRole(nil, RoleDone) {
		t.Fatal("role found in empty config")
	}
}
