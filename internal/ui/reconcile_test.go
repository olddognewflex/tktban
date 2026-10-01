package ui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/tkt"
)

// boardRepo is the [vcs].repo captureRunner reports by default.
const boardRepo = "olddognewflex/tktban"

// repoPanes is a herdr poll result: each key has one pane with the given status,
// in the given repo.
func repoPanes(repo string, pairs ...string) map[string]herdr.Live {
	out := map[string]herdr.Live{}
	for i := 0; i+1 < len(pairs); i += 2 {
		st := herdr.Status(pairs[i+1])
		out[pairs[i]] = herdr.Live{Status: st, Panes: []herdr.PaneRef{{PaneID: "w1:" + pairs[i], Status: st, Repo: repo}}}
	}
	return out
}

// syncList points the fake `tkt list` at what `tkt view` reports, so a refresh
// after a write-back shows the written value on the cards.
func syncList(cr *captureRunner) {
	var rows []string
	for _, k := range []string{"TKT-1", "TKT-2"} {
		rows = append(rows, `{"key":"`+k+`","summary":"s","status_role":"todo","blocked_by":[]`+cr.agentFields(k)+`}`)
	}
	cr.list = "[" + strings.Join(rows, ",") + "]"
}

// reconNow is the clock reconBoard pins: after the tickets' default
// agent_status_at, so a closed pane's processing predates its last sighting.
var reconNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// pinned is the time the package clock reads while pinNow has it.
var pinned time.Time

// pinNow pins the package clock for one test; elapse moves it on.
func pinNow(t *testing.T, at time.Time) {
	t.Helper()
	pinned = at
	now = func() time.Time { return pinned }
	t.Cleanup(func() { now = time.Now })
}

// elapse moves the pinned clock on by d.
func elapse(d time.Duration) { pinned = pinned.Add(d) }

// reconBoard is a loaded, live board whose TKT-1 and TKT-2 frontmatter read
// front, with write-back on or off and herdr showing a working TKT-1 pane in
// the board's repo. setup runs before the board starts, so it can change the
// fake tkt or herdr.
func reconBoard(t *testing.T, on bool, front string, setup func(*captureRunner, *fakeLive)) (Model, *captureRunner, *fakeLive) {
	t.Helper()
	pinNow(t, reconNow)
	cr := &captureRunner{
		viewAgent:   map[string]string{"TKT-1": front, "TKT-2": front},
		viewAgentAt: map[string]string{"TKT-1": "2026-09-30T00:00:00Z", "TKT-2": "2026-09-30T00:00:00Z"},
	}
	src := &fakeLive{byKey: repoPanes(boardRepo, "TKT-1", "working")}
	if setup != nil {
		setup(cr, src)
	}
	syncList(cr)
	path := filepath.Join(t.TempDir(), "settings.toml")
	if on {
		if err := os.WriteFile(path, []byte("reconcile_agent_status = true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := New(tkt.New("", "tkt").WithRunner(cr.run), 10, true, path)
	m.width, m.height = 120, 30
	m = loadBoard(m.WithLive(src))
	if cmd := m.reconcileInit(); cmd != nil {
		m, _ = update(m, cmd())
	}
	return goLive(t, m), cr, src
}

// pollCmd runs one scheduled poll like poll, and returns what the board
// answered the result with (which carries any write-back batch).
func pollCmd(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	m, cmd := update(m, liveTickMsg{})
	if cmd == nil {
		t.Fatal("tick did not poll")
	}
	return update(m, cmd())
}

// reconPoll runs one poll and, if it started a write-back batch, runs the
// batch and feeds its result back. The returned command is the board's
// answer to that result (nil when no batch ran).
func reconPoll(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	m, cmd := pollCmd(t, m)
	if !m.recon.inFlight {
		return m, nil
	}
	msg, ok := await[reconcileMsg](t, cmd)
	if !ok {
		t.Fatal("write-back batch started but never reported")
	}
	return update(m, msg)
}

// agentWrites is every `tkt edit … --agent-status` the board made.
func agentWrites(cr *captureRunner) [][]string {
	var out [][]string
	for _, call := range cr.callsTo("edit") {
		if slices.Contains(call, "--agent-status") {
			out = append(out, call)
		}
	}
	return out
}

// viewStatus is what `tkt view KEY --json` says, through the board's own tkt.
func viewStatus(t *testing.T, m Model, key string) string {
	t.Helper()
	v, err := m.tkt.View(key)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := v["agent_status"].(string)
	return s
}

// Acceptance: an agent pane killed mid-processing reads idle after the
// debounce (both the polls and the time), in the frontmatter and in `tkt
// view`, with one write.
func TestReconcileIdleWhenPaneCloses(t *testing.T) {
	m, cr, src := reconBoard(t, true, "processing", nil)
	src.byKey = map[string]herdr.Live{}

	m, _ = reconPoll(t, m)
	if w := agentWrites(cr); len(w) != 0 {
		t.Fatalf("wrote after one absent poll: %v", w)
	}
	m, _ = reconPoll(t, m)
	if w := agentWrites(cr); len(w) != 0 {
		t.Fatalf("wrote before reconcileGoneFor had passed: %v", w)
	}
	elapse(reconcileGoneFor)
	m, refresh := reconPoll(t, m)
	w := agentWrites(cr)
	if len(w) != 1 || !eq(w[0], "edit", "TKT-1", "--agent-status", "idle", "--json") {
		t.Fatalf("writes = %v, want one idle for TKT-1", w)
	}
	if got := viewStatus(t, m, "TKT-1"); got != "idle" {
		t.Fatalf("tkt view agent_status = %q, want idle", got)
	}
	if _, ok := await[boardMsg](t, refresh); !ok {
		t.Fatal("a write did not refresh the board")
	}
	for range 3 {
		m, _ = reconPoll(t, m)
	}
	if w := agentWrites(cr); len(w) != 1 {
		t.Fatalf("wrote again in the same episode: %v", w)
	}
	// TKT-2 never had a pane and is still processing: never seen, never written.
	if got := viewStatus(t, m, "TKT-2"); got != "processing" {
		t.Fatalf("TKT-2 = %q, want processing untouched", got)
	}
}

// Acceptance: none of these may write, whatever herdr does.
func TestReconcileNoWrites(t *testing.T) {
	running := `{"agents":[{"key":"TKT-1","state":"running","updated":"2026-10-01T00:00:00Z"}]}`
	stalled := `{"agents":[{"key":"TKT-1","state":"stalled","updated":"2026-10-01T00:00:00Z"}]}`
	cases := []struct {
		name  string
		on    bool
		front string
		setup func(*captureRunner, *fakeLive)
		// drive takes the board from a working TKT-1 pane to whatever the
		// case is about; nil closes the pane for three good polls, with
		// reconcileGoneFor passed, which is enough for any idle due.
		drive func(*testing.T, Model, *fakeLive) Model
	}{
		{name: "setting off", front: "processing"},
		{name: "pane in another repo", on: true, front: "processing", setup: func(_ *captureRunner, s *fakeLive) {
			s.byKey = repoPanes("someone/else", "TKT-1", "working")
		}},
		{name: "pane repo unknown", on: true, front: "processing", setup: func(_ *captureRunner, s *fakeLive) {
			s.byKey = repoPanes("", "TKT-1", "working")
		}},
		{name: "board has no repo", on: true, front: "processing", setup: func(c *captureRunner, _ *fakeLive) {
			c.vcs = `{"provider":"github","repo":""}`
		}},
		{name: "repo unreadable", on: true, front: "processing", setup: func(c *captureRunner, _ *fakeLive) {
			c.vcs = failReply
		}},
		{name: "headless run running", on: true, front: "processing", setup: func(c *captureRunner, _ *fakeLive) {
			c.agents = running
		}},
		{name: "headless run stalled", on: true, front: "processing", setup: func(c *captureRunner, _ *fakeLive) {
			c.agents = stalled
		}},
		{name: "skill wrote waiting", on: true, front: "waiting"},
		{name: "already idle", on: true, front: "idle"},
		{name: "done is left alone", on: true, front: "done"},
		{name: "one absent poll", on: true, front: "processing", drive: func(t *testing.T, m Model, s *fakeLive) Model {
			s.byKey = map[string]herdr.Live{}
			elapse(reconcileGoneFor)
			m, _ = reconPoll(t, m)
			s.byKey = repoPanes(boardRepo, "TKT-1", "working")
			for range 3 {
				m, _ = reconPoll(t, m)
			}
			return m
		}},
		{name: "never-seen key", on: true, front: "processing", setup: func(_ *captureRunner, s *fakeLive) {
			s.byKey = map[string]herdr.Live{}
		}},
		{name: "poll failures through fallback", on: true, front: "processing", drive: func(t *testing.T, m Model, s *fakeLive) Model {
			s.pollErr = errors.New("socket gone")
			elapse(reconcileGoneFor)
			for range liveMaxFails + 3 {
				m, _ = reconPoll(t, m)
			}
			if m.live.on {
				t.Fatal("setup: board did not fall back")
			}
			if k := m.recon.keys["TKT-1"]; k == nil || k.absentPolls != 0 {
				t.Fatalf("failed polls counted as absence: %+v", k)
			}
			return m
		}},
		{name: "protocol mismatch", on: true, front: "processing", drive: func(t *testing.T, m Model, s *fakeLive) Model {
			m, _ = update(m, liveProbeMsg{err: herdr.ErrProtocol})
			s.byKey = map[string]herdr.Live{}
			for range 3 {
				m, _ = update(m, liveMsg{byKey: map[string]herdr.Live{}})
			}
			if m.recon.inFlight {
				t.Fatal("a dropped source started a write-back")
			}
			return m
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, cr, src := reconBoard(t, c.on, c.front, c.setup)
			drive := c.drive
			if drive == nil {
				drive = func(t *testing.T, m Model, s *fakeLive) Model {
					s.byKey = map[string]herdr.Live{}
					elapse(reconcileGoneFor)
					for range 3 {
						m, _ = reconPoll(t, m)
					}
					return m
				}
			}
			m = drive(t, m, src)
			if w := agentWrites(cr); len(w) != 0 {
				t.Fatalf("board wrote agent_status: %v", w)
			}
			if got := viewStatus(t, m, "TKT-1"); got != c.front {
				t.Fatalf("tkt view = %q, want %q untouched", got, c.front)
			}
		})
	}
}

// Only a running or stalled headless run owns the ticket: a halted run, or a
// tkt with no agents verb at all, still lets a closed pane write idle.
func TestReconcileHeadlessRunStatesThatDoNotOwnIt(t *testing.T) {
	for _, agents := range []string{
		`{"agents":[{"key":"TKT-1","state":"halted","updated":"2026-10-01T00:00:00Z"}]}`,
		failReply, // a tkt without the verb has no headless runs
	} {
		m, _, src := reconBoard(t, true, "processing", func(c *captureRunner, _ *fakeLive) { c.agents = agents })
		src.byKey = map[string]herdr.Live{}
		elapse(reconcileGoneFor)
		m, _ = reconPoll(t, m)
		m, _ = reconPoll(t, m)
		if got := viewStatus(t, m, "TKT-1"); got != "idle" {
			t.Fatalf("agents %s: tkt view = %q, want idle", agents, got)
		}
	}
}

// Outside herdr there is no source: no repo read, and a stray poll result is
// ignored.
func TestReconcileOffWithoutLiveSource(t *testing.T) {
	cr := &captureRunner{viewAgent: map[string]string{"TKT-1": "processing"}, viewAgentAt: map[string]string{"TKT-1": "x"}}
	syncList(cr)
	path := filepath.Join(t.TempDir(), "settings.toml")
	if err := os.WriteFile(path, []byte("reconcile_agent_status = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(tkt.New("", "tkt").WithRunner(cr.run), 10, true, path).WithLive(nil)
	if cmd := m.reconcileInit(); cmd != nil {
		t.Fatal("repo read with no live source")
	}
	m = loadBoard(m)
	for range 3 {
		var cmd tea.Cmd
		m, cmd = update(m, liveMsg{byKey: map[string]herdr.Live{}})
		if cmd != nil {
			t.Fatal("poll result acted on with no live source")
		}
	}
	if w := agentWrites(cr); len(w) != 0 {
		t.Fatalf("wrote outside herdr: %v", w)
	}
}

// A write that fails is not retried every poll; a new episode (the pane comes
// back, then closes again) tries once more.
func TestReconcileOncePerEpisode(t *testing.T) {
	m, cr, src := reconBoard(t, true, "processing", func(c *captureRunner, _ *fakeLive) {
		c.failOn = map[string]bool{"edit TKT-1": true}
	})
	src.byKey = map[string]herdr.Live{}
	elapse(reconcileGoneFor)
	for range 5 {
		m, _ = reconPoll(t, m)
	}
	if n := len(agentWrites(cr)); n != 1 {
		t.Fatalf("attempts = %d, want 1", n)
	}
	if m.statusKind != "warn" || !strings.Contains(m.status, "TKT-1") {
		t.Fatalf("status = %q (%s), want a warning naming TKT-1", m.status, m.statusKind)
	}
	src.byKey = repoPanes(boardRepo, "TKT-1", "working")
	m, _ = reconPoll(t, m)
	src.byKey = map[string]herdr.Live{}
	elapse(reconcileGoneFor)
	for range 3 {
		m, _ = reconPoll(t, m)
	}
	if n := len(agentWrites(cr)); n != 2 {
		t.Fatalf("attempts = %d, want a second one for the new episode", n)
	}
}

// While a batch is in flight no other starts; a key that became due meanwhile
// is picked up once the batch lands.
func TestReconcileOneBatchInFlight(t *testing.T) {
	m, cr, src := reconBoard(t, true, "processing", func(_ *captureRunner, s *fakeLive) {
		s.byKey = repoPanes(boardRepo, "TKT-1", "working", "TKT-2", "working")
	})
	src.byKey = repoPanes(boardRepo, "TKT-2", "working")
	m, _ = reconPoll(t, m)
	elapse(reconcileGoneFor)
	m, held := pollCmd(t, m) // TKT-1 due: a batch starts and is held
	if !m.recon.inFlight {
		t.Fatal("no batch started")
	}
	src.byKey = map[string]herdr.Live{}
	elapse(reconcileGoneFor)
	calls := len(cr.calls)
	for range 3 {
		m, _ = pollCmd(t, m) // TKT-2 falls due, but must wait
	}
	if k := m.recon.keys["TKT-2"]; k.absentPolls < reconcileGonePolls || now().Sub(k.lastPresent) < reconcileGoneFor || k.idleTried {
		t.Fatalf("TKT-2 %+v: want due but not planned while a batch is in flight", *k)
	}
	if len(cr.calls) != calls {
		t.Fatalf("tkt called while the held batch had not run: %v", cr.calls[calls:])
	}
	msg, ok := await[reconcileMsg](t, held)
	if !ok {
		t.Fatal("held batch never reported")
	}
	m, _ = update(m, msg)
	syncList(cr)
	m = loadBoard(m)
	m, _ = reconPoll(t, m)
	if got := viewStatus(t, m, "TKT-2"); got != "idle" {
		t.Fatalf("TKT-2 = %q, want idle once the first batch landed", got)
	}
	if n := len(agentWrites(cr)); n != 2 {
		t.Fatalf("writes = %d, want 2", n)
	}
}

// Acceptance: a waiting the board wrote for a blocked pane does not stick: it
// is cleared to idle when the pane then closes.
func TestReconcileBoardWaitingClearedOnClose(t *testing.T) {
	m, cr, src := reconBoard(t, true, "processing", nil)
	src.byKey = repoPanes(boardRepo, "TKT-1", "blocked")
	m, _ = reconPoll(t, m)
	if got := viewStatus(t, m, "TKT-1"); got != "waiting" {
		t.Fatalf("blocked pane: tkt view = %q, want waiting", got)
	}
	syncList(cr)
	m = loadBoard(m)
	src.byKey = map[string]herdr.Live{}
	elapse(reconcileGoneFor)
	m, _ = reconPoll(t, m)
	m, _ = reconPoll(t, m)
	if got := viewStatus(t, m, "TKT-1"); got != "idle" {
		t.Fatalf("closed pane: tkt view = %q, want idle", got)
	}
	if n := len(agentWrites(cr)); n != 2 {
		t.Fatalf("writes = %d, want waiting then idle", n)
	}
}

// When herdr goes back to working, the board's own waiting becomes processing
// again, and a second prompt gets a second waiting.
func TestReconcileRestoresProcessing(t *testing.T) {
	m, cr, src := reconBoard(t, true, "processing", nil)
	src.byKey = repoPanes(boardRepo, "TKT-1", "blocked")
	m, _ = reconPoll(t, m)
	syncList(cr)
	m = loadBoard(m)
	src.byKey = repoPanes(boardRepo, "TKT-1", "working")
	m, _ = reconPoll(t, m)
	if got := viewStatus(t, m, "TKT-1"); got != "processing" {
		t.Fatalf("working again: tkt view = %q, want processing", got)
	}
	syncList(cr)
	m = loadBoard(m)
	src.byKey = repoPanes(boardRepo, "TKT-1", "blocked")
	m, _ = reconPoll(t, m)
	if got := viewStatus(t, m, "TKT-1"); got != "waiting" {
		t.Fatalf("blocked again: tkt view = %q, want waiting", got)
	}
	want := []string{"waiting", "processing", "waiting"}
	w := agentWrites(cr)
	if len(w) != len(want) {
		t.Fatalf("writes = %v, want %v", w, want)
	}
	for i, call := range w {
		if call[3] != want[i] {
			t.Fatalf("write %d = %v, want %s", i, call, want[i])
		}
	}
}

// A waiting the board wrote that someone has since rewritten (a fresh stamp)
// is theirs: closing the pane leaves it, and the board forgets its own.
func TestReconcileRewrittenWaitingIsNotCleared(t *testing.T) {
	m, cr, src := reconBoard(t, true, "processing", nil)
	src.byKey = repoPanes(boardRepo, "TKT-1", "blocked")
	m, _ = reconPoll(t, m)
	cr.viewAgentAt["TKT-1"] = "2026-10-01T09:00:00Z" // a skill wrote waiting itself
	syncList(cr)
	m = loadBoard(m)
	src.byKey = map[string]herdr.Live{}
	elapse(reconcileGoneFor)
	for range 3 {
		m, _ = reconPoll(t, m)
	}
	if got := viewStatus(t, m, "TKT-1"); got != "waiting" {
		t.Fatalf("tkt view = %q, want the skill's waiting kept", got)
	}
	if n := len(agentWrites(cr)); n != 1 {
		t.Fatalf("writes = %d, want only the board's waiting", n)
	}
	if k := m.recon.keys["TKT-1"]; k.wroteWaitingAt != "" {
		t.Fatalf("board still claims the waiting: %q", k.wroteWaitingAt)
	}
}

// done is never written, whatever herdr reports.
func TestReconcileNeverWritesDone(t *testing.T) {
	m, cr, src := reconBoard(t, true, "processing", nil)
	for _, st := range []string{"done", "idle", "unknown", "blocked", "working", "done"} {
		src.byKey = repoPanes(boardRepo, "TKT-1", st)
		m, _ = reconPoll(t, m)
		syncList(cr)
		m = loadBoard(m)
	}
	src.byKey = map[string]herdr.Live{}
	elapse(reconcileGoneFor)
	m, _ = reconPoll(t, m)
	_, _ = reconPoll(t, m)
	for _, call := range agentWrites(cr) {
		if call[3] == "done" {
			t.Fatalf("board wrote done: %v", call)
		}
	}
}

func TestPlanReconcile(t *testing.T) {
	polledAt := reconNow.Add(reconcileGoneFor)
	gone := func(k reconKey) *reconKey {
		k.absentPolls, k.lastPresent = reconcileGonePolls, reconNow
		return &k
	}
	here := func(st herdr.Status, k reconKey) *reconKey {
		k.present, k.status = true, st
		return &k
	}
	repo := []string{boardRepo}
	cases := []struct {
		name  string
		k     *reconKey
		front string
		want  string // "" = nothing planned
	}{
		{"gone processing", gone(reconKey{repos: repo}), "processing", "idle"},
		{"gone once", &reconKey{absentPolls: 1, repos: repo, lastPresent: reconNow}, "processing", ""},
		{"gone too soon", &reconKey{absentPolls: 5, repos: repo, lastPresent: polledAt.Add(-reconcileGoneFor + time.Millisecond)}, "processing", ""},
		{"gone never stamped", &reconKey{absentPolls: 5, repos: repo}, "processing", ""},
		{"gone already tried", gone(reconKey{repos: repo, idleTried: true}), "processing", ""},
		{"gone board waiting", gone(reconKey{repos: repo, wroteWaitingAt: "t"}), "waiting", "idle"},
		{"gone skill waiting", gone(reconKey{repos: repo}), "waiting", ""},
		{"gone idle", gone(reconKey{repos: repo}), "idle", ""},
		{"gone done", gone(reconKey{repos: repo}), "done", ""},
		{"gone no card", gone(reconKey{repos: repo}), "", ""},
		{"blocked processing", here(herdr.StatusBlocked, reconKey{repos: repo}), "processing", "waiting"},
		{"blocked tried", here(herdr.StatusBlocked, reconKey{repos: repo, waitTried: true}), "processing", ""},
		{"blocked waiting", here(herdr.StatusBlocked, reconKey{repos: repo}), "waiting", ""},
		{"working board waiting", here(herdr.StatusWorking, reconKey{repos: repo, wroteWaitingAt: "t"}), "waiting", "processing"},
		{"working skill waiting", here(herdr.StatusWorking, reconKey{repos: repo}), "waiting", ""},
		{"working restore tried", here(herdr.StatusWorking, reconKey{repos: repo, wroteWaitingAt: "t", restoreTried: true}), "waiting", ""},
		{"working processing", here(herdr.StatusWorking, reconKey{repos: repo}), "processing", ""},
		{"idle pane processing", here(herdr.StatusIdle, reconKey{repos: repo}), "processing", ""},
		{"done pane processing", here(herdr.StatusDone, reconKey{repos: repo}), "processing", ""},
		{"repo case differs", gone(reconKey{repos: []string{"OldDogNewFlex/TKTBan"}}), "processing", "idle"},
		{"one pane elsewhere", gone(reconKey{repos: []string{boardRepo, "other/repo"}}), "processing", ""},
		{"one pane unknown", gone(reconKey{repos: []string{boardRepo, ""}}), "processing", ""},
		{"no panes recorded", gone(reconKey{}), "processing", ""},
	}
	for _, c := range cases {
		items := planReconcile(map[string]*reconKey{"TKT-1": c.k}, map[string]string{"TKT-1": c.front}, boardRepo, polledAt)
		got := ""
		if len(items) == 1 {
			got = items[0].want
		} else if len(items) > 1 {
			t.Fatalf("%s: %d items", c.name, len(items))
		}
		if got != c.want {
			t.Errorf("%s: planned %q, want %q", c.name, got, c.want)
		}
	}
	if items := planReconcile(map[string]*reconKey{"TKT-1": gone(reconKey{repos: repo})}, map[string]string{"TKT-1": "processing"}, "", polledAt); len(items) != 0 {
		t.Errorf("planned with no board repo: %v", items)
	}
}

// A live agent pane whose branch stops naming the ticket (a detached HEAD
// mid-rebase, a checkout, a cd out of the worktree) is not a closed pane, for
// however many polls; once the pane really closes, the idle is written.
func TestReconcileLivePaneOffItsKeyIsNotGone(t *testing.T) {
	m, cr, src := reconBoard(t, true, "processing", nil)
	pane := "w1:TKT-1"
	src.byKey = map[string]herdr.Live{}
	src.agentPanes = map[string]bool{pane: true} // alive, on no key
	elapse(reconcileGoneFor)
	for range 6 {
		m, _ = reconPoll(t, m)
	}
	// Then the same pane turns up under another ticket's branch.
	src.byKey = map[string]herdr.Live{"TKT-9": {Status: herdr.StatusWorking, Panes: []herdr.PaneRef{{PaneID: pane, Repo: boardRepo}}}}
	src.agentPanes = nil
	for range 3 {
		m, _ = reconPoll(t, m)
	}
	if w := agentWrites(cr); len(w) != 0 {
		t.Fatalf("wrote while the pane was alive: %v", w)
	}
	if k := m.recon.keys["TKT-1"]; k.absentPolls != 0 || k.idleTried {
		t.Fatalf("TKT-1 %+v: a live pane advanced towards gone", *k)
	}
	src.byKey = map[string]herdr.Live{} // the pane closes
	elapse(reconcileGoneFor)
	m, _ = reconPoll(t, m)
	m, _ = reconPoll(t, m)
	w := agentWrites(cr)
	if len(w) != 1 || !eq(w[0], "edit", "TKT-1", "--agent-status", "idle", "--json") {
		t.Fatalf("writes = %v, want one idle for TKT-1 once the pane closed", w)
	}
	if got := viewStatus(t, m, "TKT-1"); got != "idle" {
		t.Fatalf("tkt view = %q, want idle", got)
	}
}

// observe matches herdr's keys to stored ones whatever their case: a pane on
// a lower-case key is present, not absent.
func TestReconcileObserveIgnoresKeyCase(t *testing.T) {
	var r reconcileState
	byKey := map[string]herdr.Live{"tkt-1": {Status: herdr.StatusWorking, Panes: []herdr.PaneRef{{PaneID: "p1"}}}}
	for range 3 {
		r.observe(byKey, nil, reconNow)
	}
	k := r.keys["TKT-1"]
	if k == nil || !k.present || k.absentPolls != 0 {
		t.Fatalf("TKT-1 = %+v, want present with no absent polls", k)
	}
}

// An idle lands only over a processing stamped before the pane was last seen:
// one written after it (another machine, an agent outside herdr) is someone
// else's, and a stamp the board cannot read proves nothing.
func TestReconcileIdleOnlyOverOlderProcessing(t *testing.T) {
	cases := []struct {
		at    string
		write bool
	}{
		{"2026-09-30T00:00:00Z", true},
		{"2026-10-01T11:59:59Z", true},
		{"2026-10-01T12:00:00Z", false}, // the pane's own second: may be later
		{"2026-10-01T12:05:00Z", false}, // written after the pane closed
		{"2026-10-01T18:00:00+02:00", false},
		{"yesterday", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.at, func(t *testing.T) {
			m, cr, src := reconBoard(t, true, "processing", nil)
			cr.viewAgentAt["TKT-1"] = c.at
			src.byKey = map[string]herdr.Live{}
			elapse(reconcileGoneFor)
			for range 3 {
				m, _ = reconPoll(t, m)
			}
			want := "processing"
			if c.write {
				want = "idle"
			}
			if got := viewStatus(t, m, "TKT-1"); got != want {
				t.Fatalf("agent_status_at %q: tkt view = %q, want %q", c.at, got, want)
			}
			if n := len(agentWrites(cr)); (n == 1) != c.write || n > 1 {
				t.Fatalf("agent_status_at %q: %d writes", c.at, n)
			}
		})
	}
}

// A `tkt agents` that fails outright (not a missing verb) leaves no way to
// tell a headless run is not on the ticket: the idle is skipped, with a
// warning.
func TestReconcileAgentsUnreadableSkipsIdle(t *testing.T) {
	m, cr, src := reconBoard(t, true, "processing", func(c *captureRunner, _ *fakeLive) {
		c.failVerbs = map[string]bool{"agents": true}
	})
	src.byKey = map[string]herdr.Live{}
	elapse(reconcileGoneFor)
	for range 3 {
		m, _ = reconPoll(t, m)
	}
	if w := agentWrites(cr); len(w) != 0 {
		t.Fatalf("wrote with tkt agents unreadable: %v", w)
	}
	if len(cr.callsTo("agents")) == 0 {
		t.Fatal("setup: tkt agents was never asked")
	}
	if m.statusKind != "warn" || !strings.Contains(m.status, "tkt agents unreadable") {
		t.Fatalf("status = %q (%s), want an agents warning", m.status, m.statusKind)
	}
	if got := viewStatus(t, m, "TKT-1"); got != "processing" {
		t.Fatalf("tkt view = %q, want processing", got)
	}
}

// An outage long enough to fall back writes nothing, even though the pane
// closed during it; once polls answer again, the close is seen on its own
// good polls and the idle lands after the usual debounce.
func TestReconcileRecoversAfterFallback(t *testing.T) {
	m, cr, src := reconBoard(t, true, "processing", nil)
	src.pollErr = errors.New("socket gone")
	for range liveMaxFails + 2 {
		m, _ = reconPoll(t, m)
	}
	if m.live.on {
		t.Fatal("setup: board did not fall back")
	}
	src.byKey = map[string]herdr.Live{} // closed while herdr was unreachable
	elapse(reconcileGoneFor)
	for range 2 {
		m, _ = reconPoll(t, m)
	}
	if w := agentWrites(cr); len(w) != 0 {
		t.Fatalf("wrote during the outage: %v", w)
	}
	src.pollErr = nil
	m, _ = reconPoll(t, m)
	if !m.live.on {
		t.Fatal("setup: board did not recover")
	}
	if w := agentWrites(cr); len(w) != 0 {
		t.Fatalf("wrote after one good poll: %v", w)
	}
	m, _ = reconPoll(t, m)
	w := agentWrites(cr)
	if len(w) != 1 || !eq(w[0], "edit", "TKT-1", "--agent-status", "idle", "--json") {
		t.Fatalf("writes = %v, want one idle after recovery", w)
	}
	if got := viewStatus(t, m, "TKT-1"); got != "idle" {
		t.Fatalf("tkt view = %q, want idle", got)
	}
}

// A key worked by two panes keeps the one that drifts off its branch: when
// the other closes, the drifted pane still works the ticket, so nothing is
// written until it closes too.
func TestReconcileDriftedPaneHoldsKeyAfterOtherCloses(t *testing.T) {
	both := map[string]herdr.Live{"TKT-1": {Status: herdr.StatusWorking, Panes: []herdr.PaneRef{
		{PaneID: "p", Dir: "/w/p", Repo: boardRepo},
		{PaneID: "q", Dir: "/w/q", Repo: boardRepo},
	}}}
	onlyP := map[string]herdr.Live{"TKT-1": {Status: herdr.StatusWorking, Panes: []herdr.PaneRef{
		{PaneID: "p", Dir: "/w/p", Repo: boardRepo},
	}}}
	m, cr, src := reconBoard(t, true, "processing", func(_ *captureRunner, s *fakeLive) { s.byKey = both })
	src.byKey, src.agentPanes = onlyP, map[string]bool{"p": true, "q": true} // q detaches, still alive
	for range 3 {
		m, _ = reconPoll(t, m)
	}
	src.byKey, src.agentPanes = map[string]herdr.Live{}, map[string]bool{"q": true} // p closes
	elapse(reconcileGoneFor)
	for range 6 {
		m, _ = reconPoll(t, m)
	}
	if w := agentWrites(cr); len(w) != 0 {
		t.Fatalf("wrote while the drifted pane was alive: %v", w)
	}
	if k := m.recon.keys["TKT-1"]; k.absentPolls != 0 {
		t.Fatalf("TKT-1 %+v: a live drifted pane advanced towards gone", *k)
	}
	src.agentPanes = nil // q closes too
	m, _ = reconPoll(t, m)
	m, _ = reconPoll(t, m)
	w := agentWrites(cr)
	if len(w) != 1 || !eq(w[0], "edit", "TKT-1", "--agent-status", "idle", "--json") {
		t.Fatalf("writes = %v, want one idle once both panes closed", w)
	}
}

// A poll with no pane set (nil AgentPanes) cannot say whether a pane closed:
// it moves no key towards gone. An empty set, by contrast, means none is
// alive.
func TestReconcileObserveNilAgentPanesIsUnknown(t *testing.T) {
	var r reconcileState
	r.observe(map[string]herdr.Live{"TKT-1": {Status: herdr.StatusWorking, Panes: []herdr.PaneRef{{PaneID: "p1"}}}}, map[string]bool{"p1": true}, reconNow)
	for range 5 {
		r.observe(map[string]herdr.Live{}, nil, reconNow.Add(reconcileGoneFor))
	}
	k := r.keys["TKT-1"]
	if k.present || k.absentPolls != 0 {
		t.Fatalf("TKT-1 = %+v, want absent with no absent polls counted", *k)
	}
	r.observe(map[string]herdr.Live{}, map[string]bool{}, reconNow.Add(reconcileGoneFor))
	if k.absentPolls != 1 {
		t.Fatalf("absentPolls = %d after an empty pane set, want 1", k.absentPolls)
	}
}

// lastPresent is when the poll started, not when its answer landed: a
// processing stamped while a slow poll ran must not count as older than it.
func TestReconcileLastPresentIsPollStart(t *testing.T) {
	m, _, src := reconBoard(t, true, "processing", nil)
	start := pinned.Add(time.Minute)
	pinned = start
	src.during = func() { elapse(900 * time.Millisecond) }
	m, _ = reconPoll(t, m)
	if k := m.recon.keys["TKT-1"]; !k.lastPresent.Equal(start) {
		t.Fatalf("lastPresent = %v, want the poll's start %v", k.lastPresent, start)
	}
	if !m.live.polledAt.Equal(start) {
		t.Fatalf("polledAt = %v, want %v", m.live.polledAt, start)
	}
}

// A processing the board restored in the same second as the pane's last
// sighting is the board's own, so the close still clears it to idle rather
// than leaving it stuck behind stampedBefore.
func TestReconcileBoardProcessingClearedOnClose(t *testing.T) {
	m, cr, src := reconBoard(t, true, "processing", func(c *captureRunner, _ *fakeLive) { c.stampNow = true })
	src.byKey = repoPanes(boardRepo, "TKT-1", "blocked")
	m, _ = reconPoll(t, m)
	syncList(cr)
	m = loadBoard(m)
	elapse(time.Second)
	src.byKey = repoPanes(boardRepo, "TKT-1", "working")
	m, _ = reconPoll(t, m) // restores processing, stamped in this poll's second
	if got := viewStatus(t, m, "TKT-1"); got != "processing" {
		t.Fatalf("working again: tkt view = %q, want processing", got)
	}
	if k := m.recon.keys["TKT-1"]; k.wroteProcessingAt == "" || stampedBefore(k.wroteProcessingAt, k.lastPresent) {
		t.Fatalf("setup: restore stamp %q should share lastPresent's second %v", k.wroteProcessingAt, k.lastPresent)
	}
	syncList(cr)
	m = loadBoard(m)
	src.byKey = map[string]herdr.Live{}
	elapse(reconcileGoneFor)
	m, _ = reconPoll(t, m)
	m, _ = reconPoll(t, m)
	if got := viewStatus(t, m, "TKT-1"); got != "idle" {
		t.Fatalf("closed pane: tkt view = %q, want idle", got)
	}
	if n := len(agentWrites(cr)); n != 3 {
		t.Fatalf("writes = %d, want waiting, processing, idle", n)
	}
}
