package ui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/model"
	"github.com/olddognewflex/tktban/internal/tkt"
)

// fakeDispatch is a live source that can also prepare a dispatch. It carries
// every herdr call a full dispatch will ever make, not just the one the
// Dispatcher interface asks for today, and every one of them but worktree.list
// fails the test on sight.
//
// That is how "the dry run creates nothing" is asserted as an observable
// fact rather than by reading the code: whatever the board does with this
// source, the only thing it is allowed to have done is list.
type fakeDispatch struct {
	fakeLive
	t *testing.T

	listed  []string // one entry per worktree.list, with the cwd asked about
	list    herdr.WorktreeListResult
	listErr error
}

func (f *fakeDispatch) WorktreeList(_ context.Context, cwd string) (herdr.WorktreeListResult, error) {
	f.listed = append(f.listed, cwd)
	return f.list, f.listErr
}

func (f *fakeDispatch) WorktreeCreate(context.Context, herdr.WorktreeCreateParams) (herdr.WorktreeResult, error) {
	f.t.Helper()
	f.t.Error("the dry run created a worktree")
	return herdr.WorktreeResult{}, nil
}

func (f *fakeDispatch) WorktreeOpen(context.Context, herdr.WorktreeOpenParams) (herdr.WorktreeResult, error) {
	f.t.Helper()
	f.t.Error("the dry run opened a worktree")
	return herdr.WorktreeResult{}, nil
}

func (f *fakeDispatch) AgentStart(context.Context, herdr.AgentStartParams) (herdr.AgentStartResult, error) {
	f.t.Helper()
	f.t.Error("the dry run started an agent")
	return herdr.AgentStartResult{}, nil
}

func (f *fakeDispatch) AgentPrompt(context.Context, herdr.AgentPromptParams) error {
	f.t.Helper()
	f.t.Error("the dry run prompted an agent")
	return nil
}

func (f *fakeDispatch) FocusPane(context.Context, string) error {
	f.t.Helper()
	f.t.Error("the dry run focused a pane")
	return nil
}

// okList is a repo with nothing dispatched in it yet.
func okList() herdr.WorktreeListResult {
	return herdr.WorktreeListResult{
		Source: herdr.WorktreeSource{
			RepoKey:            "github.com/olddognewflex/tktban",
			RepoName:           "tktban",
			RepoRoot:           "/src/tktban",
			SourceCheckoutPath: "/src/tktban",
		},
		Worktrees: []herdr.WorktreeInfo{{Path: "/src/tktban", Branch: "main"}},
	}
}

const testDispatchDir = "/src/tktban"

// dispatchBoard is a loaded, live board with the D key turned on.
func dispatchBoard(t *testing.T) (Model, *fakeDispatch, *captureRunner) {
	t.Helper()
	src := &fakeDispatch{t: t, list: okList()}
	m, cr := testModel(t)
	m = m.WithLive(src).WithDispatch(DispatchOpts{Dir: testDispatchDir})
	m.settings["dispatch"] = true
	m = loadBoard(m)
	return goLive(t, m), src, cr
}

// pressD runs the whole D flow synchronously: the keypress, then the
// preparation command it returned, then the resulting message.
//
// Whether a preparation started is read off m.dispatching rather than by
// running the returned command: a refusal's command is the status-expiry
// timer, and calling that would sit on a real six-second clock.
func pressD(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := update(m, key("D"))
	if !m.dispatching {
		return m // a guard refused before any work started
	}
	if cmd == nil {
		t.Fatal("a started dispatch returned no preparation command")
	}
	msg := cmd()
	if _, ok := msg.(dispatchPrepMsg); !ok {
		t.Fatalf("D produced %T, want dispatchPrepMsg", msg)
	}
	m, _ = update(m, msg)
	return m
}

// dispatchReadVerbs are the only tkt invocations anything on the D path may
// make: the board's own refresh and the dispatch's two config reads.
//
// An allowlist, not a denylist. A denylist of write verbs would quietly stop
// covering the moment the create sequence reaches for a verb nobody thought
// to list, which is exactly when it matters.
//
// lane-time is not on it. It is a read only when it is passed --read-only;
// without the flag it records a worklog, so it is checked by isRead below
// rather than blessed by name.
var dispatchReadVerbs = map[string]bool{
	"cfg":  true, // board.roles, priorities, vcs, board.ownership
	"list": true,
	"view": true,
}

// isRead reports whether one recorded tkt invocation changed nothing.
func isRead(call []string) bool {
	if len(call) == 0 {
		return true
	}
	if call[0] == "lane-time" {
		// The flag is the whole difference between reading a lane's time and
		// writing a worklog entry for it, so require it rather than trust the
		// verb.
		return slices.Contains(call, "--read-only")
	}
	return dispatchReadVerbs[call[0]]
}

// nonReadCall returns the first tkt invocation that changed something, or
// nil. A dry run must not transition a ticket, comment on one or edit one.
func nonReadCall(cr *captureRunner) []string {
	for _, call := range cr.calls {
		if !isRead(call) {
			return call
		}
	}
	return nil
}

// The allowlist has to be wrong about the two cases that matter, or it is not
// checking anything: a write verb, and a lane-time that is not read-only.
func TestNonReadCallCatchesWritesAndAWritingLaneTime(t *testing.T) {
	reads := [][]string{
		{"cfg", "board.roles", "--json"},
		{"list", "--query", "all", "--json"},
		{"lane-time", "--keys", "TKT-1:todo", "--read-only", "--json"},
		{"view", "TKT-1", "--json"},
	}
	writes := [][]string{
		{"transition", "TKT-1", "done"},
		{"comment", "TKT-1", "hi"},
		{"edit", "TKT-1", "--summary", "x"},
		{"create", "--type", "Story"},
		{"apply", "TKT-1", "--file", "/tmp/x"},
		// The one the verb name alone would have blessed.
		{"lane-time", "--keys", "TKT-1:todo", "--json"},
	}
	if call := nonReadCall(&captureRunner{calls: reads}); call != nil {
		t.Errorf("a read was reported as a write: %v", call)
	}
	for _, w := range writes {
		cr := &captureRunner{calls: append(append([][]string(nil), reads...), w)}
		if call := nonReadCall(cr); call == nil {
			t.Errorf("tkt %v was not caught", w)
		}
	}
}

// Every rung of the ladder says which one it was and opens nothing. A key
// that silently does nothing is the failure mode this is guarding against.
func TestDispatchGuardLadder(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, m Model, src *fakeDispatch) Model
		want  string
		// wantf is for a refusal that names the file the board is reading,
		// which is a temp directory here. Which file it names is pinned by
		// TestDispatchOffRefusalNamesTheFileTheBoardReads; this table is
		// pinning that the rung refuses at all.
		wantf func(m Model) string
	}{
		{"the setting is off", func(_ *testing.T, m Model, _ *fakeDispatch) Model {
			m.settings["dispatch"] = false
			return m
		}, "", func(m Model) string {
			return "Dispatch is off — set dispatch = true in " + m.settingsFile()
		}},

		{"no live source at all", func(t *testing.T, _ Model, _ *fakeDispatch) Model {
			m, _ := testModel(t)
			m = m.WithDispatch(DispatchOpts{Dir: testDispatchDir})
			m.settings["dispatch"] = true
			return loadBoard(m)
		}, "Live agent status is off", nil},

		{"a source that only reads status", func(t *testing.T, _ Model, _ *fakeDispatch) Model {
			m, _ := testModel(t)
			m = m.WithLive(&fakeLive{}).WithDispatch(DispatchOpts{Dir: testDispatchDir})
			m.settings["dispatch"] = true
			return goLive(t, loadBoard(m))
		}, "This board can't dispatch herdr agents", nil},

		{"herdr has gone quiet", func(t *testing.T, m Model, src *fakeDispatch) Model {
			src.pollErr = errPoll
			for range liveMaxFails {
				m = poll(t, m)
			}
			if m.live.on {
				t.Fatal("setup: live should be off after the failure threshold")
			}
			return m
		}, "herdr live status unavailable", nil},

		{"nothing selected", func(_ *testing.T, m Model, _ *fakeDispatch) Model {
			for i := range m.columns {
				m.columns[i].Cards = nil
			}
			return m
		}, "Select a card first", nil},

		{"the card has no key", func(_ *testing.T, m Model, _ *fakeDispatch) Model {
			m.columns[0].Cards[0].Key = ""
			return m
		}, "That card has no ticket key", nil},

		{"the ticket already has an agent", func(t *testing.T, m Model, src *fakeDispatch) Model {
			src.byKey = map[string]herdr.Live{"TKT-1": {
				Status: herdr.StatusWorking,
				Panes:  []herdr.PaneRef{{PaneID: "wC:p1", Status: herdr.StatusWorking}},
			}}
			return poll(t, m)
		}, "TKT-1 already has an agent pane (o focuses it)", nil},

		{"no directory resolved", func(_ *testing.T, m Model, _ *fakeDispatch) Model {
			return m.WithDispatch(DispatchOpts{})
		}, "Don't know which repo to dispatch TKT-1 in — open the board from a pane in the repo", nil},

		// A different refusal from the one above, and from the setting being
		// off: the person turned it off on the command line, and sending them
		// to look at their config instead would waste their time.
		{"turned off on the command line", func(_ *testing.T, m Model, _ *fakeDispatch) Model {
			return m.WithDispatch(DispatchOpts{Dir: testDispatchDir, OptedOut: true})
		}, "Dispatch is off for this board (--no-herdr-dispatch)", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, src, cr := dispatchBoard(t)
			m = c.setup(t, m, src)
			want := c.want
			if c.wantf != nil {
				want = c.wantf(m)
			}
			m, _ = update(m, key("D"))
			if m.dispatching {
				t.Fatal("a refused dispatch still started a preparation")
			}
			if m.status != want || m.statusKind != "warn" {
				t.Errorf("status = %q (%s), want %q (warn)", m.status, m.statusKind, want)
			}
			if m.modal != nil {
				t.Errorf("a refused dispatch opened a %T", m.modal)
			}
			if len(src.listed) != 0 {
				t.Errorf("a refused dispatch called worktree.list %v", src.listed)
			}
			if call := nonReadCall(cr); call != nil {
				t.Errorf("a refused dispatch ran tkt %v, which is not a read", call)
			}
		})
	}
}

// dispatchOffStatus is the status a fresh board gives when D is pressed with
// the setting off. It needs no live source: the opt-in is the first rung.
func dispatchOffStatus(t *testing.T, settingsPath string) string {
	t.Helper()
	cr := &captureRunner{}
	m := New(tkt.New("", "tkt").WithRunner(cr.run), 10, true, settingsPath)
	m.width, m.height = 120, 30
	m = m.WithDispatch(DispatchOpts{Dir: testDispatchDir})
	m, _ = update(m, key("D"))
	return m.status
}

// The bug this test exists for, reported against the dry run: dispatch = true
// was set in ~/.config/tktban/settings.toml while the board — launched by
// herdr's popup — reads the plugin file in herdr's state dir. The binary was
// current; only the file was wrong. The refusal named neither file, so there
// was nothing to act on.
//
// The refusal must name the file THIS board reads, which is one of two.
func TestDispatchOffRefusalNamesTheFileTheBoardReads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if h, err := os.UserHomeDir(); err != nil || h != home {
		t.Skip("the home directory is not settable on this platform")
	}

	plugin := filepath.Join(home, ".local", "state", "herdr", "plugins", "odnf.tktban", "settings.toml")
	standalone := filepath.Join(home, ".config", "tktban", "settings.toml")
	tilde := func(rel ...string) string {
		return "~" + string(os.PathSeparator) + filepath.Join(rel...)
	}
	cases := []struct {
		name  string
		path  string
		want  string
		other string
	}{
		{
			name:  "herdr's own board names the plugin file",
			path:  plugin,
			want:  tilde(".local", "state", "herdr", "plugins", "odnf.tktban", "settings.toml"),
			other: tilde(".config", "tktban", "settings.toml"),
		},
		{
			name:  "a standalone board names the standalone file",
			path:  standalone,
			want:  tilde(".config", "tktban", "settings.toml"),
			other: tilde(".local", "state", "herdr", "plugins", "odnf.tktban", "settings.toml"),
		},
	}
	for _, c := range cases {
		got := dispatchOffStatus(t, c.path)
		if !strings.Contains(got, "set dispatch = true in ") {
			t.Errorf("%s: status = %q, want it to say what to set", c.name, got)
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: status = %q, want it to name %s", c.name, got, c.want)
		}
		// Naming the wrong one is the whole bug, so it is asserted against.
		if strings.Contains(got, c.other) {
			t.Errorf("%s: status = %q names the file this board does NOT read", c.name, got)
		}
	}

	// An empty settings path is the standalone default, which New resolves,
	// so the refusal still names a real file rather than nothing.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if got := dispatchOffStatus(t, ""); !strings.Contains(got, tilde(".config", "tktban", "settings.toml")) {
		t.Errorf("an unset settings path gave %q, want the standalone default", got)
	}
}

// A refusal that asks for a configuration change names the config file, and
// an explicitly configured one is named as given rather than by convention.
func TestDispatchConfigRefusalNamesTheConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if h, err := os.UserHomeDir(); err != nil || h != home {
		t.Skip("the home directory is not settable on this platform")
	}
	explicit := filepath.Join(home, "src", "other", ".sdlc", "config.toml")

	src := &fakeDispatch{t: t, list: okList()}
	cr := &captureRunner{ownership: `{"todo->done":"human"}`}
	m := New(tkt.New(explicit, "tkt").WithRunner(cr.run), 10, true,
		filepath.Join(t.TempDir(), "settings.toml"))
	m.width, m.height = 120, 30
	m = m.WithLive(src).WithDispatch(DispatchOpts{Dir: testDispatchDir})
	m.settings["dispatch"] = true
	m = goLive(t, loadBoard(m))

	m = pressD(t, m)
	want := "~" + string(os.PathSeparator) + filepath.Join("src", "other", ".sdlc", "config.toml")
	if !strings.Contains(m.status, want) {
		t.Fatalf("status = %q, want it to name %s", m.status, want)
	}
	if strings.Contains(m.status, "the tkt config") {
		t.Errorf("status = %q still describes the config instead of naming it", m.status)
	}
}

// A second D while the first is still being prepared must not start another.
func TestDispatchIgnoresASecondPressWhileInFlight(t *testing.T) {
	m, src, _ := dispatchBoard(t)
	m, first := update(m, key("D"))
	if first == nil {
		t.Fatal("D did not start a preparation")
	}
	m, _ = update(m, key("D"))
	if m.status != "Already preparing a dispatch" || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
	// Once the first lands, the key works again.
	m, _ = update(m, first())
	if m.modal == nil {
		t.Fatal("the first preparation did not open the confirm modal")
	}
	if m.dispatching {
		t.Fatal("the board still thinks a dispatch is in flight")
	}
	if len(src.listed) != 1 {
		t.Fatalf("worktree.list calls = %v, want exactly one", src.listed)
	}
}

// The config-level refusals. Each is a reason the board could never find the
// agent again, or never had one to make, so each refuses before herdr is
// touched at all.
func TestDispatchConfigRefusals(t *testing.T) {
	cases := []struct {
		name      string
		vcs       string
		ownership string
		want      string
	}{
		{
			name: "no vcs config",
			vcs:  failReply,
			want: "No [vcs] branch_fmt in .sdlc/config.toml, so there is no branch to cut",
		},
		{
			name: "branch_fmt without the key",
			vcs:  `{"default_branch":"main","branch_fmt":"feature/{slug}"}`,
			want: `branch_fmt "feature/{slug}" in .sdlc/config.toml doesn't name TKT-1: the board could never badge or jump to its agent`,
		},
		{
			name:      "no agent-owned transition out of the lane",
			ownership: `{"todo->done":"human"}`,
			want:      "No agent-owned transition out of To Do ([board] ownership in .sdlc/config.toml)",
		},
		{
			name:      "no ownership config at all",
			ownership: failReply,
			want:      "No agent-owned transition out of To Do ([board] ownership in .sdlc/config.toml)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, src, cr := dispatchBoard(t)
			cr.vcs, cr.ownership = c.vcs, c.ownership
			m = pressD(t, m)
			if m.status != c.want || m.statusKind != "warn" {
				t.Errorf("status = %q (%s), want %q (warn)", m.status, m.statusKind, c.want)
			}
			if m.modal != nil {
				t.Errorf("a refused dispatch opened a %T", m.modal)
			}
			if m.dispatching {
				t.Error("the latch stayed set, so D would refuse for the rest of the session")
			}
			if len(src.listed) != 0 {
				t.Errorf("a config refusal still called herdr: %v", src.listed)
			}
		})
	}
}

// withPrepContext swaps the preparation's budget for the test's own.
func withPrepContext(t *testing.T, mk func() (context.Context, context.CancelFunc)) {
	t.Helper()
	orig := dispatchPrepContext
	dispatchPrepContext = mk
	t.Cleanup(func() { dispatchPrepContext = orig })
}

// The budget has to bound the tkt config reads, not just the herdr call:
// they are subprocesses, and startDispatch latches m.dispatching until a
// result comes back, so an unbounded read would leave D refusing for the
// rest of the session.
func TestDispatchBudgetReachesTheTktReads(t *testing.T) {
	m, _, cr := dispatchBoard(t)
	cr.observeCtx = true
	m = pressD(t, m)
	if _, ok := m.modal.(dispatchModal); !ok {
		t.Fatalf("modal = %T (status %q)", m.modal, m.status)
	}
	if len(cr.ctxCalls) != 2 {
		t.Fatalf("observed %d tkt reads, want the dispatch's two config reads: %+v", len(cr.ctxCalls), cr.ctxCalls)
	}
	for _, call := range cr.ctxCalls {
		if !call.deadline {
			t.Errorf("tkt %v ran with no deadline: the budget never reached the subprocess", call.args)
		}
	}
}

// And when the budget runs out during those reads, the board says so — both
// config readers answer their zero value for any failure, so without this a
// timeout would come out as "no branch_fmt configured" — and the latch
// clears, so D works again.
func TestDispatchTimesOutReadingTheConfig(t *testing.T) {
	withPrepContext(t, func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // the budget is already gone when the command starts
		return ctx, cancel
	})
	m, src, _ := dispatchBoard(t)
	m = pressD(t, m)

	want := "Timed out reading the tkt config for TKT-1"
	if m.status != want || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s), want %q (warn)", m.status, m.statusKind, want)
	}
	if m.modal != nil {
		t.Errorf("a timed-out preparation opened a %T", m.modal)
	}
	if m.dispatching {
		t.Error("the latch stayed set after a timeout, so D would refuse forever")
	}
	if len(src.listed) != 0 {
		t.Errorf("a timed-out preparation still called herdr: %v", src.listed)
	}

	// The latch really is clear: a second D with the budget restored works.
	withPrepContext(t, func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), dispatchPrepTimeout)
	})
	m = pressD(t, m)
	if _, ok := m.modal.(dispatchModal); !ok {
		t.Fatalf("after a timeout the next D gave %T (status %q)", m.modal, m.status)
	}
}

// The preparation must not carry its context out of the invocation that made
// it. Bubble Tea runs a command once, so nothing calls one twice today — but
// a command that bound its bounded Tkt back onto the captured parameter would
// hand the next call an already-cancelled context and time out every read
// before making it, which is exactly the shape a retry budget has.
func TestDispatchPrepCommandIsReusable(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	cr.observeCtx = true
	m, cmd := update(m, key("D"))
	if cmd == nil {
		t.Fatal("D did not start a preparation")
	}

	for i := range 3 {
		msg, ok := cmd().(dispatchPrepMsg)
		if !ok {
			t.Fatalf("call %d produced %T", i, msg)
		}
		if msg.err != nil {
			t.Fatalf("call %d failed: %v — the command kept the previous call's context", i, msg.err)
		}
		if msg.plan.Key != "TKT-1" {
			t.Fatalf("call %d planned %s", i, msg.plan.Key)
		}
	}
	if len(src.listed) != 3 {
		t.Fatalf("worktree.list calls = %v, want one per invocation", src.listed)
	}
	// The invariant underneath: every read runs under a context that is both
	// bounded and still live. A context that outlived the invocation that
	// made it would show up here as a read against an already-ended one.
	if len(cr.ctxCalls) != 6 {
		t.Fatalf("observed %d tkt reads, want two per invocation: %+v", len(cr.ctxCalls), cr.ctxCalls)
	}
	for i, call := range cr.ctxCalls {
		if !call.deadline {
			t.Errorf("read %d (tkt %v) ran with no deadline", i, call.args)
		}
		if !call.live {
			t.Errorf("read %d (tkt %v) ran under an already-ended context", i, call.args)
		}
	}
}

// The two herdr refusals a dispatch has to understand, plus one it does not,
// which must be reported rather than mistaken for either.
func TestDispatchPreflightRefusals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"not a git work tree",
			&herdr.APIError{Code: herdr.CodeNotGitWorktree, Message: "not a git work tree"},
			"Not a git work tree: " + testDispatchDir},
		{"the board is open in a worktree",
			&herdr.APIError{Code: herdr.CodeLinkedWorktreeSource, Message: "source is a linked worktree"},
			"This board is open in a worktree, not the main checkout, so there is nothing to branch from"},
		{"anything else is reported as itself",
			&herdr.APIError{Code: herdr.CodeWorktreeOperationInProgress, Message: "busy"},
			"Couldn't prepare a dispatch: herdr: worktree_operation_in_progress: busy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, src, _ := dispatchBoard(t)
			src.listErr = c.err
			m = pressD(t, m)
			if m.status != c.want || m.statusKind != "warn" {
				t.Errorf("status = %q (%s), want %q (warn)", m.status, m.statusKind, c.want)
			}
			if m.modal != nil {
				t.Errorf("a refused preflight opened a %T", m.modal)
			}
			if m.dispatching {
				t.Error("the latch stayed set, so D would refuse for the rest of the session")
			}
			if len(src.listed) != 1 {
				t.Errorf("worktree.list calls = %v, want exactly one", src.listed)
			}
		})
	}
}

// herdr says the source checkout is itself a linked worktree in the listing,
// without erroring, and that is a refusal too.
func TestDispatchRefusesWhenTheBoardIsInAWorktree(t *testing.T) {
	m, src, _ := dispatchBoard(t)
	src.list = herdr.WorktreeListResult{
		Source: herdr.WorktreeSource{RepoRoot: "/src/tktban", SourceCheckoutPath: testDispatchDir},
		Worktrees: []herdr.WorktreeInfo{
			{Path: testDispatchDir, Branch: "feature/tkt-9-x", IsLinkedWorktree: true},
		},
	}
	m = pressD(t, m)
	if !strings.Contains(m.status, "open in a worktree") || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
	if m.modal != nil {
		t.Fatalf("opened a %T", m.modal)
	}
}

// The modal is the point of this change: it has to show every decision a
// dispatch would take, before one is taken.
func TestDispatchModalShowsTheWholePlan(t *testing.T) {
	m, src, _ := dispatchBoard(t)
	m = pressD(t, m)
	dm, ok := m.modal.(dispatchModal)
	if !ok {
		t.Fatalf("modal = %T, want dispatchModal (status %q)", m.modal, m.status)
	}
	if len(src.listed) != 1 || src.listed[0] != testDispatchDir {
		t.Fatalf("worktree.list calls = %v", src.listed)
	}
	if dm.plan.Key != "TKT-1" {
		t.Fatalf("plan key = %q", dm.plan.Key)
	}
	view := dm.View(m.styles, 120, 40)
	for _, want := range []string{
		"Dispatch TKT-1",
		"first thing", // the summary
		"To Do → Done",
		"feature/tkt-1-first-thing", // branch
		"main",                      // base
		"olddognewflex/tktban",      // repo, as tkt names it
		"/src/tktban",               // repo root, as herdr resolved it
		"herdr chooses the path",    // worktree placement
		"claude as tkt-1",           // agent kind and name
		"Work ticket TKT-1",         // the prompt
		"Dry run: nothing is created yet",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the confirm modal is missing %q:\n%s", want, view)
		}
	}
}

// The dialog is the PR's deliverable, and it is useless if it does not fit.
// Nothing in the plan has a length the dialog controls — repository paths, a
// branch built from a ticket summary, a multi-line prompt — so it is checked
// at the widths people actually run.
func TestDispatchModalFitsTheTerminal(t *testing.T) {
	m, _, _ := dispatchBoard(t)
	// A plan with nothing short in it: a long summary (so a long branch), a
	// deep repository path, and the built-in prompt.
	m.columns[0].Cards[0].Summary =
		"dispatch a ticket to a herdr agent with a great many words in its summary"
	m = pressD(t, m)
	dm, ok := m.modal.(dispatchModal)
	if !ok {
		t.Fatalf("modal = %T (status %q)", m.modal, m.status)
	}

	for _, width := range []int{80, 100, 120, 200, 40} {
		view := dm.View(m.styles, width, 40)
		if got := lipgloss.Width(view); got > width {
			t.Errorf("at width %d the dialog rendered %d columns wide", width, got)
		}
		for i, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("at width %d line %d is %d columns:\n%s", width, i, got, line)
			}
		}
	}

	// Wrapping is display only: the plan the create sequence will consume
	// still holds the prompt exactly as it was built.
	if strings.Contains(dm.plan.Prompt, "\n\n") != strings.Contains(herdr.DefaultPrompt, "\n\n") {
		t.Error("rendering the dialog altered the plan's prompt")
	}

	// The title is a line too. Nothing bounds a ticket key's length —
	// normalizeKey only trims and uppercases — so a long but perfectly valid
	// key must not walk out of the dialog either.
	long := dm
	long.plan.Key = "VERYLONGPROJECTPREFIX_FOR_A_REAL_TEAM-123456"
	for _, width := range []int{40, 80, 120} {
		view := long.View(m.styles, width, 40)
		for i, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("long key at width %d: line %d is %d columns:\n%s", width, i, got, line)
			}
		}
	}
}

// The board is sized before the first WindowSizeMsg arrives, and a modal can
// be asked to render then.
func TestDispatchModalUnsizedTerminal(t *testing.T) {
	m, _, _ := dispatchBoard(t)
	m = pressD(t, m)
	dm := m.modal.(dispatchModal)
	if view := dm.View(m.styles, 0, 0); view == "" {
		t.Fatal("an unsized dialog rendered nothing")
	}
	if got := lipgloss.Width(dm.View(m.styles, 0, 0)); got > 90 {
		t.Fatalf("an unsized dialog rendered %d columns wide", got)
	}
}

// A branch that already has a worktree is reused rather than cut again, and
// the modal says so before the person agrees to anything.
func TestDispatchModalSaysWhenAWorktreeWouldBeReused(t *testing.T) {
	m, src, _ := dispatchBoard(t)
	src.list.Worktrees = append(src.list.Worktrees, herdr.WorktreeInfo{
		Path: "/wt/tkt-1", Branch: "feature/tkt-1-first-thing", IsLinkedWorktree: true,
	})
	m = pressD(t, m)
	dm, ok := m.modal.(dispatchModal)
	if !ok {
		t.Fatalf("modal = %T (status %q)", m.modal, m.status)
	}
	view := dm.View(m.styles, 120, 40)
	if !strings.Contains(view, "reuse /wt/tkt-1") {
		t.Errorf("the modal did not offer to reuse the existing worktree:\n%s", view)
	}
	if strings.Contains(view, "herdr chooses the path") {
		t.Errorf("the modal offered both a reuse and a new placement:\n%s", view)
	}
}

// The whole promise of this change: enter creates nothing.
func TestDispatchEnterCreatesNothing(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	m = pressD(t, m)
	dm, ok := m.modal.(dispatchModal)
	if !ok {
		t.Fatalf("modal = %T (status %q)", m.modal, m.status)
	}
	before := len(cr.calls)

	// Driven the way the program drives it: enter goes to the open modal, the
	// modal answers with a command, and that command's message comes back to
	// the board. Feeding dispatchResultMsg by hand would skip the modal's own
	// key mapping, which is the thing under test.
	m, cmd := update(m, key("enter"))
	if cmd == nil {
		t.Fatal("enter produced no result from the confirm modal")
	}
	res, ok := cmd().(dispatchResultMsg)
	if !ok {
		t.Fatalf("enter produced %T, want dispatchResultMsg", cmd())
	}
	if !res.confirmed || res.plan.Key != "TKT-1" {
		t.Fatalf("enter produced %+v, want a confirmation for TKT-1", res)
	}
	// The whole plan comes back, not just the key: this is what the create
	// sequence will act on, and it must be the one that was rendered.
	if res.plan.Branch != dm.plan.Branch || res.plan.AgentName != dm.plan.AgentName ||
		res.plan.Base != dm.plan.Base || res.plan.TargetRole != dm.plan.TargetRole {
		t.Fatalf("enter returned a different plan:\n got %+v\nwant %+v", res.plan, dm.plan)
	}
	if res.pre != dm.pre {
		t.Fatalf("enter returned preflight %+v, want %+v", res.pre, dm.pre)
	}
	m, _ = update(m, res)

	if m.modal != nil {
		t.Errorf("enter left a %T open", m.modal)
	}
	if !strings.HasPrefix(m.status, "Dry run") || !strings.Contains(m.status, "TKT-1") {
		t.Errorf("status = %q, want the dry-run wording", m.status)
	}
	if m.statusKind != "" {
		t.Errorf("status kind = %q, want a plain status", m.statusKind)
	}
	// Exactly one herdr call for the whole flow, and it was the preflight
	// read. Every other herdr call on the fake fails the test on sight.
	if len(src.listed) != 1 {
		t.Errorf("herdr worktree.list calls = %v, want exactly one", src.listed)
	}
	if call := nonReadCall(cr); call != nil {
		t.Errorf("the dry run ran tkt %v, which is not a read", call)
	}
	for _, call := range cr.calls[before:] {
		t.Errorf("enter ran tkt %v", call)
	}
}

// esc says nothing at all: cancelling a dialog needs no announcement.
func TestDispatchEscDoesNothing(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	m = pressD(t, m)
	before := len(cr.calls)
	m, cmd := update(m, key("esc"))
	if cmd == nil {
		t.Fatal("esc produced no result from the confirm modal")
	}
	res, ok := cmd().(dispatchResultMsg)
	if !ok {
		t.Fatalf("esc produced %T, want dispatchResultMsg", cmd())
	}
	if res.confirmed {
		t.Fatalf("esc confirmed the dispatch: %+v", res)
	}
	m, _ = update(m, res)
	if m.modal != nil {
		t.Errorf("esc left a %T open", m.modal)
	}
	if m.status != "" {
		t.Errorf("esc set the status to %q", m.status)
	}
	if len(src.listed) != 1 {
		t.Errorf("worktree.list calls = %v", src.listed)
	}
	if len(cr.calls) != before {
		t.Errorf("esc ran tkt %v", cr.calls[before:])
	}
}

// The in-flight plan carries its own ticket. A board refresh that re-points
// the selection between the keypress and the confirm must not re-point the
// dispatch with it — otherwise D on one card could dialog about another.
func TestDispatchPlanSurvivesARefreshMidPreflight(t *testing.T) {
	m, _, _ := dispatchBoard(t)
	m, cmd := update(m, key("D"))
	if cmd == nil {
		t.Fatal("D did not start a preparation")
	}

	// The auto-refresh lands first, and the To Do column now leads with a
	// different ticket entirely.
	m, _ = update(m, boardMsg{
		roles: []model.RolePair{{Role: "todo", Lane: "To Do"}, {Role: "done", Lane: "Done"}},
		columns: []model.Column{
			{Lane: "To Do", Role: "todo", Cards: []model.Card{{Key: "TKT-7", Summary: "something else"}}},
			{Lane: "Done", Role: "done"},
		},
	})
	if card, _ := m.selectedCard(); card.Key != "TKT-7" {
		t.Fatalf("setup: selection is %q, want the refreshed card", card.Key)
	}

	m, _ = update(m, cmd())
	dm, ok := m.modal.(dispatchModal)
	if !ok {
		t.Fatalf("modal = %T (status %q)", m.modal, m.status)
	}
	if dm.plan.Key != "TKT-1" {
		t.Fatalf("the dispatch re-pointed to %s; it must stay on the card D was pressed on", dm.plan.Key)
	}
	if !strings.Contains(dm.plan.Branch, "tkt-1") {
		t.Fatalf("branch = %q, want the original ticket's", dm.plan.Branch)
	}
}

// The distinguishing case for the re-check, which no other test covers: the
// selection moves AND an agent appears — on the card the selection moved to,
// not on the one being dispatched.
//
// A re-check that read the selection would refuse here, naming the wrong
// ticket; one that reads the plan's own key ignores TKT-7's agent entirely
// and confirms TKT-1. It is the guard for that exact line.
func TestDispatchRecheckUsesThePlanNotTheSelection(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	m = pressD(t, m)
	if _, ok := m.modal.(dispatchModal); !ok {
		t.Fatalf("modal = %T (status %q)", m.modal, m.status)
	}

	// An auto-refresh re-points the selection at a different ticket.
	m, _ = update(m, boardMsg{
		roles: []model.RolePair{{Role: "todo", Lane: "To Do"}, {Role: "done", Lane: "Done"}},
		columns: []model.Column{
			{Lane: "To Do", Role: "todo", Cards: []model.Card{{Key: "TKT-7", Summary: "something else"}}},
			{Lane: "Done", Role: "done"},
		},
	})
	if card, _ := m.selectedCard(); card.Key != "TKT-7" {
		t.Fatalf("setup: selection is %q, want the refreshed card", card.Key)
	}

	// And an agent appears on THAT ticket — not on the one being dispatched.
	src.byKey = map[string]herdr.Live{"TKT-7": {
		Status: herdr.StatusWorking,
		Panes:  []herdr.PaneRef{{PaneID: "wC:p9", Status: herdr.StatusWorking}},
	}}
	m = poll(t, m)

	m, cmd := update(m, key("enter"))
	res, ok := cmd().(dispatchResultMsg)
	if !ok || !res.confirmed {
		t.Fatalf("enter produced %+v", cmd())
	}
	if res.plan.Key != "TKT-1" {
		t.Fatalf("the confirm answered for %s, want TKT-1", res.plan.Key)
	}
	m, _ = update(m, res)

	want := "Dry run — dispatch lands in the next change; nothing was created for TKT-1"
	if m.status != want {
		t.Fatalf("status = %q (%s), want %q — TKT-7's agent is not TKT-1's",
			m.status, m.statusKind, want)
	}
	if m.statusKind != "" {
		t.Errorf("status kind = %q, want a plain status", m.statusKind)
	}
	if len(src.listed) != 1 {
		t.Errorf("worktree.list calls = %v, want exactly one", src.listed)
	}
	if call := nonReadCall(cr); call != nil {
		t.Errorf("the dry run ran tkt %v, which is not a read", call)
	}
}

// A dialog that replaces the one you are typing in is worse than a dispatch
// you have to ask for again — but it has to say so, or D looks broken.
func TestDispatchPrepDroppedWhenAnotherModalOpened(t *testing.T) {
	m, _, _ := dispatchBoard(t)
	m, cmd := update(m, key("D"))
	m = step(m, key("f")) // the person opens the filter modal meanwhile
	if _, ok := m.modal.(filterModal); !ok {
		t.Fatalf("setup: modal = %T", m.modal)
	}
	m, _ = update(m, cmd())
	if _, ok := m.modal.(filterModal); !ok {
		t.Fatalf("the prepared dispatch replaced the open modal with a %T", m.modal)
	}
	if m.dispatching {
		t.Fatal("the board still thinks a dispatch is in flight")
	}
	want := "Dispatch for TKT-1 was dropped behind the open dialog — press D again"
	if m.status != want || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s), want %q (warn)", m.status, m.statusKind, want)
	}
}

// The keypress guard goes stale: live status keeps polling while the dialog
// sits open, so an agent can appear on the ticket between D and enter. Today
// that only changes the wording; in the change that makes enter create things
// it is what stops two agents racing one branch.
func TestDispatchRefusesAnAgentThatAppearedDuringTheDialog(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	m = pressD(t, m)
	if _, ok := m.modal.(dispatchModal); !ok {
		t.Fatalf("modal = %T (status %q)", m.modal, m.status)
	}

	// herdr reports an agent on TKT-1 while the dialog is open. Live polling
	// runs behind modals, so this is the ordinary course of events.
	src.byKey = map[string]herdr.Live{"TKT-1": {
		Status: herdr.StatusWorking,
		Panes:  []herdr.PaneRef{{PaneID: "wC:p1", Status: herdr.StatusWorking}},
	}}
	m = poll(t, m)
	if _, still := m.modal.(dispatchModal); !still {
		t.Fatalf("a poll closed the confirm dialog (modal = %T)", m.modal)
	}

	before := len(cr.calls)
	m, cmd := update(m, key("enter"))
	res, ok := cmd().(dispatchResultMsg)
	if !ok || !res.confirmed {
		t.Fatalf("enter produced %+v", cmd())
	}
	m, _ = update(m, res)

	want := "TKT-1 picked up an agent while the dialog was open (o focuses it)"
	if m.status != want || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s), want %q (warn)", m.status, m.statusKind, want)
	}
	if m.modal != nil {
		t.Errorf("the dialog stayed open as a %T", m.modal)
	}
	if len(src.listed) != 1 {
		t.Errorf("worktree.list calls = %v, want exactly one", src.listed)
	}
	if call := nonReadCall(cr); call != nil {
		t.Errorf("a refused confirm ran tkt %v, which is not a read", call)
	}
	if len(cr.calls) != before {
		t.Errorf("the confirm ran tkt %v", cr.calls[before:])
	}
}

// d is still the date modal. D is a different key on purpose, and adding it
// must not have moved the one next to it.
func TestLowercaseDStillOpensTheDateModal(t *testing.T) {
	m, _, _ := dispatchBoard(t)
	m = step(m, key("d"))
	if _, ok := m.modal.(dateModal); !ok {
		t.Fatalf("modal = %T, want dateModal", m.modal)
	}
}

// Only enter and esc mean anything. A stray keystroke landing on the confirm
// dialog must not answer it — least of all confirm it.
func TestDispatchModalIgnoresOtherKeys(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	m = pressD(t, m)
	if _, ok := m.modal.(dispatchModal); !ok {
		t.Fatalf("modal = %T (status %q)", m.modal, m.status)
	}
	before := len(cr.calls)
	for _, k := range []string{"j", "k", "q", "y", "n", "D", "d", " "} {
		var cmd tea.Cmd
		m, cmd = update(m, key(k))
		if cmd != nil {
			if res, ok := cmd().(dispatchResultMsg); ok {
				t.Fatalf("%q answered the confirm dialog: %+v", k, res)
			}
		}
		if _, still := m.modal.(dispatchModal); !still {
			t.Fatalf("%q closed the confirm dialog (modal is now %T)", k, m.modal)
		}
	}
	if len(src.listed) != 1 {
		t.Errorf("worktree.list calls = %v, want exactly one", src.listed)
	}
	if len(cr.calls) != before {
		t.Errorf("stray keys ran tkt %v", cr.calls[before:])
	}
}

func TestFooterAdvertisesDispatch(t *testing.T) {
	if !strings.Contains(footerKeys, "D dispatch") {
		t.Fatalf("footer missing the dispatch key: %s", footerKeys)
	}
}

// errPoll is a poll failure for the "herdr has gone quiet" guard.
var errPoll = &herdr.APIError{Code: "internal_error", Message: "connection refused"}
