package herdr

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// TestFocusPaneRequest pins the exact wire request over a real unix socket:
// method pane.focus with a pane_id param. agent.focus is the method that takes
// "target"; sending that shape here fails with invalid_request.
func TestFocusPaneRequest(t *testing.T) {
	var got map[string]any
	c := realSocket(t, func(conn net.Conn) {
		got = readReq(t, conn)
		conn.Write([]byte(`{"id":"x","result":{"type":"pane_info","pane_id":"wC:p1","workspace_id":"wC"}}` + "\n"))
	})
	if err := c.FocusPane(context.Background(), "wC:p1"); err != nil {
		t.Fatalf("focus: %v", err)
	}
	if got["method"] != "pane.focus" {
		t.Fatalf("method = %v, want pane.focus", got["method"])
	}
	params, ok := got["params"].(map[string]any)
	if !ok {
		t.Fatalf("params = %v, want an object", got["params"])
	}
	if params["pane_id"] != "wC:p1" {
		t.Fatalf("params = %v, want {\"pane_id\":\"wC:p1\"}", params)
	}
	if len(params) != 1 {
		t.Fatalf("params = %v, want pane_id alone (target is agent.focus's field)", params)
	}
}

// A pane that closed between the poll and the keypress gives pane_not_found,
// and the UI branches on that code, so it must survive as an *APIError.
func TestFocusPaneNotFound(t *testing.T) {
	c := pipeClient(t, func(map[string]any) string {
		return `{"id":"","error":{"code":"pane_not_found","message":"pane wC:p9 not found"}}`
	})
	err := c.FocusPane(context.Background(), "wC:p9")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "pane_not_found" {
		t.Fatalf("want APIError pane_not_found, got %v", err)
	}
}

// A herdr that accepts the focus but never replies must not wedge the board.
func TestFocusPaneTimeout(t *testing.T) {
	c := pipeClient(t, func(map[string]any) string { return "" }) // never replies
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.FocusPane(ctx, "wC:p1") }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want deadline exceeded, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("focus outlived its context")
	}
}

func paneRef(id string, st Status, focused bool) PaneRef {
	return PaneRef{PaneID: id, WorkspaceID: "wC", TabID: "wC:t1", Status: st, Focused: focused}
}

// A pane herdr already has focused is where the human last was, so it wins
// even over a blocked pane elsewhere.
func TestPickPaneFocusedWins(t *testing.T) {
	l := Live{Panes: []PaneRef{
		paneRef("p1", StatusBlocked, false),
		paneRef("p2", StatusIdle, true),
		paneRef("p3", StatusWorking, false),
	}}
	got, ok := PickPane(l)
	if !ok || got.PaneID != "p2" {
		t.Fatalf("pick = %+v ok=%v, want the focused p2", got, ok)
	}
	// Order must not matter: focused first is still focused.
	l.Panes = []PaneRef{paneRef("p2", StatusIdle, true), paneRef("p1", StatusBlocked, false)}
	if got, _ := PickPane(l); got.PaneID != "p2" {
		t.Fatalf("pick = %+v, want p2 whichever way round", got)
	}
}

func TestPickPaneRanksBlockedOverWorking(t *testing.T) {
	cases := []struct {
		name  string
		panes []PaneRef
		want  string
	}{
		{"blocked over working", []PaneRef{paneRef("p1", StatusWorking, false), paneRef("p2", StatusBlocked, false)}, "p2"},
		{"blocked first still wins", []PaneRef{paneRef("p2", StatusBlocked, false), paneRef("p1", StatusWorking, false)}, "p2"},
		{"working over idle", []PaneRef{paneRef("p1", StatusIdle, false), paneRef("p2", StatusWorking, false)}, "p2"},
		{"working over done", []PaneRef{paneRef("p1", StatusDone, false), paneRef("p2", StatusWorking, false)}, "p2"},
		{"idle over unknown", []PaneRef{paneRef("p1", StatusUnknown, false), paneRef("p2", StatusIdle, false)}, "p2"},
	}
	for _, c := range cases {
		got, ok := PickPane(Live{Panes: c.panes})
		if !ok || got.PaneID != c.want {
			t.Errorf("%s: pick = %+v ok=%v, want %s", c.name, got, ok, c.want)
		}
	}
}

// Equal panes keep agent.list order (which Resolve preserves), so the jump
// key lands on the same pane every time rather than wandering between polls.
func TestPickPaneStableOnTies(t *testing.T) {
	l := Live{Panes: []PaneRef{
		paneRef("p1", StatusWorking, false),
		paneRef("p2", StatusWorking, false),
		paneRef("p3", StatusWorking, false),
	}}
	for i := range 5 {
		if got, _ := PickPane(l); got.PaneID != "p1" {
			t.Fatalf("run %d: pick = %s, want the first listed p1", i, got.PaneID)
		}
	}
	// idle and done tie by design: the first listed still wins.
	l.Panes = []PaneRef{paneRef("p1", StatusDone, false), paneRef("p2", StatusIdle, false)}
	if got, _ := PickPane(l); got.PaneID != "p1" {
		t.Fatalf("idle displaced done: %s", got.PaneID)
	}
	l.Panes = []PaneRef{paneRef("p1", StatusIdle, false), paneRef("p2", StatusDone, false)}
	if got, _ := PickPane(l); got.PaneID != "p1" {
		t.Fatalf("done displaced idle: %s", got.PaneID)
	}
}

func TestPickPaneEmpty(t *testing.T) {
	if got, ok := PickPane(Live{}); ok {
		t.Fatalf("no panes must not pick one, got %+v", got)
	}
	if got, ok := PickPane(Live{Status: StatusWorking, Panes: []PaneRef{}}); ok {
		t.Fatalf("empty pane slice must not pick one, got %+v", got)
	}
}
