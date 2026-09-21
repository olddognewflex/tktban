// QA Author — adversarial tests
// Break-It dimensions covered: PickPane across several workspaces/tabs, a
// focused pane at the very bottom of the rank order still winning, stability
// over a large pane list, and Live.Status being irrelevant to the pick (only
// Panes matters).
package herdr

import "testing"

// A ticket can have agent panes spread across several herdr workspaces (e.g.
// a worktree opened in two different windows). PickPane must not let
// workspace or tab identity affect the choice — only Focused and rank do.
func TestPickPaneAcrossWorkspaces(t *testing.T) {
	l := Live{Panes: []PaneRef{
		{PaneID: "wA:p1", WorkspaceID: "wA", TabID: "wA:t1", Status: StatusWorking},
		{PaneID: "wB:p9", WorkspaceID: "wB", TabID: "wB:t3", Status: StatusBlocked},
		{PaneID: "wC:p2", WorkspaceID: "wC", TabID: "wC:t1", Status: StatusIdle},
	}}
	got, ok := PickPane(l)
	if !ok || got.PaneID != "wB:p9" {
		t.Fatalf("pick = %+v ok=%v, want the blocked pane in a different workspace", got, ok)
	}
}

// The lowest-ranked pane still wins outright when herdr already has it
// focused, even against a blocked pane in another workspace entirely.
func TestPickPaneFocusedLowestRankStillWins(t *testing.T) {
	l := Live{Panes: []PaneRef{
		{PaneID: "wA:blocked", WorkspaceID: "wA", Status: StatusBlocked},
		{PaneID: "wB:working", WorkspaceID: "wB", Status: StatusWorking},
		{PaneID: "wC:unknown", WorkspaceID: "wC", Status: StatusUnknown, Focused: true},
	}}
	got, ok := PickPane(l)
	if !ok || got.PaneID != "wC:unknown" {
		t.Fatalf("pick = %+v ok=%v, want the focused-but-unknown pane wC:unknown", got, ok)
	}
}

// Live.Status is a separately maintained aggregate (set by Resolve); PickPane
// must never consult it, only the individual Panes' own Status. An empty or
// inconsistent Live.Status must not change the outcome.
func TestPickPaneIgnoresLiveStatusField(t *testing.T) {
	panes := []PaneRef{
		{PaneID: "p1", Status: StatusIdle},
		{PaneID: "p2", Status: StatusWorking},
	}
	for _, aggregate := range []Status{"", StatusBlocked, StatusUnknown, Status("bogus")} {
		got, ok := PickPane(Live{Status: aggregate, Panes: panes})
		if !ok || got.PaneID != "p2" {
			t.Fatalf("aggregate status %q: pick = %+v ok=%v, want p2 by its own rank", aggregate, got, ok)
		}
	}
}

// A ticket with a very large number of agent panes (a pathological but
// possible herdr state) must still resolve deterministically to the single
// correct pane, without picking up any of the filler panes.
func TestPickPaneManyPanes(t *testing.T) {
	const n = 500
	var panes []PaneRef
	for i := 0; i < n; i++ {
		st := StatusIdle
		if i%3 == 0 {
			st = StatusUnknown
		}
		panes = append(panes, PaneRef{PaneID: "filler", Status: st})
	}
	// One genuinely urgent pane buried in the middle.
	panes[n/2] = PaneRef{PaneID: "the-blocked-one", Status: StatusBlocked}
	got, ok := PickPane(Live{Panes: panes})
	if !ok || got.PaneID != "the-blocked-one" {
		t.Fatalf("pick among %d panes = %+v ok=%v, want the-blocked-one", n, got, ok)
	}

	// Now bury a focused pane instead: it must win over the blocked one.
	panes[n/4] = PaneRef{PaneID: "the-focused-one", Status: StatusIdle, Focused: true}
	got, ok = PickPane(Live{Panes: panes})
	if !ok || got.PaneID != "the-focused-one" {
		t.Fatalf("pick among %d panes = %+v ok=%v, want the-focused-one over blocked", n, got, ok)
	}
}
