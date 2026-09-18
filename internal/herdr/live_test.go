package herdr

import (
	"context"
	"errors"
	"testing"
)

func agent(pane string, status Status, cwd, fg string) Agent {
	name := "claude"
	return Agent{PaneID: pane, WorkspaceID: "w1", TabID: "w1:t1", Agent: &name, Status: status, Cwd: cwd, ForegroundCwd: fg}
}

// keysByDir fakes the dir -> branch -> key step.
func keysByDir(m map[string]string) func(string) string {
	return func(dir string) string { return m[dir] }
}

func TestResolvePrefersForegroundCwd(t *testing.T) {
	keyFor := keysByDir(map[string]string{"/shell": "TKB-1", "/agent": "TKB-2"})
	got := Resolve([]Agent{agent("p1", StatusWorking, "/shell", "/agent")}, keyFor)
	if _, ok := got["TKB-1"]; ok {
		t.Fatal("pane cwd used although foreground_cwd was set")
	}
	if got["TKB-2"].Status != StatusWorking {
		t.Fatalf("TKB-2 = %+v, want working", got["TKB-2"])
	}
	// No foreground cwd: fall back to cwd.
	got = Resolve([]Agent{agent("p1", StatusBlocked, "/shell", "")}, keyFor)
	if got["TKB-1"].Status != StatusBlocked {
		t.Fatalf("cwd fallback: %+v", got)
	}
}

func TestResolveSkipsAgentlessAndUnkeyed(t *testing.T) {
	released := agent("p1", StatusWorking, "/a", "/a")
	released.Agent = nil
	blank := agent("p2", StatusWorking, "/a", "/a")
	empty := ""
	blank.Agent = &empty
	unkeyed := agent("p3", StatusWorking, "/main", "/main")
	nodir := agent("p4", StatusWorking, "", "")
	got := Resolve([]Agent{released, blank, unkeyed, nodir}, keysByDir(map[string]string{"/a": "TKB-1"}))
	if len(got) != 0 {
		t.Fatalf("want nothing resolved, got %+v", got)
	}
}

func TestResolveBlockedBeatsWorking(t *testing.T) {
	keyFor := keysByDir(map[string]string{"/a": "TKB-1", "/b": "TKB-1"})
	for _, order := range [][]Agent{
		{agent("p1", StatusWorking, "", "/a"), agent("p2", StatusBlocked, "", "/b")},
		{agent("p2", StatusBlocked, "", "/b"), agent("p1", StatusWorking, "", "/a")},
	} {
		if got := Resolve(order, keyFor)["TKB-1"].Status; got != StatusBlocked {
			t.Fatalf("status = %q, want blocked", got)
		}
	}
	// working beats idle/done/unknown
	got := Resolve([]Agent{
		agent("p1", StatusUnknown, "", "/a"),
		agent("p2", StatusDone, "", "/a"),
		agent("p3", StatusWorking, "", "/a"),
		agent("p4", StatusIdle, "", "/a"),
	}, keyFor)
	if got["TKB-1"].Status != StatusWorking {
		t.Fatalf("status = %q, want working", got["TKB-1"].Status)
	}
}

func TestResolveIdleEqualsDone(t *testing.T) {
	keyFor := keysByDir(map[string]string{"/a": "TKB-1"})
	// Equal rank: neither displaces the other, both beat unknown.
	if got := Resolve([]Agent{agent("p1", StatusDone, "", "/a"), agent("p2", StatusIdle, "", "/a")}, keyFor); got["TKB-1"].Status != StatusDone {
		t.Fatalf("idle displaced done: %q", got["TKB-1"].Status)
	}
	if got := Resolve([]Agent{agent("p1", StatusIdle, "", "/a"), agent("p2", StatusDone, "", "/a")}, keyFor); got["TKB-1"].Status != StatusIdle {
		t.Fatalf("done displaced idle: %q", got["TKB-1"].Status)
	}
	if got := Resolve([]Agent{agent("p1", StatusUnknown, "", "/a"), agent("p2", StatusIdle, "", "/a")}, keyFor); got["TKB-1"].Status != StatusIdle {
		t.Fatalf("unknown outranked idle: %q", got["TKB-1"].Status)
	}
}

func TestResolveKeepsPaneRefs(t *testing.T) {
	a := agent("wC:p1", StatusWorking, "", "/a")
	a.WorkspaceID, a.TabID, a.Focused = "wC", "wC:t1", true
	b := agent("wD:p3", StatusIdle, "", "/a")
	got := Resolve([]Agent{a, b}, keysByDir(map[string]string{"/a": "TKB-1"}))["TKB-1"]
	if len(got.Panes) != 2 {
		t.Fatalf("panes = %+v", got.Panes)
	}
	want := PaneRef{PaneID: "wC:p1", WorkspaceID: "wC", TabID: "wC:t1", Status: StatusWorking, Focused: true}
	if got.Panes[0] != want || got.Panes[1].PaneID != "wD:p3" {
		t.Fatalf("pane refs = %+v", got.Panes)
	}
}

func TestResolveReadsEachDirOnce(t *testing.T) {
	calls := 0
	keyFor := func(string) string { calls++; return "TKB-1" }
	Resolve([]Agent{agent("p1", StatusIdle, "", "/a"), agent("p2", StatusIdle, "", "/a")}, keyFor)
	if calls != 1 {
		t.Fatalf("keyForDir called %d times for one dir", calls)
	}
}

func TestProbeRejectsProtocolMismatch(t *testing.T) {
	s := &SocketSource{Client: pipeClient(t, func(map[string]any) string {
		return `{"id":"x","result":{"type":"pong","version":"0.10.0","protocol":23}}`
	})}
	if err := s.Probe(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("want ErrProtocol, got %v", err)
	}
	s.Client = pipeClient(t, func(map[string]any) string {
		return `{"id":"x","result":{"type":"pong","version":"0.9.0","protocol":22}}`
	})
	if err := s.Probe(context.Background()); err != nil {
		t.Fatalf("protocol 22 rejected: %v", err)
	}
}

func TestPollResolvesAgentList(t *testing.T) {
	s := &SocketSource{
		Client:    pipeClient(t, func(map[string]any) string { return compact(t, agentListReply) }),
		KeyForDir: keysByDir(map[string]string{"/src/tktban-wt": "TKB-22"}),
	}
	got, err := s.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["TKB-22"].Status != StatusWorking {
		t.Fatalf("poll = %+v", got)
	}
}
