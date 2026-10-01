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

// keysByDir fakes the dir -> branch -> key step with one key per dir.
func keysByDir(m map[string]string) func(string) []string {
	return func(dir string) []string {
		if k := m[dir]; k != "" {
			return []string{k}
		}
		return nil
	}
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
	want := PaneRef{PaneID: "wC:p1", WorkspaceID: "wC", TabID: "wC:t1", Status: StatusWorking, Focused: true, Dir: "/a"}
	if got.Panes[0] != want || got.Panes[1].PaneID != "wD:p3" {
		t.Fatalf("pane refs = %+v", got.Panes)
	}
}

func TestResolveReadsEachDirOnce(t *testing.T) {
	calls := 0
	keyFor := func(string) []string { calls++; return []string{"TKB-1"} }
	Resolve([]Agent{agent("p1", StatusIdle, "", "/a"), agent("p2", StatusIdle, "", "/a")}, keyFor)
	if calls != 1 {
		t.Fatalf("keysForDir called %d times for one dir", calls)
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
		Client: pipeClient(t, func(map[string]any) string { return compact(t, agentListReply) }),
		KeysForDir: func(_ context.Context, dir string) []string {
			return keysByDir(map[string]string{"/src/tktban-wt": "TKB-22"})(dir)
		},
	}
	snap, err := s.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := snap.ByKey
	if len(got) != 1 || got["TKB-22"].Status != StatusWorking {
		t.Fatalf("poll = %+v", got)
	}
}

// AgentPanes lists every pane with an agent, including one whose branch names
// no ticket, and leaves out a pane whose agent herdr has released.
func TestPollReportsAgentPanesWithoutKeys(t *testing.T) {
	s := &SocketSource{
		Client:     pipeClient(t, func(map[string]any) string { return compact(t, agentListReply) }),
		KeysForDir: func(context.Context, string) []string { return nil }, // detached HEAD
	}
	snap, err := s.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.ByKey) != 0 {
		t.Fatalf("ByKey = %+v, want no tickets", snap.ByKey)
	}
	if !snap.AgentPanes["wC:p1"] || snap.AgentPanes["wD:p2"] || len(snap.AgentPanes) != 1 {
		t.Fatalf("AgentPanes = %v, want only the live agent wC:p1", snap.AgentPanes)
	}
}

// A branch naming several keys badges each; merging still ranks per key.
func TestResolveMapsPaneUnderEveryKey(t *testing.T) {
	keysFor := func(dir string) []string {
		if dir == "/revert" {
			return []string{"REVERT-45", "TKB-22"}
		}
		return []string{"TKB-22"}
	}
	got := Resolve([]Agent{
		agent("p1", StatusWorking, "", "/revert"),
		agent("p2", StatusBlocked, "", "/other"),
	}, keysFor)
	if got["REVERT-45"].Status != StatusWorking || len(got["REVERT-45"].Panes) != 1 {
		t.Fatalf("REVERT-45 = %+v", got["REVERT-45"])
	}
	if got["TKB-22"].Status != StatusBlocked || len(got["TKB-22"].Panes) != 2 {
		t.Fatalf("TKB-22 = %+v", got["TKB-22"])
	}
}

// A resolve that outlives the poll's context is a failed poll, not a result.
func TestPollFailsWhenResolveOutlivesContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &SocketSource{
		Client:     pipeClient(t, func(map[string]any) string { return compact(t, agentListReply) }),
		KeysForDir: func(context.Context, string) []string { cancel(); return []string{"TKB-22"} },
	}
	if _, err := s.Poll(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// Poll fills each pane's Repo, reading each distinct dir once.
func TestPollFillsRepoOncePerDir(t *testing.T) {
	calls := map[string]int{}
	s := &SocketSource{
		Client: pipeClient(t, func(map[string]any) string { return compact(t, agentListReply) }),
		KeysForDir: func(context.Context, string) []string {
			return []string{"TKB-22", "TKB-23"}
		},
		RepoForDir: func(dir string) string { calls[dir]++; return "o/n" },
	}
	snap, err := s.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := snap.ByKey
	if len(got) == 0 {
		t.Fatal("poll returned no tickets")
	}
	for key, l := range got {
		for _, p := range l.Panes {
			if p.Repo != "o/n" || p.Dir == "" {
				t.Fatalf("%s pane = %+v, want Repo o/n and a Dir", key, p)
			}
		}
	}
	for dir, n := range calls {
		if n != 1 {
			t.Fatalf("RepoForDir(%q) called %d times in one poll", dir, n)
		}
	}
}

// Distinct dirs get their own lookups.
func TestAttachReposLooksUpEachDir(t *testing.T) {
	calls := map[string]int{}
	s := &SocketSource{RepoForDir: func(dir string) string { calls[dir]++; return "r" + dir }}
	byKey := map[string]Live{
		"TKB-1": {Panes: []PaneRef{{Dir: "/a"}, {Dir: "/b"}}},
		"TKB-2": {Panes: []PaneRef{{Dir: "/a"}}},
	}
	s.attachRepos(context.Background(), byKey)
	if calls["/a"] != 1 || calls["/b"] != 1 || len(calls) != 2 {
		t.Fatalf("calls = %v, want one per distinct dir", calls)
	}
	if byKey["TKB-1"].Panes[1].Repo != "r/b" || byKey["TKB-2"].Panes[0].Repo != "r/a" {
		t.Fatalf("repos = %+v", byKey)
	}
}

// A cancelled ctx stops attachRepos; panes left unfilled keep Repo "", which
// never matches a board repo.
func TestAttachReposStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	s := &SocketSource{RepoForDir: func(string) string { calls++; cancel(); return "o/n" }}
	byKey := map[string]Live{
		"TKB-1": {Panes: []PaneRef{{Dir: "/a"}, {Dir: "/b"}, {Dir: "/c"}}},
	}
	s.attachRepos(ctx, byKey)
	if calls != 1 {
		t.Fatalf("RepoForDir called %d times after cancel, want 1", calls)
	}
	p := byKey["TKB-1"].Panes
	if p[0].Repo != "o/n" || p[1].Repo != "" || p[2].Repo != "" {
		t.Fatalf("panes = %+v, want only the first filled", p)
	}
}
