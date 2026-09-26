package ui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/model"
)

// ---- fixtures ----

// createdWorktree is herdr's worktree_created reply for TKT-1's plan.
func createdWorktree() herdr.WorktreeResult {
	return herdr.WorktreeResult{
		Workspace: herdr.WorkspaceInfo{WorkspaceID: "wE", Label: "TKT-1", Number: 5},
		Tab:       herdr.TabInfo{TabID: "wE:t1", WorkspaceID: "wE", Label: "TKT-1"},
		RootPane: herdr.PaneInfo{
			PaneID: "wE:p1", WorkspaceID: "wE", TabID: "wE:t1",
			TerminalID: "term_1", Cwd: "/wt/tkt-1",
		},
		Worktree: herdr.WorktreeInfo{
			Path: "/wt/tkt-1", Branch: "feature/tkt-1-first-thing",
			Label: "TKT-1", OpenWorkspaceID: "wE", IsLinkedWorktree: true,
		},
	}
}

func startedAgent() herdr.AgentStartResult {
	agent := "claude"
	return herdr.AgentStartResult{
		Agent: herdr.Agent{PaneID: "wE:p1", WorkspaceID: "wE", TabID: "wE:t1", Agent: &agent, Status: herdr.StatusIdle},
		Argv:  []string{"claude"},
	}
}

// ---- drivers ----

// runDispatch drives a confirmed dispatch to the end: every staged command in
// turn, synchronously, exactly as the program would.
//
// It stops as soon as a command produces a message that is not one of the
// dispatch's own — which is how the last step is recognised, because the last
// step batches a status timer and a board refresh that belong to the board.
func runDispatch(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for range 12 {
		if cmd == nil {
			return m
		}
		msg := cmd()
		switch msg.(type) {
		case dispatchStageMsg, dispatchLaneMsg, dispatchDoneMsg:
		default:
			return m
		}
		m, cmd = update(m, msg)
	}
	t.Fatal("the dispatch did not finish in 12 steps")
	return m
}

// confirmDispatch answers the open confirm dialog with enter and drives the
// whole create sequence.
func confirmDispatch(t *testing.T, m Model) Model {
	t.Helper()
	if _, ok := m.modal.(dispatchModal); !ok {
		t.Fatalf("no confirm dialog open (modal = %T, status %q)", m.modal, m.status)
	}
	m, cmd := update(m, key("enter"))
	if cmd == nil {
		t.Fatal("enter produced no result from the confirm modal")
	}
	res, ok := cmd().(dispatchResultMsg)
	if !ok || !res.confirmed {
		t.Fatalf("enter produced %+v, want a confirmation", cmd())
	}
	m, cmd = update(m, res)
	return runDispatch(t, m, cmd)
}

// dispatchOnce is the whole flow: D, then enter, then the create sequence.
func dispatchOnce(t *testing.T, m Model) Model {
	t.Helper()
	return confirmDispatch(t, pressD(t, m))
}

// recordSleeps swaps the retry backoff for a recorder, so the retry budget can
// be asserted without spending a single millisecond of it.
func recordSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	orig := dispatchSleep
	dispatchSleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	t.Cleanup(func() { dispatchSleep = orig })
	return &waits
}

// ---- the happy path ----

// The whole sequence, asserted as the traffic it is: the ordered herdr method
// sequence, the exact params of each call, the two tkt writes, and the status.
//
// Every one of those params is a decision, and every one of them is invisible in
// the result: no path (herdr places the worktree), no trust_repository (trusting
// a repo is a write), focus false (a dispatch does not steal the screen),
// timeout_ms 20000 (so herdr's own timeout fires before ours), and no wait on
// the prompt (a board must not block on an agent).
func TestDispatchCreatesTheWorktreeStartsTheAgentAndRecordsIt(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	m.settings["dispatch_args"] = "--permission-mode plan"

	m = dispatchOnce(t, m)

	assertHerdrCalls(t, src, "worktree.list", "worktree.create", "agent.start", "agent.prompt")

	want := herdr.WorktreeCreateParams{
		Cwd:    testDispatchDir,
		Branch: "feature/tkt-1-first-thing",
		Base:   "main",
		Label:  "TKT-1",
		Focus:  false,
	}
	if got := src.createParams[0]; got != want {
		t.Errorf("worktree.create params = %+v, want %+v", got, want)
	}
	// Spelled out again: these two are refusals, not defaults that happen to be
	// zero.
	if got := src.createParams[0]; got.Path != "" || got.TrustRepository {
		t.Errorf("worktree.create sent path=%q trust_repository=%v", got.Path, got.TrustRepository)
	}

	start := src.startParams[0]
	if start.Name != "tkt-1" || start.Kind != "claude" || start.PaneID != "wE:p1" ||
		start.TimeoutMS != 20000 || !slices.Equal(start.Args, []string{"--permission-mode", "plan"}) {
		t.Errorf("agent.start params = %+v", start)
	}

	prompt := src.promptParams[0]
	if prompt.Target != "tkt-1" || !strings.Contains(prompt.Text, "Work ticket TKT-1") {
		t.Errorf("agent.prompt params = %+v", prompt)
	}

	// The ticket half.
	if call := cr.last("transition"); !slices.Equal(call, []string{"transition", "TKT-1", "done"}) {
		t.Errorf("tkt transition = %v, want the plan's target role", call)
	}
	comment := cr.last("comment")
	if len(comment) != 3 || comment[1] != "TKT-1" {
		t.Fatalf("tkt comment = %v", comment)
	}
	for _, want := range []string{
		"feature/tkt-1-first-thing (from main)",
		"/wt/tkt-1 (created)",
		"wE", "wE:t1", "wE:p1",
		"tkt-1 (claude)",
		"prompt: sent",
		"To Do → Done",
	} {
		if !strings.Contains(comment[2], want) {
			t.Errorf("the comment is missing %q:\n%s", want, comment[2])
		}
	}

	if m.modal != nil {
		t.Errorf("a finished dispatch left a %T open", m.modal)
	}
	if m.dispatchSrc != nil {
		t.Error("the board still thinks a dispatch is in flight")
	}
	for _, want := range []string{"Dispatched TKT-1", "tkt-1", "/wt/tkt-1", "o focuses it", "moved to Done"} {
		if !strings.Contains(m.status, want) {
			t.Errorf("status = %q, want it to mention %q", m.status, want)
		}
	}
	if m.statusKind != "" {
		t.Errorf("status kind = %q, want a plain status on a clean dispatch", m.statusKind)
	}
}

// A branch that already has a worktree is opened, never cut a second time.
func TestDispatchReusesAnExistingWorktree(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	src.list.Worktrees = append(src.list.Worktrees, herdr.WorktreeInfo{
		Path: "/wt/tkt-1", Branch: "feature/tkt-1-first-thing", IsLinkedWorktree: true, OpenWorkspaceID: "wE",
	})
	open := createdWorktree()
	open.AlreadyOpen = true
	src.open, src.start = open, startedAgent()

	m = dispatchOnce(t, m)

	assertHerdrCalls(t, src, "worktree.list", "worktree.open", "agent.start", "agent.prompt")
	if len(src.createParams) != 0 {
		t.Fatalf("a branch that already has a worktree was cut again: %+v", src.createParams)
	}
	want := herdr.WorktreeOpenParams{
		Cwd: testDispatchDir, Branch: "feature/tkt-1-first-thing", Focus: false,
	}
	if got := src.openParams[0]; got != want {
		t.Errorf("worktree.open params = %+v, want exactly %+v (no base, no path, no trust)", got, want)
	}
	if !strings.Contains(m.status, "reusing its worktree") {
		t.Errorf("status = %q, want it to say the worktree was reused", m.status)
	}
	if c := cr.last("comment"); c == nil || !strings.Contains(c[2], "reused, already open") {
		t.Errorf("the comment did not record the reuse: %v", c)
	}
}

// ---- the failure matrix ----

// One table, every row, each asserting what did AND did not happen. In
// particular: a failed create runs no `tkt transition` but does run a `tkt
// comment`, and no row anywhere calls worktree.remove, pane.close or
// workspace.close — assertHerdrCalls checks that on every recorded sequence,
// not on one case.
func TestDispatchFailureMatrix(t *testing.T) {
	createFail := &herdr.APIError{Code: herdr.CodeWorktreeCreateFailed, Message: "fatal: invalid reference: main"}
	trustFail := &herdr.APIError{Code: herdr.CodeWorkspaceTrustBlocked, Message: "repository is not trusted"}
	busy := &herdr.APIError{Code: herdr.CodeAgentPaneBusy, Message: "pane is not at a shell prompt"}
	blocked := &herdr.APIError{Code: herdr.CodeAgentBlocked, Message: "agent is blocked"}

	cases := []struct {
		name  string
		setup func(m Model, src *fakeDispatch, cr *captureRunner) Model

		wantCalls []string
		// wantStatus are substrings the status line must contain, wantAbsent
		// substrings it must not.
		wantStatus []string
		wantAbsent []string
		wantKind   string
		// wantTransition is the tkt transition argv, nil for "no transition".
		wantTransition []string
		// wantComment are substrings the ticket comment must carry; an empty
		// slice still requires that a comment was made.
		wantComment []string
		noComment   bool
	}{
		{
			// AC2: no transition, a comment carrying herdr's code and message,
			// and a status that names it. Nothing deleted.
			name: "worktree.create fails",
			setup: func(m Model, src *fakeDispatch, _ *captureRunner) Model {
				src.createErr = createFail
				return m
			},
			wantCalls:      []string{"worktree.list", "worktree.create"},
			wantStatus:     []string{"TKT-1 was not dispatched", "worktree.create failed", "worktree_create_failed", "fatal: invalid reference: main", "Nothing was created"},
			wantKind:       "warn",
			wantTransition: nil,
			wantComment:    []string{"could not finish dispatching TKT-1", "worktree_create_failed", "fatal: invalid reference: main", "lane: unchanged (To Do)"},
		},
		{
			name: "the repository is not trusted",
			setup: func(m Model, src *fakeDispatch, _ *captureRunner) Model {
				src.createErr = trustFail
				return m
			},
			wantCalls:      []string{"worktree.list", "worktree.create"},
			wantStatus:     []string{"workspace_trust_blocked", "repository is not trusted", "Nothing was created"},
			wantKind:       "warn",
			wantTransition: nil,
			wantComment:    []string{"workspace_trust_blocked"},
		},
		{
			name: "worktree.open fails on the reuse path",
			setup: func(m Model, src *fakeDispatch, _ *captureRunner) Model {
				src.list.Worktrees = append(src.list.Worktrees, herdr.WorktreeInfo{
					Path: "/wt/tkt-1", Branch: "feature/tkt-1-first-thing", IsLinkedWorktree: true,
				})
				src.openErr = &herdr.APIError{Code: herdr.CodeWorktreeNotFound, Message: "no such worktree"}
				return m
			},
			wantCalls:      []string{"worktree.list", "worktree.open"},
			wantStatus:     []string{"worktree.open failed", "worktree_not_found"},
			wantAbsent:     []string{"worktree.create failed"},
			wantKind:       "warn",
			wantTransition: nil,
			wantComment:    []string{"worktree_not_found"},
		},
		{
			// The worktree exists and stays. Nothing is rolled back, the lane
			// does not move, and the comment records everything needed to find
			// what was left behind.
			name: "agent.start fails after the worktree exists",
			setup: func(m Model, src *fakeDispatch, _ *captureRunner) Model {
				src.create = createdWorktree()
				src.startErrs = []error{blocked}
				return m
			},
			wantCalls:      []string{"worktree.list", "worktree.create", "agent.start"},
			wantStatus:     []string{"TKT-1's worktree is ready at /wt/tkt-1", "the agent did not start", "agent_blocked", "Nothing was removed", "TKT-1 did not move"},
			wantKind:       "warn",
			wantTransition: nil,
			wantComment: []string{
				"feature/tkt-1-first-thing", "/wt/tkt-1 (created)", "wE", "wE:p1",
				"agent_blocked", "not started (tkt-1); the worktree and its pane were left in place",
			},
		},
		{
			name: "agent.start stays busy for the whole budget",
			setup: func(m Model, src *fakeDispatch, _ *captureRunner) Model {
				src.create = createdWorktree()
				src.startErrs = []error{busy, busy, busy, busy, busy, busy}
				return m
			},
			wantCalls: []string{"worktree.list", "worktree.create",
				"agent.start", "agent.start", "agent.start", "agent.start", "agent.start", "agent.start"},
			wantStatus:     []string{"worktree is ready", "agent_pane_busy", "Nothing was removed"},
			wantKind:       "warn",
			wantTransition: nil,
			wantComment:    []string{"agent_pane_busy"},
		},
		{
			// The agent IS dispatched, so the lane still moves and the ticket
			// is still annotated; only the prompt is the person's to paste.
			name: "agent.prompt fails",
			setup: func(m Model, src *fakeDispatch, _ *captureRunner) Model {
				src.create, src.start = createdWorktree(), startedAgent()
				src.promptErr = blocked
				return m
			},
			wantCalls:      []string{"worktree.list", "worktree.create", "agent.start", "agent.prompt"},
			wantStatus:     []string{"Dispatched TKT-1 to tkt-1", "the first prompt did not land", "agent_blocked", "o focuses the pane", "moved to Done"},
			wantKind:       "warn",
			wantTransition: []string{"transition", "TKT-1", "done"},
			wantComment:    []string{"NOT sent — paste it into the pane by hand", "To Do → Done"},
		},
		{
			name: "the transition fails",
			setup: func(m Model, src *fakeDispatch, cr *captureRunner) Model {
				src.create, src.start = createdWorktree(), startedAgent()
				cr.failVerbs = map[string]bool{"transition": true}
				return m
			},
			wantCalls:      []string{"worktree.list", "worktree.create", "agent.start", "agent.prompt"},
			wantStatus:     []string{"Dispatched TKT-1", "the lane did not move"},
			wantKind:       "warn",
			wantTransition: []string{"transition", "TKT-1", "done"},
			wantComment:    []string{"lane: did not move"},
		},
		{
			name: "the comment fails",
			setup: func(m Model, src *fakeDispatch, cr *captureRunner) Model {
				src.create, src.start = createdWorktree(), startedAgent()
				cr.failVerbs = map[string]bool{"comment": true}
				return m
			},
			wantCalls:      []string{"worktree.list", "worktree.create", "agent.start", "agent.prompt"},
			wantStatus:     []string{"Dispatched TKT-1", "moved to Done", "not recorded on the ticket"},
			wantKind:       "warn",
			wantTransition: []string{"transition", "TKT-1", "done"},
			// The comment was attempted; it just failed.
			wantComment: nil,
		},
		{
			// herdr never answered. We cannot know whether the call landed, so
			// the wording admits it — and nothing retries the worktree step,
			// which is what stops a ticket ending up with two worktrees.
			name: "herdr is unreachable",
			setup: func(m Model, src *fakeDispatch, _ *captureRunner) Model {
				src.createErr = errDial
				return m
			},
			wantCalls:      []string{"worktree.list", "worktree.create"},
			wantStatus:     []string{"worktree.create did not answer", "the dispatch may be incomplete"},
			wantKind:       "warn",
			wantTransition: nil,
			wantComment:    []string{"may be incomplete"},
		},
		{
			// The ticket left the lane the plan was built for while the
			// sequence ran. Moving it from wherever it is now to the plan's
			// target is not the transition anybody agreed to.
			name: "the ticket left the dispatch source lane",
			setup: func(m Model, src *fakeDispatch, _ *captureRunner) Model {
				src.create, src.start = createdWorktree(), startedAgent()
				return m
			},
			wantCalls:      []string{"worktree.list", "worktree.create", "agent.start", "agent.prompt"},
			wantStatus:     []string{"Dispatched TKT-1", "had already left To Do", "Done now", "the lane was left alone"},
			wantKind:       "warn",
			wantTransition: nil,
			wantComment:    []string{"left alone — TKT-1 had already left To Do (it is in Done now)"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recordSleeps(t)
			m, src, cr := dispatchBoard(t)
			m = c.setup(m, src, cr)
			m = pressD(t, m)
			if c.name == "the ticket left the dispatch source lane" {
				// A refresh lands between the confirm and the writes, with
				// TKT-1 now in Done.
				m, cmd := update(m, key("enter"))
				res := cmd().(dispatchResultMsg)
				m, cmd = update(m, res)
				m, _ = update(m, movedBoard())
				m = runDispatch(t, m, cmd)
				assertMatrixRow(t, m, src, cr, c.wantCalls, c.wantStatus, c.wantAbsent, c.wantKind, c.wantTransition, c.wantComment, c.noComment)
				return
			}
			m = confirmDispatch(t, m)
			assertMatrixRow(t, m, src, cr, c.wantCalls, c.wantStatus, c.wantAbsent, c.wantKind, c.wantTransition, c.wantComment, c.noComment)
		})
	}
}

// assertMatrixRow checks one row of the failure matrix.
func assertMatrixRow(t *testing.T, m Model, src *fakeDispatch, cr *captureRunner,
	wantCalls, wantStatus, wantAbsent []string, wantKind string,
	wantTransition, wantComment []string, noComment bool,
) {
	t.Helper()
	assertHerdrCalls(t, src, wantCalls...)

	for _, want := range wantStatus {
		if !strings.Contains(m.status, want) {
			t.Errorf("status = %q, want it to contain %q", m.status, want)
		}
	}
	for _, absent := range wantAbsent {
		if strings.Contains(m.status, absent) {
			t.Errorf("status = %q, want it NOT to contain %q", m.status, absent)
		}
	}
	if m.statusKind != wantKind {
		t.Errorf("status kind = %q, want %q", m.statusKind, wantKind)
	}

	got := cr.last("transition")
	if wantTransition == nil {
		if got != nil {
			t.Errorf("tkt transition = %v, want none: the lane must not move", got)
		}
	} else if !slices.Equal(got, wantTransition) {
		t.Errorf("tkt transition = %v, want %v", got, wantTransition)
	}

	comment := cr.last("comment")
	if noComment {
		if comment != nil {
			t.Errorf("tkt comment = %v, want none", comment)
		}
	} else if comment == nil {
		t.Fatal("no tkt comment: a dispatch that went wrong has to leave a record")
	} else {
		for _, want := range wantComment {
			if !strings.Contains(comment[2], want) {
				t.Errorf("the comment is missing %q:\n%s", want, comment[2])
			}
		}
	}

	// Whatever happened, the dialog is closed and the board is idle again.
	if m.modal != nil {
		t.Errorf("a finished dispatch left a %T open", m.modal)
	}
	if m.dispatchSrc != nil {
		t.Error("the board still thinks a dispatch is in flight")
	}
}

// movedBoard is a refresh in which TKT-1 has moved from To Do to Done.
func movedBoard() boardMsg {
	return boardMsg{
		roles: []model.RolePair{{Role: "todo", Lane: "To Do"}, {Role: "done", Lane: "Done"}},
		columns: []model.Column{
			{Lane: "To Do", Role: "todo"},
			{Lane: "Done", Role: "done", Cards: []model.Card{{Key: "TKT-1", Summary: "first thing"}}},
		},
	}
}

// errDial is herdr not being there at all: no code, no message, no way to know
// whether the call landed.
var errDial = &noCodeError{"dial unix /run/herdr.sock: connect: no such file or directory"}

type noCodeError struct{ msg string }

func (e *noCodeError) Error() string { return e.msg }

// ---- the retry budget, through the board ----

// Three busy replies then a success: four agent.start calls, and the delays the
// board waited are exactly the documented schedule. No wall clock is involved —
// the sleeper is injected and records instead of waiting.
func TestDispatchRetriesABusyPaneThroughTheBoard(t *testing.T) {
	busy := &herdr.APIError{Code: herdr.CodeAgentPaneBusy, Message: "pane is not at a shell prompt"}
	waits := recordSleeps(t)
	m, src, cr := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	src.startErrs = []error{busy, busy, busy}

	m = dispatchOnce(t, m)

	assertHerdrCalls(t, src, "worktree.list", "worktree.create",
		"agent.start", "agent.start", "agent.start", "agent.start", "agent.prompt")
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	if !slices.Equal(*waits, want) {
		t.Fatalf("waits = %v, want %v", *waits, want)
	}
	// A dispatch that sat there for most of a second says why, or it looks
	// broken rather than patient.
	if !strings.Contains(m.status, "waiting 700ms for the shell") {
		t.Errorf("status = %q, want it to account for the wait", m.status)
	}
	if c := cr.last("comment"); c == nil || !strings.Contains(c[2], "4 attempts, 3 busy") {
		t.Errorf("the comment did not record the retries: %v", c)
	}
	if got := src.startParams[3].Name; got != "tkt-1" {
		t.Errorf("the retry changed the name to %q; a busy pane is not a name collision", got)
	}
}

// A name collision means an earlier agent on this ticket is still live. The next
// candidate is tried once, out of the same budget.
func TestDispatchRetriesOnceOnAgentNameTaken(t *testing.T) {
	taken := &herdr.APIError{Code: herdr.CodeAgentNameTaken, Message: "agent name is in use"}
	waits := recordSleeps(t)
	m, src, _ := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	src.startErrs = []error{taken}

	m = dispatchOnce(t, m)

	assertHerdrCalls(t, src, "worktree.list", "worktree.create", "agent.start", "agent.start", "agent.prompt")
	names := []string{src.startParams[0].Name, src.startParams[1].Name}
	if !slices.Equal(names, []string{"tkt-1", "tkt-1-2"}) {
		t.Fatalf("names = %v, want the next candidate second", names)
	}
	// One slot of the same schedule, not a budget of its own.
	if want := []time.Duration{100 * time.Millisecond}; !slices.Equal(*waits, want) {
		t.Errorf("waits = %v, want %v", *waits, want)
	}
	// The prompt goes to the name the agent actually started under, not the one
	// the plan wanted — otherwise it would land in the other agent's pane.
	if got := src.promptParams[0].Target; got != "tkt-1-2" {
		t.Fatalf("prompt target = %q, want tkt-1-2", got)
	}
	if !strings.Contains(m.status, "the name tkt-1 was taken") {
		t.Errorf("status = %q, want it to say the name was taken", m.status)
	}
}

// ---- staging, budgets and the dialog ----

// The dialog shows each stage as it runs, and keeps the finished ones as a log
// — which is what makes a sequence that stopped half-way readable.
func TestDispatchProgressStagesAdvance(t *testing.T) {
	m, src, _ := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	m = pressD(t, m)

	m, cmd := update(m, key("enter"))
	m, cmd = update(m, cmd().(dispatchResultMsg))

	var progress []string
	for range 12 {
		if cmd == nil {
			break
		}
		dm, ok := m.modal.(dispatchModal)
		if !ok {
			break
		}
		progress = append(progress, dm.progress)
		// The dialog renders at every stage, and it still has to fit.
		view := dm.View(m.styles, 100, 40)
		if !strings.Contains(view, dm.progress) {
			t.Fatalf("the dialog does not show the step it is on (%q):\n%s", dm.progress, view)
		}
		if !strings.Contains(view, "Dispatch TKT-1") {
			t.Errorf("the dialog stopped naming the ticket:\n%s", view)
		}
		if strings.Contains(view, "enter dispatch") {
			t.Errorf("the dialog still offers a key while in flight:\n%s", view)
		}
		msg := cmd()
		switch msg.(type) {
		case dispatchStageMsg, dispatchLaneMsg, dispatchDoneMsg:
		default:
			cmd = nil
			continue
		}
		m, cmd = update(m, msg)
	}

	wantSteps := []string{
		"Cutting feature/tkt-1-first-thing and opening a worktree…",
		"Starting claude in the worktree…",
		"Sending the first prompt…",
		"Moving TKT-1 to Done…",
		"Recording the dispatch on TKT-1…",
	}
	if !slices.Equal(progress, wantSteps) {
		t.Fatalf("progress steps =\n %q\nwant\n %q", progress, wantSteps)
	}
	if m.modal != nil {
		t.Errorf("the dialog stayed open as a %T", m.modal)
	}
}

// The reuse path says so while it is happening, not only afterwards.
func TestDispatchProgressSaysWhenItIsReusingAWorktree(t *testing.T) {
	m, src, _ := dispatchBoard(t)
	src.list.Worktrees = append(src.list.Worktrees, herdr.WorktreeInfo{
		Path: "/wt/tkt-1", Branch: "feature/tkt-1-first-thing", IsLinkedWorktree: true,
	})
	src.open, src.start = createdWorktree(), startedAgent()
	m = pressD(t, m)
	m, cmd := update(m, key("enter"))
	m, _ = update(m, cmd().(dispatchResultMsg))
	dm, ok := m.modal.(dispatchModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	if !strings.Contains(dm.progress, "Opening the worktree at /wt/tkt-1") {
		t.Fatalf("progress = %q", dm.progress)
	}
}

// Every keystroke is swallowed while a dispatch is in flight. A second enter in
// particular must not reach the board, or it would cut a second worktree.
func TestDispatchSwallowsEveryKeyWhileInFlight(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	m = pressD(t, m)
	m, cmd := update(m, key("enter"))
	m, next := update(m, cmd().(dispatchResultMsg))
	if _, ok := m.modal.(dispatchModal); !ok {
		t.Fatalf("modal = %T", m.modal)
	}

	callsBefore := len(cr.calls)
	for _, k := range []string{"enter", "esc", "D", "d", "q", "j", "r", "m", "c", "n", " "} {
		var cmd tea.Cmd
		m, cmd = update(m, key(k))
		if cmd != nil {
			t.Fatalf("%q produced a command mid-dispatch: %T", k, cmd())
		}
		dm, still := m.modal.(dispatchModal)
		if !still {
			t.Fatalf("%q closed the dialog mid-dispatch (modal = %T)", k, m.modal)
		}
		if !dm.running() {
			t.Fatalf("%q took the dialog out of flight", k)
		}
	}
	if len(cr.calls) != callsBefore {
		t.Errorf("stray keys ran tkt %v", cr.calls[callsBefore:])
	}
	// And q in particular did not quit: a dispatch in flight is not a moment to
	// take the board away.
	if len(src.calls) != 1 {
		t.Fatalf("herdr calls before the sequence ran = %v", src.calls)
	}

	// The sequence still finishes, and exactly once.
	m = runDispatch(t, m, next)
	assertHerdrCalls(t, src, "worktree.list", "worktree.create", "agent.start", "agent.prompt")
	if len(src.createParams) != 1 {
		t.Fatalf("worktree.create ran %d times", len(src.createParams))
	}
}

// A board refresh mid-sequence must not re-point the dispatch. The plan travels
// on the message; nothing downstream reads the selection.
func TestDispatchSurvivesABoardRefreshMidSequence(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	m = pressD(t, m)
	m, cmd := update(m, key("enter"))
	m, next := update(m, cmd().(dispatchResultMsg))

	// The board is filtered, so a card missing from it means hidden, not moved.
	m.filter = filterState{assignee: "bob"}
	// The refresh lands between the confirm and the first create, and the board
	// is now a different board: TKT-1 is not on it, and the selection has moved
	// to another ticket entirely.
	m, _ = update(m, boardMsg{
		roles: []model.RolePair{{Role: "todo", Lane: "To Do"}, {Role: "done", Lane: "Done"}},
		columns: []model.Column{
			{Lane: "To Do", Role: "todo", Cards: []model.Card{{Key: "TKT-7", Summary: "something else"}}},
			{Lane: "Done", Role: "done"},
		},
	})
	if card, _ := m.selectedCard(); card.Key != "TKT-7" {
		t.Fatalf("setup: selection is %q", card.Key)
	}

	m = runDispatch(t, m, next)
	if got := src.createParams[0].Branch; got != "feature/tkt-1-first-thing" {
		t.Fatalf("cut %q, want the branch D was pressed for", got)
	}
	// A ticket the board cannot see is not a ticket that has moved lane: a
	// filter is not a transition, so the plan's own move still runs.
	if call := cr.last("transition"); !slices.Equal(call, []string{"transition", "TKT-1", "done"}) {
		t.Fatalf("tkt transition = %v", call)
	}
	if c := cr.last("comment"); c == nil || c[1] != "TKT-1" {
		t.Fatalf("tkt comment = %v", c)
	}
}

// Each stage gets its own budget, and every one of them reaches the call it
// bounds. A stage with no deadline is a stage that can wedge the dialog open
// forever.
func TestDispatchStageBudgets(t *testing.T) {
	want := map[herdr.Stage]time.Duration{
		herdr.StageWorktree: 15 * time.Second,
		herdr.StageAgent:    30 * time.Second,
		herdr.StagePrompt:   5 * time.Second,
	}
	for stage, d := range want {
		if got := dispatchStageBudget(stage); got != d {
			t.Errorf("%v budget = %v, want %v", stage, got, d)
		}
	}
	// The relation that matters, and the one a single-attempt check gets wrong:
	// the agent stage bounds the WHOLE retry loop, so the last attempt must
	// still have herdr's full startup timeout inside our budget after the
	// backoff has been spent. Otherwise our deadline cuts it short and a herdr
	// timeout — which names what failed — becomes a closed socket, which cannot
	// even say whether the agent started.
	needed := time.Duration(herdr.AgentStartTimeoutMS)*time.Millisecond + herdr.AgentStartBackoffTotal()
	if got := dispatchStageBudget(herdr.StageAgent); needed >= got {
		t.Fatalf("the agent stage gets %v, but herdr's %dms timeout plus %v of backoff needs %v",
			got, herdr.AgentStartTimeoutMS, herdr.AgentStartBackoffTotal(), needed)
	}

	// And the contexts really are bounded, per stage, as the board mints them.
	var seen []time.Duration
	orig := dispatchStageContext
	dispatchStageContext = func(stage herdr.Stage) (context.Context, context.CancelFunc) {
		ctx, cancel := orig(stage)
		dl, ok := ctx.Deadline()
		if !ok {
			t.Errorf("stage %v got an unbounded context", stage)
			return ctx, cancel
		}
		seen = append(seen, time.Until(dl).Round(time.Second))
		return ctx, cancel
	}
	t.Cleanup(func() { dispatchStageContext = orig })

	m, src, cr := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	cr.observeCtx = true
	m = dispatchOnce(t, m)
	if !strings.HasPrefix(m.status, "Dispatched TKT-1") {
		t.Fatalf("status = %q", m.status)
	}
	wantSeen := []time.Duration{15 * time.Second, 30 * time.Second, 5 * time.Second}
	if !slices.Equal(seen, wantSeen) {
		t.Fatalf("stage budgets minted = %v, want %v", seen, wantSeen)
	}

	// The two tkt writes get their own 5s budget, and it reaches the
	// subprocess — a wedged tkt has to be killed, not waited on, because the
	// dialog is open until it answers.
	var writes []ctxCall
	for _, call := range cr.ctxCalls {
		if len(call.args) > 0 && (call.args[0] == "transition" || call.args[0] == "comment") {
			writes = append(writes, call)
		}
	}
	if len(writes) != 2 {
		t.Fatalf("observed %d dispatch writes, want the transition and the comment: %+v", len(writes), cr.ctxCalls)
	}
	for _, call := range writes {
		if !call.deadline {
			t.Errorf("tkt %v ran with no deadline", call.args)
		}
		if call.budget > dispatchWriteTimeout || call.budget < dispatchWriteTimeout-time.Second {
			t.Errorf("tkt %v ran with %v left, want about %v", call.args, call.budget, dispatchWriteTimeout)
		}
	}
}

// A stage whose budget has already run out fails that stage and nothing else —
// and above all does not retry the worktree, because a retry after "we don't
// know whether it landed" is how a ticket ends up with two worktrees.
func TestDispatchStageContextRunsOut(t *testing.T) {
	orig := dispatchStageContext
	dispatchStageContext = func(herdr.Stage) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // the budget is gone before the call is made
		return ctx, cancel
	}
	t.Cleanup(func() { dispatchStageContext = orig })

	m, src, cr := dispatchBoard(t)
	// The fake does not look at the context, so the call "succeeds" on the
	// wire; what is under test is that a dead budget produces one attempt and
	// no second one. Make herdr answer the way a closed socket does instead.
	src.createErr = context.DeadlineExceeded
	m = dispatchOnce(t, m)

	assertHerdrCalls(t, src, "worktree.list", "worktree.create")
	if len(src.createParams) != 1 {
		t.Fatalf("worktree.create ran %d times; a deadline must never be retried", len(src.createParams))
	}
	if !strings.Contains(m.status, "may be incomplete") {
		t.Errorf("status = %q, want it to admit the dispatch may be incomplete", m.status)
	}
	if cr.last("transition") != nil {
		t.Error("a timed-out dispatch moved the lane")
	}
	if c := cr.last("comment"); c == nil || !strings.Contains(c[2], "may be incomplete") {
		t.Fatalf("comment = %v, want it to say the dispatch may be incomplete", c)
	}
}

// A successful dispatch does not quit, even as herdr's popup. There is an agent
// to go and look at, and the person follows with o; quitting would take the
// board away at the moment it has something to show.
func TestDispatchDoesNotQuitThePopup(t *testing.T) {
	m, src, _ := dispatchBoard(t)
	m = m.WithPopup(true)
	src.create, src.start = createdWorktree(), startedAgent()
	m = pressD(t, m)

	m, cmd := update(m, key("enter"))
	m, cmd = update(m, cmd().(dispatchResultMsg))
	for range 12 {
		if cmd == nil {
			break
		}
		msg := cmd()
		if isQuit(msg) {
			t.Fatal("a dispatch quit the program; the popup has to stay open for o")
		}
		switch msg.(type) {
		case dispatchStageMsg, dispatchLaneMsg, dispatchDoneMsg:
		default:
			cmd = nil
			continue
		}
		m, cmd = update(m, msg)
	}
	if !strings.HasPrefix(m.status, "Dispatched TKT-1") {
		t.Fatalf("status = %q", m.status)
	}
}

// isQuit reports whether a message is Bubble Tea's own quit.
func isQuit(msg tea.Msg) bool {
	_, ok := msg.(tea.QuitMsg)
	return ok
}

// The dispatch's own writes are the only ones it makes, and nothing on the path
// is a read verb pretending otherwise. This is PR 1's allowlist, re-run now
// that there is something to allow.
func TestDispatchMakesOnlyItsOwnTwoWrites(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	m = dispatchOnce(t, m)

	var writes [][]string
	for _, call := range cr.calls {
		if !isRead(call) {
			writes = append(writes, call)
		}
	}
	if len(writes) != 2 {
		t.Fatalf("the dispatch made %d writes, want exactly a transition and a comment: %v", len(writes), writes)
	}
	if writes[0][0] != "transition" || writes[1][0] != "comment" {
		t.Fatalf("writes = %v, want transition then comment", writes)
	}
	if !strings.HasPrefix(m.status, "Dispatched TKT-1") {
		t.Fatalf("status = %q", m.status)
	}
}

// A dispatch from a card whose lane has no agent-owned transition never gets
// this far — but a plan with no target lane must still word a status rather
// than come out blank.
func TestDispatchWordsAPlanWithNoLaneNames(t *testing.T) {
	plan := herdr.Plan{Key: "TKT-9", SourceRole: "todo", TargetRole: "doing"}
	rep := dispatchReport{plan: plan, res: herdr.DispatchResult{
		Created: true, Started: true, Prompted: true,
		WorktreePath: "/wt/x", AgentName: "tkt-9",
	}, laneMoved: true}
	text, kind := dispatchStatusText(rep)
	if kind != "" {
		t.Errorf("kind = %q", kind)
	}
	if !strings.Contains(text, "moved to doing") {
		t.Errorf("status = %q, want the role key as the lane fallback", text)
	}
	if !strings.Contains(dispatchCommentBody(rep), "todo → doing") {
		t.Errorf("comment = %q", dispatchCommentBody(rep))
	}
}

// A stage message arriving after the run was torn down changes nothing. Bubble
// Tea will not do this, but a status-expiry tick landing between the last two
// stages is the shape that would, and a nil-source panic here would take the
// whole board down.
func TestDispatchIgnoresStaleStageMessages(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	for _, msg := range []tea.Msg{
		dispatchStageMsg{seq: herdr.NewSequence(herdr.Plan{Key: "TKT-1"}, herdr.PreflightResult{})},
		dispatchLaneMsg{},
		dispatchDoneMsg{},
	} {
		var cmd tea.Cmd
		m, cmd = update(m, msg)
		if cmd != nil {
			t.Errorf("%T produced a command on an idle board", msg)
		}
		if m.modal != nil {
			t.Errorf("%T opened a %T", msg, m.modal)
		}
		if m.status != "" {
			t.Errorf("%T set the status to %q", msg, m.status)
		}
	}
}

// ---- one dispatch at a time ----
//
// The reachable chain this section exists for, found in review: press `n`
// (asynchronous — the modal stays nil while `tkt cfg issue_types` runs), press
// `D`, press enter, and while the sequence is cutting a worktree the pending
// issueTypesMsg lands and REPLACES the progress dialog. esc then nils it, the
// board takes keys again, and `D` works — because m.dispatching covers only the
// ~2s preparation and PickPane finds no agent yet, stage 1 having up to 15s to
// run. If run 1's worktree.create had landed, run 2 would worktree.open into
// the same pane, hit agent_name_taken, rename, and start a SECOND agent on one
// branch.
//
// So m.modal is not a lock, and three things make it safe: the key refuses
// while a run is in flight, the confirm refuses too, and an asynchronous modal
// declines to displace a running progress dialog.

// startDispatchRun gets a dispatch as far as "stage 1 in flight" and hands back
// the command that would finish it.
func startDispatchRun(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	m = pressD(t, m)
	m, cmd := update(m, key("enter"))
	res, ok := cmd().(dispatchResultMsg)
	if !ok || !res.confirmed {
		t.Fatalf("enter produced %+v", cmd())
	}
	m, next := update(m, res)
	if !m.dispatchBusy() {
		t.Fatal("the confirm did not start a run")
	}
	if next == nil {
		t.Fatal("the confirm started no work")
	}
	return m, next
}

// D is refused while a run is in flight, even with no dialog in the way.
func TestDispatchRefusesASecondDispatchWhileOneIsRunning(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	m, next := startDispatchRun(t, m)

	// Take the dialog out of the way, which is exactly what an asynchronous
	// modal landing and then being escaped does. The board is now taking keys
	// with a dispatch still in flight.
	m.modal = nil
	before := len(src.calls)

	m, _ = update(m, key("D"))
	if m.status != "Dispatching TKT-1 — wait for it to finish" || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
	// Whether work started is read off m.dispatching, never by running the
	// returned command: a refusal's command is the status-expiry timer, and
	// calling it would sit on a real six-second clock.
	if m.dispatching {
		t.Fatal("the refused key still started a preparation")
	}
	if m.modal != nil {
		t.Fatalf("the refused key opened a %T", m.modal)
	}
	if len(src.calls) != before {
		t.Fatalf("the refused key made herdr calls %v", src.calls[before:])
	}

	// And run 1 still finishes, exactly once.
	m = runDispatch(t, m, next)
	assertHerdrCalls(t, src, "worktree.list", "worktree.create", "agent.start", "agent.prompt")
	if len(src.createParams) != 1 {
		t.Fatalf("worktree.create ran %d times", len(src.createParams))
	}
	if n := countCalls(cr, "transition"); n != 1 {
		t.Fatalf("tkt transition ran %d times, want 1", n)
	}
}

// And the confirm refuses too, not only the key. A dispatchResultMsg arriving
// while a run is going would otherwise overwrite that run's source, sequence
// and report outright.
func TestDispatchConfirmRefusesWhileARunIsInFlight(t *testing.T) {
	m, src, _ := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	m, next := startDispatchRun(t, m)

	running, ok := m.modal.(dispatchModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	run := m.dispatchRun
	before := len(src.calls)

	// A second confirm, for a different ticket, delivered straight to the board.
	other := herdr.BuildPlan(herdr.PlanInput{
		Key: "TKT-9", Summary: "another", Dir: testDispatchDir,
		BranchFmt: "feature/{key-lower}-{slug}", Base: "main",
		SourceRole: "todo", TargetRole: "done", AgentKind: "claude",
	})
	m, _ = update(m, dispatchResultMsg{plan: other, confirmed: true})

	if m.dispatchRun != run {
		t.Fatalf("the second confirm started run %d, superseding %d", m.dispatchRun, run)
	}
	if m.dispatchRep.plan.Key != "TKT-1" {
		t.Fatalf("the second confirm clobbered the report: now %q", m.dispatchRep.plan.Key)
	}
	if got, ok := m.modal.(dispatchModal); !ok || got.progress != running.progress {
		t.Fatalf("the second confirm replaced the running dialog (%T)", m.modal)
	}
	if !strings.Contains(m.status, "Dispatching TKT-1") {
		t.Errorf("status = %q, want the refusal", m.status)
	}
	// The refusal's own command is the status timer, so the proof that no
	// second sequence started is that herdr was not called and the run stamp
	// did not move — both asserted here, not by running it.
	if len(src.calls) != before {
		t.Fatalf("the refused confirm made herdr calls %v", src.calls[before:])
	}

	m = runDispatch(t, m, next)
	if len(src.createParams) != 1 || src.createParams[0].Branch != "feature/tkt-1-first-thing" {
		t.Fatalf("worktree.create calls = %+v", src.createParams)
	}
}

// The chain in full, as a test: an asynchronous modal landing mid-sequence
// cannot displace the progress dialog, and D after it is still refused.
//
// This one fails without the setModal guard: the createModal replaces the
// progress dialog, and from there the board is back on the keyboard.
func TestDispatchProgressDialogSurvivesAnAsyncModal(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.Msg
	}{
		{"an issue-type fetch for n", issueTypesMsg{types: []string{"Story"}, priorities: []string{"High"}}},
		{"a ticket fetch for v", ticketMsg{ticket: model.Ticket{"key": "TKT-2", "summary": "x"}, purpose: "view"}},
		{"a ticket fetch for e", ticketMsg{ticket: model.Ticket{"key": "TKT-2", "summary": "x"}, purpose: "edit"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, src, cr := dispatchBoard(t)
			src.create, src.start = createdWorktree(), startedAgent()
			m, next := startDispatchRun(t, m)
			running := m.modal.(dispatchModal)

			m, _ = update(m, c.msg)

			got, ok := m.modal.(dispatchModal)
			if !ok {
				t.Fatalf("%T displaced the progress dialog with a %T", c.msg, m.modal)
			}
			if !got.running() || got.progress != running.progress {
				t.Fatalf("the progress dialog changed: %q → %q", running.progress, got.progress)
			}
			if !strings.Contains(m.status, "Dispatching TKT-1") {
				t.Errorf("status = %q, want it to say why the dialog did not open", m.status)
			}

			// The dialog is still swallowing keys, and D is refused anyway.
			herdrBefore := len(src.calls)
			m, _ = update(m, key("D"))
			if m.dispatching {
				t.Fatal("D started a second preparation")
			}
			if len(src.calls) != herdrBefore {
				t.Fatalf("D made herdr calls %v", src.calls[herdrBefore:])
			}

			m = runDispatch(t, m, next)
			assertHerdrCalls(t, src, "worktree.list", "worktree.create", "agent.start", "agent.prompt")
			if len(src.createParams) != 1 {
				t.Fatalf("worktree.create ran %d times", len(src.createParams))
			}
			if n := countCalls(cr, "comment"); n != 1 {
				t.Fatalf("tkt comment ran %d times, want 1", n)
			}
			// And the dialog it declined to open is openable once the dispatch
			// is over, so nothing was lost but a keystroke.
			m, _ = update(m, c.msg)
			if _, still := m.modal.(dispatchModal); still || m.modal == nil {
				t.Fatalf("after the dispatch the modal is %T", m.modal)
			}
		})
	}
}

// A message from a superseded run is inert. Without the run stamp, run 1's
// in-flight stage message would be applied on top of run 2's state, producing a
// report that describes neither dispatch.
func TestDispatchDropsMessagesFromASupersededRun(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()

	// Run 1, all the way through.
	m = dispatchOnce(t, m)
	if m.dispatchRun != 1 || m.dispatchBusy() {
		t.Fatalf("after run 1: run=%d busy=%v", m.dispatchRun, m.dispatchBusy())
	}

	// Run 2, stopped with stage 1 in flight.
	m, next := startDispatchRun(t, m)
	if m.dispatchRun != 2 {
		t.Fatalf("run = %d, want 2", m.dispatchRun)
	}
	running := m.modal.(dispatchModal)
	callsBefore, tktBefore := len(src.calls), len(cr.calls)

	// Run 1's messages arrive late. Every one of them must do nothing: a
	// finished sequence would drive run 2 straight into its tkt writes, and a
	// done message would close run 2's dialog and report run 1's outcome twice.
	finished := herdr.NewSequence(herdr.Plan{Key: "TKT-1"}, herdr.PreflightResult{})
	for _, msg := range []tea.Msg{
		dispatchStageMsg{run: 1, seq: finished},
		dispatchLaneMsg{run: 1, skipped: true, why: "nonsense"},
		dispatchDoneMsg{run: 1},
	} {
		var cmd tea.Cmd
		m, cmd = update(m, msg)
		if cmd != nil {
			t.Fatalf("%T from run 1 produced %T", msg, cmd())
		}
		got, ok := m.modal.(dispatchModal)
		if !ok {
			t.Fatalf("%T from run 1 closed run 2's dialog (%T)", msg, m.modal)
		}
		if got.progress != running.progress {
			t.Fatalf("%T from run 1 moved run 2's dialog on to %q", msg, got.progress)
		}
		if m.dispatchRep.laneSkipped || m.dispatchRep.laneWhy != "" {
			t.Fatalf("%T from run 1 wrote into run 2's report: %+v", msg, m.dispatchRep)
		}
	}
	if len(src.calls) != callsBefore {
		t.Fatalf("run 1's late messages made herdr calls %v", src.calls[callsBefore:])
	}
	if len(cr.calls) != tktBefore {
		t.Fatalf("run 1's late messages ran tkt %v", cr.calls[tktBefore:])
	}

	// Run 2 still finishes normally, on its own stamp.
	m = runDispatch(t, m, next)
	if !strings.HasPrefix(m.status, "Dispatched TKT-1") || m.statusKind != "" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
	if n := countCalls(cr, "transition"); n != 2 {
		t.Fatalf("tkt transition ran %d times across two runs, want 2", n)
	}
}

// Two runs in a row each get their own stamp, so the stamp really identifies a
// run rather than being a constant that happens to match.
func TestDispatchStampsEachRun(t *testing.T) {
	m, src, _ := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	for want := 1; want <= 3; want++ {
		m = dispatchOnce(t, m)
		if m.dispatchRun != want {
			t.Fatalf("after dispatch %d the run stamp is %d", want, m.dispatchRun)
		}
		if m.dispatchBusy() {
			t.Fatalf("dispatch %d left the board busy", want)
		}
	}
}

// countCalls counts recorded tkt invocations of one verb.
func countCalls(cr *captureRunner, verb string) int {
	n := 0
	for _, call := range cr.calls {
		if len(call) > 0 && call[0] == verb {
			n++
		}
	}
	return n
}

// ---- an agent whose fate herdr never reported ----

// The one case where the header and the comment used to contradict each other:
// our own deadline cancelled agent.start, so we do not know whether herdr went
// on to start it. Asserting "not started" there is the guess that sends someone
// to dispatch a second agent onto the same branch.
func TestDispatchAgentStartWithNoAnswerAdmitsItMayHaveStarted(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	src.create = createdWorktree()
	src.startErrs = []error{context.DeadlineExceeded}

	m = dispatchOnce(t, m)

	assertHerdrCalls(t, src, "worktree.list", "worktree.create", "agent.start")
	for _, want := range []string{
		"TKT-1's worktree is ready at /wt/tkt-1",
		"agent.start did not answer",
		"the agent may or may not be running",
		"check the pane (o) before dispatching again",
		"Nothing was removed",
	} {
		if !strings.Contains(m.status, want) {
			t.Errorf("status = %q, want it to contain %q", m.status, want)
		}
	}
	// And above all it must NOT assert the agent did not start.
	if strings.Contains(m.status, "the agent did not start") {
		t.Errorf("status = %q asserts something we cannot know", m.status)
	}
	comment := cr.last("comment")
	if comment == nil {
		t.Fatal("no comment")
	}
	if !strings.Contains(comment[2], "may or may not have started (tkt-1)") {
		t.Errorf("the comment does not admit the uncertainty:\n%s", comment[2])
	}
	if strings.Contains(comment[2], "not started (tkt-1); the worktree") {
		t.Errorf("the comment contradicts its own header:\n%s", comment[2])
	}
	if cr.last("transition") != nil {
		t.Error("the lane moved on an agent whose state is unknown")
	}

	// A herdr error code, by contrast, means herdr decided not to: that one is
	// allowed to say so plainly.
	m2, src2, cr2 := dispatchBoard(t)
	src2.create = createdWorktree()
	src2.startErrs = []error{&herdr.APIError{Code: herdr.CodeInvalidAgentArgument, Message: "bad --flag"}}
	m2 = dispatchOnce(t, m2)
	if !strings.Contains(m2.status, "the agent did not start") {
		t.Errorf("status = %q, want the plain wording for a herdr refusal", m2.status)
	}
	if strings.Contains(m2.status, "may or may not") {
		t.Errorf("status = %q hedges a refusal herdr was explicit about", m2.status)
	}
	if c := cr2.last("comment"); c == nil || !strings.Contains(c[2], "not started (tkt-1); the worktree and its pane were left in place") {
		t.Errorf("comment = %v", c)
	}
}

// The worktree reply with no root pane never reached agent.start, so its comment
// must not read as an agent that herdr refused OR one that might be running.
func TestDispatchNoRootPaneSaysItNeverGotToTheAgent(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	wt := createdWorktree()
	wt.RootPane.PaneID = ""
	src.create = wt

	m = dispatchOnce(t, m)

	assertHerdrCalls(t, src, "worktree.list", "worktree.create")
	comment := cr.last("comment")
	if comment == nil {
		t.Fatal("no comment")
	}
	if !strings.Contains(comment[2], "not started: the dispatch stopped before agent.start") {
		t.Errorf("comment:\n%s", comment[2])
	}
	if strings.Contains(comment[2], "may or may not have started") {
		t.Errorf("the comment hedges an agent that was never asked for:\n%s", comment[2])
	}
	// The worktree exists, so nothing may claim otherwise.
	if strings.Contains(m.status, "Nothing was created") {
		t.Errorf("status = %q, but the worktree was created", m.status)
	}
	if !strings.Contains(m.status, "/wt/tkt-1 is still there") {
		t.Errorf("status = %q, want it to name the worktree left behind", m.status)
	}
}

// ---- the lane skip, and the filter that is not a transition ----

// A ticket missing from an UNFILTERED board is not in the source lane any more,
// whatever the plan says, so the lane is left alone rather than transitioned
// from a lane it has left.
func TestDispatchSkipsTheLaneWhenTheTicketLeavesAnUnfilteredBoard(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	m, next := startDispatchRun(t, m)

	m, _ = update(m, boardMsg{
		roles: []model.RolePair{{Role: "todo", Lane: "To Do"}, {Role: "done", Lane: "Done"}},
		columns: []model.Column{
			{Lane: "To Do", Role: "todo", Cards: []model.Card{{Key: "TKT-7", Summary: "something else"}}},
			{Lane: "Done", Role: "done"},
		},
	})
	m = runDispatch(t, m, next)

	if cr.last("transition") != nil {
		t.Error("a ticket that is not on an unfiltered board was transitioned anyway")
	}
	if !strings.Contains(m.status, "TKT-1 is no longer on the board") ||
		!strings.Contains(m.status, "the lane was left alone") {
		t.Errorf("status = %q", m.status)
	}
	if c := cr.last("comment"); c == nil || !strings.Contains(c[2], "left alone — TKT-1 is no longer on the board") {
		t.Errorf("comment = %v", c)
	}
	// It is still a dispatch: only the lane was left alone.
	if !strings.Contains(m.status, "Dispatched TKT-1") {
		t.Errorf("status = %q", m.status)
	}
}

// A hidden column is still a lane the ticket is in, so a card behind x must not
// read as "no longer on the board".
func TestDispatchLaneSkipLooksThroughHiddenColumns(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	m, next := startDispatchRun(t, m)

	// The To Do column is hidden, so it is in allColumns but not columns.
	m.hidden = map[string]bool{"todo": true}
	m.applyHidden()
	if len(m.columns) != 1 {
		t.Fatalf("setup: %d visible columns", len(m.columns))
	}
	m = runDispatch(t, m, next)

	if call := cr.last("transition"); !slices.Equal(call, []string{"transition", "TKT-1", "done"}) {
		t.Fatalf("tkt transition = %v, want the move to run: a hidden column is still a lane", call)
	}
	if strings.Contains(m.status, "no longer on the board") {
		t.Errorf("status = %q — the card is hidden, not gone", m.status)
	}
}

// The board reports and prompts the name herdr registered, not the one it asked
// for. `name` is a field of its own in herdr's agent_started reply, distinct
// from `agent` (the kind), and herdr is free to hand back something else.
func TestDispatchFollowsTheAgentNameHerdrReturns(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	src.create = createdWorktree()
	started := startedAgent()
	started.Agent.Name = "tkt-1-herdrs-choice"
	src.start = started

	m = dispatchOnce(t, m)

	if got := src.startParams[0].Name; got != "tkt-1" {
		t.Fatalf("agent.start asked for %q, want the planned name", got)
	}
	if got := src.promptParams[0].Target; got != "tkt-1-herdrs-choice" {
		t.Fatalf("agent.prompt targeted %q, want the name in herdr's reply", got)
	}
	if !strings.Contains(m.status, "tkt-1-herdrs-choice") {
		t.Errorf("status = %q, want it to name the agent that exists", m.status)
	}
	if c := cr.last("comment"); c == nil || !strings.Contains(c[2], "tkt-1-herdrs-choice (claude)") {
		t.Errorf("the comment records the wrong agent name: %v", c)
	}
}

// ---- a stale answer to a dialog whose run has started ----

// The twin of TestDispatchConfirmRefusesWhileARunIsInFlight, on the arm that
// was wrong: a CANCEL. It is reachable with two keystrokes, because `send` runs
// the answer as a command and dispatchModal.Update mutates nothing it is called
// on — so a dialog that has not yet been swapped for the progress display
// accepts enter and then esc, putting two dispatchResultMsgs in flight.
//
// The confirmed one starts the run and installs the progress display. The
// cancel used to nil it, and progressModal cannot restore a nil: the dispatch
// went on cutting a worktree behind a board showing nothing, with the ordinary
// modal keys reachable again and auto-refresh resumed.
func TestDispatchStaleCancelDoesNotEraseTheProgressDialog(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	m = pressD(t, m)

	// Both answers come out of the same dialog, as two keystrokes on it do.
	dialog := m.modal.(dispatchModal)
	_, confirmCmd := dialog.Update(key("enter"))
	_, cancelCmd := dialog.Update(key("esc"))
	if confirmCmd == nil || cancelCmd == nil {
		t.Fatal("the dialog answered one of the two keys with nothing")
	}
	confirm, ok := confirmCmd().(dispatchResultMsg)
	if !ok || !confirm.confirmed {
		t.Fatalf("enter produced %+v", confirmCmd())
	}
	cancel, ok := cancelCmd().(dispatchResultMsg)
	if !ok || cancel.confirmed {
		t.Fatalf("esc produced %+v", cancelCmd())
	}

	// The confirm lands first and the run starts.
	m, next := update(m, confirm)
	running, ok := m.modal.(dispatchModal)
	if !ok || !running.running() {
		t.Fatalf("modal = %T after the confirm", m.modal)
	}
	run := m.dispatchRun

	// Then the cancel lands, for a dialog that no longer exists.
	m, cmd := update(m, cancel)
	got, ok := m.modal.(dispatchModal)
	if !ok {
		t.Fatalf("a stale cancel erased the progress dialog (modal = %T)", m.modal)
	}
	if !got.running() || got.progress != running.progress {
		t.Fatalf("a stale cancel changed the dialog: %q → %q", running.progress, got.progress)
	}
	if m.dispatchRun != run || !m.dispatchBusy() {
		t.Fatalf("a stale cancel disturbed the run: run=%d busy=%v", m.dispatchRun, m.dispatchBusy())
	}
	// A cancel says nothing, stale or not.
	if m.status != "" {
		t.Errorf("a stale cancel set the status to %q", m.status)
	}
	if cmd != nil {
		t.Errorf("a stale cancel produced a %T", cmd())
	}

	// The dialog is therefore still swallowing keys — which is the property the
	// erasure destroyed. m and c would otherwise open dialogs the dispatch's
	// own completion then discards.
	for _, k := range []string{"m", "c", "D", "r", "q"} {
		var kc tea.Cmd
		m, kc = update(m, key(k))
		if kc != nil {
			t.Fatalf("%q reached the board mid-dispatch: %T", k, kc())
		}
		if _, still := m.modal.(dispatchModal); !still {
			t.Fatalf("%q opened a %T mid-dispatch", k, m.modal)
		}
	}

	// And the run finishes normally, once.
	m = runDispatch(t, m, next)
	assertHerdrCalls(t, src, "worktree.list", "worktree.create", "agent.start", "agent.prompt")
	if !strings.HasPrefix(m.status, "Dispatched TKT-1") {
		t.Fatalf("status = %q", m.status)
	}
	if n := countCalls(cr, "transition"); n != 1 {
		t.Fatalf("tkt transition ran %d times, want 1", n)
	}
}

// ---- $EDITOR must not land on top of a dispatch ----

// N and E are asynchronous like v and n, and this one does not open a modal at
// all: it suspends the whole TUI into $EDITOR. Landing mid-dispatch would put
// the editor over the progress display and overlap its `tkt apply` with the
// dispatch's own two writes.
func TestDispatchRefusesToLaunchTheEditorMidSequence(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	src.create, src.start = createdWorktree(), startedAgent()
	m, next := startDispatchRun(t, m)
	running := m.modal.(dispatchModal)
	tktBefore := len(cr.calls)

	// The returned command is not inspected: a refusal's command is the
	// status-expiry timer, and calling it would sit on a real six-second clock.
	// That the editor did not launch is read off the status and the fact that no
	// tkt ran, which is the observable effect anyway.
	m, _ = update(m, editorPrepMsg{key: "TKT-1", path: "/tmp/nope.md", isNew: false})
	if got, ok := m.modal.(dispatchModal); !ok || got.progress != running.progress {
		t.Fatalf("the refusal disturbed the dialog (%T)", m.modal)
	}
	if !strings.Contains(m.status, "Dispatching TKT-1") {
		t.Errorf("status = %q, want the refusal", m.status)
	}
	if len(cr.calls) != tktBefore {
		t.Fatalf("the refused editor ran tkt %v", cr.calls[tktBefore:])
	}

	m = runDispatch(t, m, next)
	assertHerdrCalls(t, src, "worktree.list", "worktree.create", "agent.start", "agent.prompt")
	if n := countCalls(cr, "apply"); n != 0 {
		t.Fatalf("tkt apply ran %d times during a dispatch", n)
	}
}
