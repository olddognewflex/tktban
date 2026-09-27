package herdr

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// recordingDispatch is a DispatchClient that records every call it was given,
// in order, with the exact params, and serves canned replies.
//
// The recording is the whole point. A dispatch is five herdr calls whose
// parameter names are the thing that rots on a herdr upgrade, and whose ORDER
// is the thing that matters when one of them fails — so the tests assert the
// ordered method sequence and the params of each call, rather than only the
// value that came back.
type recordingDispatch struct {
	calls []recordedCall

	list    WorktreeListResult
	listErr error

	create    WorktreeResult
	createErr error

	open    WorktreeResult
	openErr error

	// startErrs is served one per agent.start call; a short slice means every
	// later call succeeds. start is what a successful call replies.
	startErrs []error
	start     AgentStartResult

	// promptErrs is served one per agent.prompt call, oldest first; a short
	// slice means every later call succeeds. promptErr is the simpler form,
	// applied to every call.
	promptErrs []error
	promptErr  error
}

// recordedCall is one herdr call: the method name as it goes on the wire, and
// the params struct it was given.
type recordedCall struct {
	method string
	params any
}

func (f *recordingDispatch) WorktreeList(_ context.Context, cwd string) (WorktreeListResult, error) {
	f.calls = append(f.calls, recordedCall{"worktree.list", WorktreeListParams{Cwd: cwd}})
	return f.list, f.listErr
}

func (f *recordingDispatch) WorktreeCreate(_ context.Context, p WorktreeCreateParams) (WorktreeResult, error) {
	f.calls = append(f.calls, recordedCall{"worktree.create", p})
	return f.create, f.createErr
}

func (f *recordingDispatch) WorktreeOpen(_ context.Context, p WorktreeOpenParams) (WorktreeResult, error) {
	f.calls = append(f.calls, recordedCall{"worktree.open", p})
	return f.open, f.openErr
}

func (f *recordingDispatch) AgentStart(_ context.Context, p AgentStartParams) (AgentStartResult, error) {
	n := 0
	for _, c := range f.calls {
		if c.method == "agent.start" {
			n++
		}
	}
	f.calls = append(f.calls, recordedCall{"agent.start", p})
	if n < len(f.startErrs) && f.startErrs[n] != nil {
		return AgentStartResult{}, f.startErrs[n]
	}
	return f.start, nil
}

func (f *recordingDispatch) AgentPrompt(_ context.Context, p AgentPromptParams) error {
	n := 0
	for _, c := range f.calls {
		if c.method == "agent.prompt" {
			n++
		}
	}
	f.calls = append(f.calls, recordedCall{"agent.prompt", p})
	if n < len(f.promptErrs) {
		return f.promptErrs[n]
	}
	return f.promptErr
}

// methods is the ordered method sequence a dispatch produced.
func (f *recordingDispatch) methods() []string {
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.method
	}
	return out
}

// only returns the params of the single call to method, failing when there was
// not exactly one.
func (f *recordingDispatch) only(t *testing.T, method string) any {
	t.Helper()
	var found []any
	for _, c := range f.calls {
		if c.method == method {
			found = append(found, c.params)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s was called %d times, want exactly once (sequence %v)", method, len(found), f.methods())
	}
	return found[0]
}

// recordingSleep is an injected Sleeper that records what it was asked to wait
// for and returns at once. No test here spends a single millisecond of the
// retry budget.
type recordingSleep struct {
	waits []time.Duration
	err   error // what to return instead of waiting, e.g. a dead context
}

func (s *recordingSleep) sleep(_ context.Context, d time.Duration) error {
	s.waits = append(s.waits, d)
	return s.err
}

// runSequence drives a whole dispatch to StageDone, one Next per stage, and
// returns the result and the stages it ran through in order.
//
// The loop is three lines because that is all the board's staging is too: the
// board runs exactly this, one iteration per tea.Cmd, so each stage gets its
// own bounded context and the dialog can say which one is running.
func runSequence(t *testing.T, c DispatchClient, plan Plan, pre PreflightResult, sleep Sleeper) (DispatchResult, []Stage) {
	t.Helper()
	// A backstop, not a budget. The board gives each stage its own deadline
	// (dispatchStageBudget); this one only exists so that a sequence which has
	// been broken into looping — or a socket peer that stops answering — fails
	// the test instead of hanging it. A passing run never comes near it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := NewSequence(plan, pre)
	var ran []Stage
	for st := s.Stage(); st != StageDone; st = s.Stage() {
		ran = append(ran, st)
		s = s.Next(ctx, c, sleep)
		if len(ran) > 8 {
			t.Fatalf("the sequence did not finish: stages %v", ran)
		}
	}
	// Whatever happened, no failure path may have tidied up after itself.
	assertNothingDestroyed(t, c)
	return s.Res, ran
}

// destructiveMethods are the herdr calls a dispatch must never make. A
// half-finished dispatch leaves a worktree that may hold a checkout, a stash
// or an edited file, and a board does not get to decide those are disposable.
var destructiveMethods = []string{
	"worktree.remove", "worktree.prune",
	"pane.close", "workspace.close", "tab.close",
	"agent.stop", "agent.release",
}

// dispatchMethods are the only herdr calls a dispatch may make at all.
var dispatchMethods = []string{
	"worktree.list", "worktree.create", "worktree.open",
	"agent.start", "agent.prompt",
}

// forbiddenCall returns the first recorded call a dispatch must never have
// made, or "". An allowlist, not a denylist: a method nobody thought to forbid
// is caught too, which is exactly when it matters.
func forbiddenCall(f *recordingDispatch) string {
	for _, call := range f.calls {
		if !slices.Contains(dispatchMethods, call.method) {
			return call.method
		}
	}
	return ""
}

// assertNothingDestroyed is the standing policy over a recorded sequence: not
// one case, every case. Every test that drives a sequence runs it, on whatever
// the sequence happened to do.
func assertNothingDestroyed(t *testing.T, c DispatchClient) {
	t.Helper()
	f, ok := c.(*recordingDispatch)
	if !ok {
		return
	}
	if bad := forbiddenCall(f); bad != "" {
		t.Fatalf("the sequence called %s (%v); a dispatch must never undo its own work",
			bad, f.methods())
	}
}

// The allowlist has to be able to fail, or it is checking nothing.
func TestForbiddenCallCatchesEveryUndoMethod(t *testing.T) {
	for _, method := range append(append([]string(nil), destructiveMethods...), "pane.focus") {
		f := &recordingDispatch{calls: []recordedCall{
			{"worktree.create", nil}, {method, nil},
		}}
		if got := forbiddenCall(f); got != method {
			t.Errorf("%s was not caught by the policy check (got %q)", method, got)
		}
	}
	clean := &recordingDispatch{calls: []recordedCall{
		{"worktree.list", nil}, {"worktree.create", nil}, {"worktree.open", nil},
		{"agent.start", nil}, {"agent.prompt", nil},
	}}
	if got := forbiddenCall(clean); got != "" {
		t.Errorf("a clean sequence was reported as destructive: %q", got)
	}
}

// The structural half of the same policy: DispatchClient itself has no way to
// remove or close anything, so no failure path can reach one even by mistake.
// This is what stops the next person widening the interface "just for
// cleanup".
func TestDispatchClientCannotDestroyAnything(t *testing.T) {
	typ := reflect.TypeOf((*DispatchClient)(nil)).Elem()
	want := map[string]bool{
		"WorktreeList": true, "WorktreeCreate": true, "WorktreeOpen": true,
		"AgentStart": true, "AgentPrompt": true,
	}
	got := map[string]bool{}
	for i := range typ.NumMethod() {
		got[typ.Method(i).Name] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DispatchClient methods = %v, want exactly %v", keysOf(got), keysOf(want))
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// testPlan is the plan every sequence test dispatches.
func testPlan() Plan {
	return BuildPlan(PlanInput{
		Key:        "TKB-25",
		Summary:    "dispatch a ticket",
		Dir:        "/src/tktban",
		Repo:       "olddognewflex/tktban",
		BranchFmt:  "feature/{key-lower}-{slug}",
		Base:       "main",
		SourceRole: "todo",
		SourceLane: "To Do",
		TargetRole: "in_progress",
		TargetLane: "In Progress",
		AgentKind:  "claude",
		AgentArgs:  []string{"--permission-mode", "plan"},
	})
}

// createdWorktree is herdr's worktree_created reply for that plan.
func createdWorktree() WorktreeResult {
	return WorktreeResult{
		Workspace: WorkspaceInfo{WorkspaceID: "wE", Label: "TKB-25", Number: 5},
		Tab:       TabInfo{TabID: "wE:t1", WorkspaceID: "wE", Label: "TKB-25"},
		RootPane: PaneInfo{
			PaneID: "wE:p1", WorkspaceID: "wE", TabID: "wE:t1",
			TerminalID: "term_1", Cwd: "/wt/tkb-25",
		},
		Worktree: WorktreeInfo{
			Path: "/wt/tkb-25", Branch: "feature/tkb-25-dispatch-a-ticket",
			Label: "TKB-25", OpenWorkspaceID: "wE", IsLinkedWorktree: true,
		},
	}
}

// startedAgent is herdr's agent_started reply. It carries no `name` on purpose:
// the tests that care about the name set it explicitly, so the ones that do not
// are exercising the fallback to the name we sent.
func startedAgent() AgentStartResult {
	agent := "claude"
	return AgentStartResult{
		Agent: Agent{PaneID: "wE:p1", WorkspaceID: "wE", TabID: "wE:t1", Agent: &agent, Status: StatusIdle},
		Argv:  []string{"claude", "--permission-mode", "plan"},
	}
}

// The happy path, asserted as the wire traffic it is: the exact ordered method
// sequence, and the exact params of every call.
//
// Every one of these params is a decision this PR made, and every one of them
// is invisible in the result: no path (herdr places the worktree), no
// trust_repository (trusting a repo is a write), focus false (a dispatch does
// not steal the screen), timeout_ms 20000 (so herdr's timeout fires before
// ours), and no wait on the prompt (a board must not block on an agent).
func TestSequenceHappyPathCallsAndParams(t *testing.T) {
	f := &recordingDispatch{create: createdWorktree(), start: startedAgent()}
	plan := testPlan()
	sl := &recordingSleep{}

	res, stages := runSequence(t, f, plan, PreflightResult{RepoRoot: "/src/tktban"}, sl.sleep)

	if want := []string{"worktree.create", "agent.start", "agent.prompt"}; !slices.Equal(f.methods(), want) {
		t.Fatalf("method sequence = %v, want %v", f.methods(), want)
	}
	if want := []Stage{StageWorktree, StageAgent, StagePrompt}; !slices.Equal(stages, want) {
		t.Errorf("stages = %v, want %v", stages, want)
	}
	if len(sl.waits) != 0 {
		t.Errorf("a happy path waited %v", sl.waits)
	}

	wantCreate := WorktreeCreateParams{
		Cwd:    "/src/tktban",
		Branch: "feature/tkb-25-dispatch-a-ticket",
		Base:   "main",
		Label:  "TKB-25",
		Focus:  false,
	}
	if got := f.only(t, "worktree.create").(WorktreeCreateParams); got != wantCreate {
		t.Errorf("worktree.create params = %+v, want %+v", got, wantCreate)
	}
	// Spelled out again, because a zero value that happens to match is not the
	// same as a field nobody may set: these two are refusals, not defaults.
	if got := f.only(t, "worktree.create").(WorktreeCreateParams); got.Path != "" || got.TrustRepository {
		t.Errorf("worktree.create sent path=%q trust_repository=%v; herdr places the worktree and trusting is a write",
			got.Path, got.TrustRepository)
	}

	gotStart := f.only(t, "agent.start").(AgentStartParams)
	wantStart := AgentStartParams{
		Name: "tkb-25", Kind: "claude", PaneID: "wE:p1",
		Args: []string{"--permission-mode", "plan"}, TimeoutMS: 20000,
	}
	if gotStart.Name != wantStart.Name || gotStart.Kind != wantStart.Kind ||
		gotStart.PaneID != wantStart.PaneID || gotStart.TimeoutMS != wantStart.TimeoutMS ||
		!slices.Equal(gotStart.Args, wantStart.Args) {
		t.Errorf("agent.start params = %+v, want %+v", gotStart, wantStart)
	}

	gotPrompt := f.only(t, "agent.prompt").(AgentPromptParams)
	if gotPrompt.Target != "tkb-25" || gotPrompt.Text != plan.Prompt {
		t.Errorf("agent.prompt params = %+v, want target tkb-25 and the plan's prompt", gotPrompt)
	}

	if !res.Created || !res.Started || !res.Prompted || res.Err != nil || res.Failed != StageDone {
		t.Fatalf("result = %+v, want a complete dispatch", res)
	}
	if res.Reused || res.AlreadyOpen {
		t.Errorf("a fresh create reported a reuse: %+v", res)
	}
	// Every field the comment body and the status line are built from.
	if res.WorktreePath != "/wt/tkb-25" || res.WorkspaceID != "wE" ||
		res.TabID != "wE:t1" || res.PaneID != "wE:p1" || res.AgentName != "tkb-25" {
		t.Errorf("result = %+v, want the worktree, workspace, tab, pane and agent name", res)
	}
	if !slices.Equal(res.Argv, []string{"claude", "--permission-mode", "plan"}) {
		t.Errorf("argv = %v, want herdr's own", res.Argv)
	}
	if res.Start.Attempts != 1 || res.Start.Busy != 0 || res.Start.Renamed != 0 {
		t.Errorf("start stats = %+v, want one clean attempt", res.Start)
	}
}

// The reuse path: the preflight already saw the branch checked out, so the
// worktree is opened and never cut a second time. worktree.open takes no base
// — there is no branch to cut — and the same no-path/no-trust rules hold.
func TestSequenceReusesAnExistingWorktree(t *testing.T) {
	open := createdWorktree()
	open.AlreadyOpen = true
	f := &recordingDispatch{open: open, start: startedAgent()}
	plan := testPlan()

	res, _ := runSequence(t, f, plan, PreflightResult{
		RepoRoot:             "/src/tktban",
		ExistingWorktreePath: "/wt/tkb-25",
		ExistingWorkspaceID:  "wE",
	}, (&recordingSleep{}).sleep)

	if want := []string{"worktree.open", "agent.start", "agent.prompt"}; !slices.Equal(f.methods(), want) {
		t.Fatalf("method sequence = %v, want %v", f.methods(), want)
	}
	for _, c := range f.calls {
		if c.method == "worktree.create" {
			t.Fatal("a branch that already has a worktree must not be cut again")
		}
	}
	wantOpen := WorktreeOpenParams{
		Cwd: "/src/tktban", Branch: "feature/tkb-25-dispatch-a-ticket", Focus: false,
	}
	if got := f.only(t, "worktree.open").(WorktreeOpenParams); got != wantOpen {
		t.Errorf("worktree.open params = %+v, want exactly %+v (no base, no path, no trust)", got, wantOpen)
	}
	if !res.Reused || !res.AlreadyOpen || !res.Prompted {
		t.Fatalf("result = %+v, want a reused, already-open worktree and a prompted agent", res)
	}
}

// ---- the failure matrix ----
//
// One table, every row, each asserting what did AND did not happen. The rows
// are the same ones the README lists and the board's status line words.

func TestSequenceFailureMatrix(t *testing.T) {
	createFail := &APIError{Code: CodeWorktreeCreateFailed, Message: "fatal: invalid reference: main"}
	trustFail := &APIError{Code: CodeWorkspaceTrustBlocked, Message: "repository is not trusted"}
	blocked := &APIError{Code: CodeAgentBlocked, Message: "agent is blocked"}
	badArg := &APIError{Code: CodeInvalidAgentArgument, Message: "bad --flag"}
	gone := errors.New("dial unix /run/herdr.sock: connect: no such file or directory")

	cases := []struct {
		name string
		// setup configures the fake and the preflight.
		setup func(f *recordingDispatch) PreflightResult
		// wantMethods is the exact ordered method sequence.
		wantMethods []string
		wantStages  []Stage
		wantFailed  Stage
		wantErr     error
		wantCode    string
		// what the caller must be able to read off the result.
		wantCreated  bool
		wantStarted  bool
		wantPrompted bool
	}{
		{
			name: "worktree.create fails: nothing else is even attempted",
			setup: func(f *recordingDispatch) PreflightResult {
				f.createErr = createFail
				return PreflightResult{RepoRoot: "/src/tktban"}
			},
			wantMethods: []string{"worktree.create"},
			wantStages:  []Stage{StageWorktree},
			wantFailed:  StageWorktree,
			wantErr:     createFail,
			wantCode:    CodeWorktreeCreateFailed,
		},
		{
			name: "the repository is not trusted: a refusal, not something to route around",
			setup: func(f *recordingDispatch) PreflightResult {
				f.createErr = trustFail
				return PreflightResult{RepoRoot: "/src/tktban"}
			},
			wantMethods: []string{"worktree.create"},
			wantStages:  []Stage{StageWorktree},
			wantFailed:  StageWorktree,
			wantErr:     trustFail,
			wantCode:    CodeWorkspaceTrustBlocked,
		},
		{
			name: "worktree.open fails on the reuse path",
			setup: func(f *recordingDispatch) PreflightResult {
				f.openErr = &APIError{Code: CodeWorktreeNotFound, Message: "no such worktree"}
				return PreflightResult{ExistingWorktreePath: "/wt/tkb-25"}
			},
			wantMethods: []string{"worktree.open"},
			wantStages:  []Stage{StageWorktree},
			wantFailed:  StageWorktree,
			wantCode:    CodeWorktreeNotFound,
		},
		{
			name: "a reply with no root pane is caught here, not three seconds later",
			setup: func(f *recordingDispatch) PreflightResult {
				wt := createdWorktree()
				wt.RootPane.PaneID = ""
				f.create = wt
				return PreflightResult{}
			},
			wantMethods: []string{"worktree.create"},
			wantStages:  []Stage{StageWorktree},
			wantFailed:  StageWorktree,
			wantCode:    "", // herdr broke its own contract; there is no code
			// The worktree IS there — herdr made it and then answered without a
			// pane — so the result says so and nobody may word this as
			// "nothing was created".
			wantCreated: true,
		},
		{
			name: "agent.start fails after the worktree exists: no rollback",
			setup: func(f *recordingDispatch) PreflightResult {
				f.create = createdWorktree()
				f.startErrs = []error{badArg}
				return PreflightResult{}
			},
			wantMethods: []string{"worktree.create", "agent.start"},
			wantStages:  []Stage{StageWorktree, StageAgent},
			wantFailed:  StageAgent,
			wantErr:     badArg,
			wantCode:    CodeInvalidAgentArgument,
			wantCreated: true,
		},
		{
			name: "agent.prompt fails: the agent is dispatched all the same",
			setup: func(f *recordingDispatch) PreflightResult {
				f.create = createdWorktree()
				f.start = startedAgent()
				f.promptErr = blocked
				return PreflightResult{}
			},
			wantMethods: []string{"worktree.create", "agent.start", "agent.prompt"},
			wantStages:  []Stage{StageWorktree, StageAgent, StagePrompt},
			wantFailed:  StagePrompt,
			wantErr:     blocked,
			wantCode:    CodeAgentBlocked,
			wantCreated: true,
			wantStarted: true,
		},
		{
			name: "herdr is unreachable: no code, and the worktree step is not retried",
			setup: func(f *recordingDispatch) PreflightResult {
				f.createErr = gone
				return PreflightResult{}
			},
			wantMethods: []string{"worktree.create"},
			wantStages:  []Stage{StageWorktree},
			wantFailed:  StageWorktree,
			wantErr:     gone,
			wantCode:    "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &recordingDispatch{}
			pre := c.setup(f)
			sl := &recordingSleep{}
			res, stages := runSequence(t, f, testPlan(), pre, sl.sleep)

			if !slices.Equal(f.methods(), c.wantMethods) {
				t.Errorf("method sequence = %v, want %v", f.methods(), c.wantMethods)
			}
			if !slices.Equal(stages, c.wantStages) {
				t.Errorf("stages = %v, want %v", stages, c.wantStages)
			}
			if res.Failed != c.wantFailed {
				t.Errorf("failed stage = %v, want %v", res.Failed, c.wantFailed)
			}
			if res.Err == nil {
				t.Fatalf("result = %+v, want an error", res)
			}
			if c.wantErr != nil && !errors.Is(res.Err, c.wantErr) {
				t.Errorf("err = %v, want %v", res.Err, c.wantErr)
			}
			if got := ErrorCode(res.Err); got != c.wantCode {
				t.Errorf("error code = %q, want %q", got, c.wantCode)
			}
			// What the comment and the status line read off it.
			if res.Created != c.wantCreated {
				t.Errorf("Created = %v, want %v", res.Created, c.wantCreated)
			}
			if res.Started != c.wantStarted {
				t.Errorf("Started = %v, want %v", res.Started, c.wantStarted)
			}
			if res.Prompted != c.wantPrompted {
				t.Errorf("Prompted = %v, want %v", res.Prompted, c.wantPrompted)
			}
			if c.wantCreated && res.WorktreePath == "" {
				t.Error("a worktree that exists must be named on the result, or nobody can be told about it")
			}
		})
	}
}

// A failed sequence stays failed: Next on it is a no-op. That is what makes
// "NEVER auto-retry the worktree step" structural rather than a convention —
// a caller that loops until Stage() is StageDone cannot loop at all.
func TestSequenceNeverRetriesAFailedStep(t *testing.T) {
	f := &recordingDispatch{createErr: &APIError{Code: CodeWorktreeCreateFailed, Message: "boom"}}
	s := NewSequence(testPlan(), PreflightResult{})
	s = s.Next(context.Background(), f, (&recordingSleep{}).sleep)
	if s.Stage() != StageDone {
		t.Fatalf("stage after a failure = %v, want StageDone", s.Stage())
	}
	for range 5 {
		s = s.Next(context.Background(), f, (&recordingSleep{}).sleep)
	}
	if want := []string{"worktree.create"}; !slices.Equal(f.methods(), want) {
		t.Fatalf("method sequence = %v, want %v — a second create would leave a second worktree", f.methods(), want)
	}
	assertNothingDestroyed(t, f)
}

// A finished sequence is equally inert.
func TestSequenceNextAfterDoneChangesNothing(t *testing.T) {
	f := &recordingDispatch{create: createdWorktree(), start: startedAgent()}
	s := NewSequence(testPlan(), PreflightResult{})
	for range 3 {
		s = s.Next(context.Background(), f, (&recordingSleep{}).sleep)
	}
	if s.Stage() != StageDone || !s.Res.Prompted {
		t.Fatalf("result = %+v", s.Res)
	}
	before := len(f.calls)
	for range 3 {
		s = s.Next(context.Background(), f, (&recordingSleep{}).sleep)
	}
	if len(f.calls) != before {
		t.Fatalf("a finished sequence made %v", f.methods()[before:])
	}
}

// ---- the retry budget ----

// The expected failure right after a worktree is created: the pane is there
// but its shell is not at a prompt yet. The delays are asserted exactly,
// because the schedule is the decision — a fast shell must cost 100ms, not a
// flat second.
func TestStartAgentRetriesOnPaneBusy(t *testing.T) {
	busy := &APIError{Code: CodeAgentPaneBusy, Message: "pane is not at a shell prompt"}
	f := &recordingDispatch{
		startErrs: []error{busy, busy, busy},
		start:     startedAgent(),
	}
	sl := &recordingSleep{}

	res, stats, err := StartAgentWithRetry(context.Background(), f, testPlan(), "wE:p1", sl.sleep)
	if err != nil {
		t.Fatalf("three busies then a success must succeed: %v", err)
	}
	if len(f.calls) != 4 {
		t.Fatalf("agent.start calls = %d (%v), want 4", len(f.calls), f.methods())
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	if !slices.Equal(sl.waits, want) {
		t.Fatalf("waits = %v, want %v", sl.waits, want)
	}
	if stats.Attempts != 4 || stats.Busy != 3 || stats.Renamed != 0 {
		t.Errorf("stats = %+v", stats)
	}
	if stats.Waited != 700*time.Millisecond {
		t.Errorf("total wait = %v, want 700ms", stats.Waited)
	}
	if stats.Name != "tkb-25" {
		t.Errorf("name = %q, want the first candidate: a busy pane is not a name collision", stats.Name)
	}
	if len(res.Argv) == 0 {
		t.Error("the successful start dropped herdr's argv")
	}
	// Every retry asks for the same thing. A retry that quietly changed the
	// name or the pane would be a different call, not a retry.
	for i, c := range f.calls {
		p := c.params.(AgentStartParams)
		if p.Name != "tkb-25" || p.PaneID != "wE:p1" || p.TimeoutMS != AgentStartTimeoutMS {
			t.Errorf("attempt %d = %+v", i, p)
		}
	}
}

// The budget is finite, and running out is not a rollback: the worktree and
// its pane are still there for the person to use.
func TestStartAgentGivesUpAfterTheBudget(t *testing.T) {
	busy := &APIError{Code: CodeAgentPaneBusy, Message: "pane is not at a shell prompt"}
	f := &recordingDispatch{startErrs: []error{busy, busy, busy, busy, busy, busy, busy, busy}}
	sl := &recordingSleep{}

	_, stats, err := StartAgentWithRetry(context.Background(), f, testPlan(), "wE:p1", sl.sleep)
	if err == nil {
		t.Fatal("a pane that stays busy must give up, not keep trying")
	}
	if code := ErrorCode(err); code != CodeAgentPaneBusy {
		t.Errorf("error code = %q, want the last herdr reply's", code)
	}
	if len(f.calls) != 7 {
		t.Fatalf("agent.start calls = %d, want 7 (one attempt plus six retries)", len(f.calls))
	}
	want := []time.Duration{
		100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond,
		800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond,
	}
	if !slices.Equal(sl.waits, want) {
		t.Fatalf("waits = %v, want %v", sl.waits, want)
	}
	if stats.Attempts != 7 || stats.Busy != 7 {
		t.Errorf("stats = %+v", stats)
	}
	if stats.Waited != 6300*time.Millisecond {
		t.Errorf("total wait = %v, want 6.3s", stats.Waited)
	}
	assertNothingDestroyed(t, f)
}

// The case the live dispatch actually hit, replayed: four busy replies then a
// success on attempt five, 1.5s of waiting. It is here so that shortening the
// schedule back below the measured need fails a test rather than a dispatch.
func TestStartAgentSurvivesTheObservedSlowShell(t *testing.T) {
	busy := &APIError{Code: CodeAgentPaneBusy, Message: "pane is not at a shell prompt"}
	f := &recordingDispatch{
		startErrs: []error{busy, busy, busy, busy},
		start:     startedAgent(),
	}
	sl := &recordingSleep{}
	_, stats, err := StartAgentWithRetry(context.Background(), f, testPlan(), "wE:p1", sl.sleep)
	if err != nil {
		t.Fatalf("the observed slow shell must still be dispatchable: %v", err)
	}
	if stats.Attempts != 5 || stats.Busy != 4 {
		t.Fatalf("stats = %+v, want the observed 5 attempts / 4 busy", stats)
	}
	if stats.Waited != 1500*time.Millisecond {
		t.Fatalf("waited %v, want the observed 1.5s", stats.Waited)
	}
	// And there is real headroom left, not one slot.
	if left := len(agentRetryBackoff) - stats.Busy; left < 2 {
		t.Fatalf("only %d retry slots spare on the observed case", left)
	}
}

// A name collision means an earlier agent on this ticket is still live
// somewhere. The next candidate is tried once, out of the same budget, and a
// second collision stops rather than counting upwards forever.
func TestStartAgentRetriesOnceOnNameTaken(t *testing.T) {
	taken := &APIError{Code: CodeAgentNameTaken, Message: "agent name is in use"}
	f := &recordingDispatch{startErrs: []error{taken}, start: startedAgent()}
	sl := &recordingSleep{}

	_, stats, err := StartAgentWithRetry(context.Background(), f, testPlan(), "wE:p1", sl.sleep)
	if err != nil {
		t.Fatalf("one collision then a success must succeed: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("agent.start calls = %d, want 2", len(f.calls))
	}
	if got := f.calls[0].params.(AgentStartParams).Name; got != "tkb-25" {
		t.Errorf("first name = %q, want tkb-25", got)
	}
	if got := f.calls[1].params.(AgentStartParams).Name; got != "tkb-25-2" {
		t.Errorf("second name = %q, want the next candidate tkb-25-2", got)
	}
	// It shares the budget rather than getting one of its own: the wait is the
	// first slot of the same schedule.
	if want := []time.Duration{100 * time.Millisecond}; !slices.Equal(sl.waits, want) {
		t.Errorf("waits = %v, want %v", sl.waits, want)
	}
	if stats.Renamed != 1 || stats.Attempts != 2 || stats.Busy != 0 {
		t.Errorf("stats = %+v", stats)
	}
	if stats.Name != "tkb-25-2" {
		t.Fatalf("stats.Name = %q, want the name the agent actually started under", stats.Name)
	}
}

func TestStartAgentStopsAfterASecondNameCollision(t *testing.T) {
	taken := &APIError{Code: CodeAgentNameTaken, Message: "agent name is in use"}
	f := &recordingDispatch{startErrs: []error{taken, taken, taken, taken}}
	sl := &recordingSleep{}

	_, stats, err := StartAgentWithRetry(context.Background(), f, testPlan(), "wE:p1", sl.sleep)
	if code := ErrorCode(err); code != CodeAgentNameTaken {
		t.Fatalf("err = %v, want agent_name_taken", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("agent.start calls = %d, want 2: walking up the names would just make more of them", len(f.calls))
	}
	if stats.Renamed != 1 {
		t.Errorf("stats = %+v", stats)
	}
}

// A busy pane and a name collision share one budget, in whatever order they
// arrive.
func TestStartAgentSharesTheBudgetBetweenBusyAndTaken(t *testing.T) {
	busy := &APIError{Code: CodeAgentPaneBusy, Message: "busy"}
	taken := &APIError{Code: CodeAgentNameTaken, Message: "taken"}
	f := &recordingDispatch{startErrs: []error{busy, taken, busy}, start: startedAgent()}
	sl := &recordingSleep{}

	_, stats, err := StartAgentWithRetry(context.Background(), f, testPlan(), "wE:p1", sl.sleep)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 4 {
		t.Fatalf("agent.start calls = %d, want 4", len(f.calls))
	}
	names := make([]string, len(f.calls))
	for i, c := range f.calls {
		names[i] = c.params.(AgentStartParams).Name
	}
	if want := []string{"tkb-25", "tkb-25", "tkb-25-2", "tkb-25-2"}; !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	if !slices.Equal(sl.waits, want) {
		t.Errorf("waits = %v, want %v — the rename spends a slot of the same schedule", sl.waits, want)
	}
	if stats.Busy != 2 || stats.Renamed != 1 || stats.Attempts != 4 {
		t.Errorf("stats = %+v", stats)
	}
}

// Every other code returns at once. They will not come right by being asked
// again, and a dispatch that sat there retrying invalid_agent_name for three
// seconds would be worse than one that says so.
func TestStartAgentDoesNotRetryOtherCodes(t *testing.T) {
	for _, code := range []string{
		CodeInvalidAgentName, CodeInvalidAgentArgument, CodeAgentBlocked, CodePaneNotFound,
	} {
		f := &recordingDispatch{startErrs: []error{&APIError{Code: code, Message: "no"}}}
		sl := &recordingSleep{}
		_, stats, err := StartAgentWithRetry(context.Background(), f, testPlan(), "wE:p1", sl.sleep)
		if ErrorCode(err) != code {
			t.Errorf("%s: err = %v", code, err)
		}
		if len(f.calls) != 1 {
			t.Errorf("%s: agent.start calls = %d, want 1", code, len(f.calls))
		}
		if len(sl.waits) != 0 {
			t.Errorf("%s: waited %v before giving up", code, sl.waits)
		}
		if stats.Attempts != 1 {
			t.Errorf("%s: stats = %+v", code, stats)
		}
	}
	// A dial failure has no code at all and is not retried either.
	f := &recordingDispatch{startErrs: []error{errors.New("connection refused")}}
	if _, _, err := StartAgentWithRetry(context.Background(), f, testPlan(), "wE:p1", (&recordingSleep{}).sleep); err == nil {
		t.Fatal("a dial failure must be reported")
	}
	if len(f.calls) != 1 {
		t.Fatalf("agent.start calls = %d, want 1", len(f.calls))
	}
}

// A context that ends mid-backoff stops the retries and says both things: the
// deadline is why we stopped, the herdr code is what we last saw.
func TestStartAgentStopsWhenTheContextEndsMidBackoff(t *testing.T) {
	busy := &APIError{Code: CodeAgentPaneBusy, Message: "busy"}
	f := &recordingDispatch{startErrs: []error{busy, busy, busy, busy, busy, busy}}
	sl := &recordingSleep{err: context.DeadlineExceeded}

	_, stats, err := StartAgentWithRetry(context.Background(), f, testPlan(), "wE:p1", sl.sleep)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if !errors.Is(err, busy) {
		t.Errorf("err = %v, want the last herdr reply wrapped too", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("agent.start calls = %d, want 1: the budget ended with the context", len(f.calls))
	}
	if stats.Waited != 0 {
		t.Errorf("stats.Waited = %v, want 0: the wait never happened", stats.Waited)
	}
}

// A nil Sleeper is the real one. Nothing in the suite passes nil — that is the
// point of asserting it here rather than finding out in production.
func TestStartAgentDefaultsItsSleeper(t *testing.T) {
	f := &recordingDispatch{start: startedAgent()}
	if _, _, err := StartAgentWithRetry(context.Background(), f, testPlan(), "wE:p1", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v", f.methods())
	}
}

// A plan with no agent name (one built by hand rather than by BuildPlan) still
// gets a name herdr will accept.
func TestStartAgentFallsBackToTheGeneratedName(t *testing.T) {
	f := &recordingDispatch{start: startedAgent()}
	plan := testPlan()
	plan.AgentName = ""
	if _, stats, err := StartAgentWithRetry(context.Background(), f, plan, "wE:p1", nil); err != nil || stats.Name != "tkb-25" {
		t.Fatalf("stats = %+v err = %v", stats, err)
	}
}

// Stage names the herdr method it calls, because both the status line and the
// ticket comment name the step that failed and there must not be a second
// vocabulary for the same three calls.
func TestStageStrings(t *testing.T) {
	want := map[Stage]string{
		StageWorktree: "worktree.create",
		StageAgent:    "agent.start",
		StagePrompt:   "agent.prompt",
		StageDone:     "done",
		Stage(99):     "unknown",
	}
	for st, w := range want {
		if got := st.String(); got != w {
			t.Errorf("Stage(%d).String() = %q, want %q", st, got, w)
		}
	}
	// StageDone has to be the zero value, or DispatchResult.Failed would read
	// as a worktree failure on every successful dispatch.
	if StageDone != 0 {
		t.Fatalf("StageDone = %d, want the zero value", StageDone)
	}
	var zero DispatchResult
	if zero.Failed != StageDone {
		t.Fatalf("a zero DispatchResult reports %v as failed", zero.Failed)
	}
	// The working stages run in this order: Stage() walks it.
	if !(StageWorktree < StageAgent && StageAgent < StagePrompt) {
		t.Fatal("the stages are out of order")
	}
}

// The prompt targets the name herdr REGISTERED, not the one we asked for.
//
// herdr's agent_started reply carries `name` as a field of its own — distinct
// from `agent`, which is the kind — and it is not guaranteed to be the name
// that was sent. Prompting the sent name when herdr chose another is how a
// prompt lands in somebody else's pane, so the reply wins.
func TestSequencePromptTargetsTheNameHerdrRegistered(t *testing.T) {
	started := startedAgent()
	started.Agent.Name = "tkb-25-herdrs-choice"
	f := &recordingDispatch{create: createdWorktree(), start: started}

	res, _ := runSequence(t, f, testPlan(), PreflightResult{}, nil)
	if res.AgentName != "tkb-25-herdrs-choice" {
		t.Fatalf("result agent name = %q, want herdr's", res.AgentName)
	}
	got := f.only(t, "agent.prompt").(AgentPromptParams).Target
	if got != "tkb-25-herdrs-choice" {
		t.Fatalf("prompt target = %q, want the name in herdr's reply", got)
	}
	// And the name we sent is still recorded, because a dispatch that comes
	// back under a different name is worth being able to see.
	if res.Start.Name != "tkb-25" {
		t.Errorf("start stats name = %q, want the name that was sent", res.Start.Name)
	}
}

// A herdr that answers without a name falls back to the one that was sent,
// rather than prompting "".
func TestSequencePromptFallsBackToTheNameSent(t *testing.T) {
	f := &recordingDispatch{create: createdWorktree(), start: startedAgent()}
	res, _ := runSequence(t, f, testPlan(), PreflightResult{}, nil)
	if res.AgentName != "tkb-25" {
		t.Fatalf("result agent name = %q, want the sent name as the fallback", res.AgentName)
	}
	if got := f.only(t, "agent.prompt").(AgentPromptParams).Target; got != "tkb-25" {
		t.Fatalf("prompt target = %q", got)
	}
}

// A failed start leaves no agent name, because no agent exists — but it does
// leave the name it tried, which is the useful half of "tkb-25-2 was refused".
func TestSequenceFailedStartLeavesNoAgentNameButRecordsTheOneTried(t *testing.T) {
	taken := &APIError{Code: CodeAgentNameTaken, Message: "taken"}
	f := &recordingDispatch{create: createdWorktree(), startErrs: []error{taken, taken}}
	res, _ := runSequence(t, f, testPlan(), PreflightResult{}, (&recordingSleep{}).sleep)
	if res.AgentName != "" {
		t.Fatalf("AgentName = %q after a failed start; no agent exists to name", res.AgentName)
	}
	if res.Start.Name != "tkb-25-2" {
		t.Errorf("Start.Name = %q, want the last name tried", res.Start.Name)
	}
}

// The retry budget has to fit inside whatever deadline a caller puts on the
// whole stage, with herdr's own timeout for the LAST attempt inside it too.
// That is the relation a caller sizes its budget from, so it is exported and
// asserted rather than recomputed by hand.
func TestAgentRetryBackoffTotal(t *testing.T) {
	if got, want := AgentRetryBackoffTotal(), 6300*time.Millisecond; got != want {
		t.Fatalf("AgentRetryBackoffTotal = %v, want %v", got, want)
	}
	// Each entry counted exactly once, in case somebody adds one.
	var sum time.Duration
	for _, d := range agentRetryBackoff {
		sum += d
	}
	if sum != AgentRetryBackoffTotal() {
		t.Fatalf("total %v does not match the schedule %v", AgentRetryBackoffTotal(), agentRetryBackoff)
	}
	if len(agentRetryBackoff) != 6 {
		t.Fatalf("the schedule has %d retries, want 6", len(agentRetryBackoff))
	}
	// Geometric, and it has to stay geometric: the whole argument for the
	// length is that each step buys the most headroom per unit of worst-case
	// latency, which a flattened tail would quietly undo.
	for i := 1; i < len(agentRetryBackoff); i++ {
		if agentRetryBackoff[i] != 2*agentRetryBackoff[i-1] {
			t.Fatalf("the schedule stopped doubling at %d: %v", i, agentRetryBackoff)
		}
	}
	// The measured case (TKB-27: agent.start succeeded on attempt 5, after
	// 1.5s of waiting) must sit well inside the budget, not at its edge.
	if observed := 1500 * time.Millisecond; AgentRetryBackoffTotal() < 3*observed {
		t.Fatalf("budget %v is less than 3x the observed %v", AgentRetryBackoffTotal(), observed)
	}
}

// ---- the wire, over a real socket ----

// pinSequence serves n requests over a REAL unix socket, one connection each
// (herdr closes after every reply, so the client dials per call), and hands
// back every request line it read.
//
// A real socket rather than a net.Pipe for the same reason PR 1's pinned
// request test used one: what rots on a herdr upgrade is the parameter names
// on the wire, and the only honest way to pin them is to read the bytes a
// genuine connection carried.
func pinSequence(t *testing.T, replies []string) (*SocketSource, func() []string) {
	t.Helper()
	// os.MkdirTemp rather than t.TempDir for the reason realSocket documents:
	// a temp path keyed by the test name blows past macOS's ~104-byte sun_path
	// limit for unix sockets.
	dir, err := os.MkdirTemp("", "hd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}

	lines := make(chan string, len(replies))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range replies {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			line, err := bufio.NewReader(conn).ReadString('\n')
			if err == nil {
				lines <- line
				conn.Write([]byte(replies[i] + "\n"))
			}
			conn.Close()
		}
	}()
	// Close the listener first so the accept loop ends, then wait for it: the
	// goroutine must be gone before the test is, or -race sees it writing to a
	// channel a finished test owns.
	t.Cleanup(func() { ln.Close(); <-done })

	return &SocketSource{Client: NewClient(path)}, func() []string {
		close(lines)
		var out []string
		for l := range lines {
			out = append(out, l)
		}
		return out
	}
}

// The three requests a dispatch puts on the wire, pinned as JSON. This is the
// test that has to fail on a herdr upgrade that renames a parameter.
func TestSequenceRequestJSONOverARealSocket(t *testing.T) {
	src, recorded := pinSequence(t, []string{
		`{"id":"a","result":{"type":"worktree_created",` +
			`"workspace":{"workspace_id":"wE","label":"TKB-25","number":5},` +
			`"tab":{"tab_id":"wE:t1","workspace_id":"wE","label":"TKB-25"},` +
			`"root_pane":{"pane_id":"wE:p1","workspace_id":"wE","tab_id":"wE:t1",` +
			`"terminal_id":"term_1","cwd":"/wt/tkb-25","focused":false},` +
			`"worktree":{"path":"/wt/tkb-25","branch":"feature/tkb-25-dispatch-a-ticket",` +
			`"label":"TKB-25","open_workspace_id":"wE","is_bare":false,"is_detached":false,` +
			`"is_linked_worktree":true,"is_prunable":false}}}`,
		// The reply names the agent, and names it something other than what
		// was asked for. That is the case this pins: `name` decoded off a real
		// socket, and the prompt following it.
		`{"id":"b","result":{"type":"agent_started","argv":["claude","--permission-mode","plan"],` +
			`"agent":{"pane_id":"wE:p1","agent":"claude","name":"tkb-25-renamed",` +
			`"agent_status":"idle","workspace_id":"wE","tab_id":"wE:t1","terminal_id":"term_1",` +
			`"focused":false,"revision":2}}}`,
		`{"id":"c","result":{"type":"ok"}}`,
	})

	res, _ := runSequence(t, src, testPlan(), PreflightResult{RepoRoot: "/src/tktban"}, nil)
	if res.Err != nil {
		t.Fatalf("result = %+v", res)
	}
	if res.AgentName != "tkb-25-renamed" {
		t.Fatalf("agent name = %q, want the one herdr's reply carried", res.AgentName)
	}

	got := recorded()
	if len(got) != 3 {
		t.Fatalf("recorded %d requests, want 3: %v", len(got), got)
	}

	wantParams := []struct {
		method string
		params map[string]any
	}{
		{"worktree.create", map[string]any{
			"cwd": "/src/tktban", "branch": "feature/tkb-25-dispatch-a-ticket",
			"base": "main", "label": "TKB-25", "focus": false,
		}},
		{"agent.start", map[string]any{
			"name": "tkb-25", "kind": "claude", "pane_id": "wE:p1",
			"args": []any{"--permission-mode", "plan"}, "timeout_ms": float64(20000),
		}},
		{"agent.prompt", map[string]any{
			// herdr's reply, not the plan's name and not the name sent.
			"target": "tkb-25-renamed", "text": testPlan().Prompt,
		}},
	}
	for i, want := range wantParams {
		method, p := params(t, []byte(got[i]))
		if method != want.method {
			t.Errorf("request %d method = %q, want %q", i, method, want.method)
		}
		if !reflect.DeepEqual(p, want.params) {
			t.Errorf("request %d params = %v, want exactly %v", i, p, want.params)
		}
	}
	// Spelled out, because these are the fields whose absence is the decision.
	create := requestParams(t, got[0])
	for _, absent := range []string{"path", "trust_repository", "workspace_id"} {
		if _, present := create[absent]; present {
			t.Errorf("worktree.create sent %s: %v", absent, create)
		}
	}
	if _, present := requestParams(t, got[2])["wait"]; present {
		t.Error("agent.prompt sent wait: a board must never block on an agent status")
	}
}

// The reuse path on the wire: worktree.open, with no base.
func TestSequenceOpenRequestJSONOverARealSocket(t *testing.T) {
	src, recorded := pinSequence(t, []string{
		`{"id":"a","result":{"type":"worktree_opened","already_open":true,` +
			`"workspace":{"workspace_id":"wE","label":"TKB-25","number":5},` +
			`"tab":{"tab_id":"wE:t1","workspace_id":"wE","label":"TKB-25"},` +
			`"root_pane":{"pane_id":"wE:p1","workspace_id":"wE","tab_id":"wE:t1",` +
			`"terminal_id":"term_1","cwd":"/wt/tkb-25","focused":true},` +
			`"worktree":{"path":"/wt/tkb-25","branch":"feature/tkb-25-dispatch-a-ticket",` +
			`"label":"TKB-25","open_workspace_id":"wE","is_bare":false,"is_detached":false,` +
			`"is_linked_worktree":true,"is_prunable":false}}}`,
		`{"id":"b","result":{"type":"agent_started","argv":["claude"],` +
			`"agent":{"pane_id":"wE:p1","agent":"claude","agent_status":"idle"}}}`,
		`{"id":"c","result":{"type":"ok"}}`,
	})

	res, _ := runSequence(t, src, testPlan(), PreflightResult{ExistingWorktreePath: "/wt/tkb-25"}, nil)
	if res.Err != nil || !res.Reused || !res.AlreadyOpen {
		t.Fatalf("result = %+v", res)
	}
	got := recorded()
	if len(got) != 3 {
		t.Fatalf("recorded %d requests: %v", len(got), got)
	}
	method, p := params(t, []byte(got[0]))
	if method != "worktree.open" {
		t.Fatalf("method = %q, want worktree.open", method)
	}
	want := map[string]any{"cwd": "/src/tktban", "branch": "feature/tkb-25-dispatch-a-ticket", "focus": false}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("params = %v, want exactly %v (no base, no path, no trust)", p, want)
	}
}

// requestParams is params() without the method, for the absence checks.
func requestParams(t *testing.T, line string) map[string]any {
	t.Helper()
	_, p := params(t, []byte(line))
	return p
}

// A herdr error over a real socket arrives as a code the sequence branched on,
// and the sequence stops there.
func TestSequenceRealSocketErrorReplyStopsTheSequence(t *testing.T) {
	src, recorded := pinSequence(t, []string{
		`{"id":"a","error":{"code":"workspace_trust_blocked","message":"repository is not trusted"}}`,
	})
	res, stages := runSequence(t, src, testPlan(), PreflightResult{}, nil)
	if got := ErrorCode(res.Err); got != CodeWorkspaceTrustBlocked {
		t.Fatalf("error code = %q, want workspace_trust_blocked", got)
	}
	if res.Created || res.Started {
		t.Fatalf("result = %+v", res)
	}
	if !slices.Equal(stages, []Stage{StageWorktree}) {
		t.Errorf("stages = %v", stages)
	}
	if got := recorded(); len(got) != 1 || !strings.Contains(got[0], `"worktree.create"`) {
		t.Errorf("requests = %v, want exactly the one create", got)
	}
}

// ---- the prompt's own retry budget ----
//
// The bug this section exists for, from the first live dispatch (TKB-27): the
// worktree and the agent came up correctly and the prompt was dropped on the
// floor, so the agent sat idle waiting to be told something until a human
// pasted the prompt in by hand.
//
//	agent: tkb-27 (claude), argv: claude
//	agent start: 5 attempts, 4 busy, 0 renamed, waited 1.5s
//	prompt: NOT sent — paste it into the pane by hand
//	agent.prompt failed — agent_not_ready: agent tkb-27 is not an active named agent
//
// `herdr agent list` then showed tkb-27 registered and working, so the name was
// right and herdr did register it — just not by the time we asked, once. A
// successful agent.start does not mean agent.prompt will be accepted.

// herdr's own answer while the named-agent registry catches up.
func notReady(name string) error {
	return &APIError{Code: CodeAgentNotReady, Message: "agent " + name + " is not an active named agent"}
}

// Two not-ready replies then a success: three calls, and the delays are the
// documented schedule's first two steps.
func TestPromptAgentRetriesOnNotReady(t *testing.T) {
	f := &recordingDispatch{promptErrs: []error{notReady("tkb-25"), notReady("tkb-25")}}
	sl := &recordingSleep{}

	stats, err := PromptAgentWithRetry(context.Background(), f, "tkb-25", "go", sl.sleep)
	if err != nil {
		t.Fatalf("two not-readies then a success must succeed: %v", err)
	}
	if len(f.calls) != 3 {
		t.Fatalf("agent.prompt calls = %d (%v), want 3", len(f.calls), f.methods())
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	if !slices.Equal(sl.waits, want) {
		t.Fatalf("waits = %v, want %v", sl.waits, want)
	}
	if stats.Attempts != 3 || stats.NotReady != 2 || stats.Busy != 0 {
		t.Errorf("stats = %+v", stats)
	}
	if stats.Waited != 300*time.Millisecond {
		t.Errorf("total wait = %v, want 300ms", stats.Waited)
	}
	// Every attempt asks for the same thing. A retry that changed the target or
	// the text would be a different call, not a retry — and a changed text is
	// how an agent ends up with half a prompt twice.
	for i, c := range f.calls {
		p := c.params.(AgentPromptParams)
		if p.Target != "tkb-25" || p.Text != "go" {
			t.Errorf("attempt %d = %+v", i, p)
		}
	}
}

// agent_pane_busy is retried here too: it is the same "the pane is not there
// yet" the start waits out.
func TestPromptAgentRetriesOnPaneBusy(t *testing.T) {
	f := &recordingDispatch{promptErrs: []error{&APIError{Code: CodeAgentPaneBusy, Message: "busy"}}}
	sl := &recordingSleep{}
	stats, err := PromptAgentWithRetry(context.Background(), f, "tkb-25", "go", sl.sleep)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Attempts != 2 || stats.Busy != 1 || stats.NotReady != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

// The budget is finite here as well, and running out changes nothing on disk:
// the agent is running, it just has not been told what to do.
func TestPromptAgentGivesUpAfterTheBudget(t *testing.T) {
	errs := make([]error, 10)
	for i := range errs {
		errs[i] = notReady("tkb-25")
	}
	f := &recordingDispatch{promptErrs: errs}
	sl := &recordingSleep{}

	stats, err := PromptAgentWithRetry(context.Background(), f, "tkb-25", "go", sl.sleep)
	if code := ErrorCode(err); code != CodeAgentNotReady {
		t.Fatalf("err = %v, want agent_not_ready", err)
	}
	if len(f.calls) != 7 {
		t.Fatalf("agent.prompt calls = %d, want 7 (one attempt plus six retries)", len(f.calls))
	}
	want := []time.Duration{
		100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond,
		800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond,
	}
	if !slices.Equal(sl.waits, want) {
		t.Fatalf("waits = %v, want %v", sl.waits, want)
	}
	if stats.Attempts != 7 || stats.NotReady != 7 || stats.Waited != 6300*time.Millisecond {
		t.Errorf("stats = %+v", stats)
	}
	assertNothingDestroyed(t, f)
}

// agent_blocked is NOT retried. herdr rejects the submission before sending any
// input because the agent is at an approval or question dialog: asking again
// cannot help while it is up, and once a human dismisses it the prompt would be
// typed into whatever replaced it.
//
// Nor are the refusals that will never come right, nor agent_prompt_failed —
// herdr writes the text and the Enter as one submission and reports success
// only once both are through, so a failure may have left part of the prompt in
// the pane, and sending it again could append to it.
func TestPromptAgentDoesNotRetryTheseCodes(t *testing.T) {
	for _, code := range []string{
		CodeAgentBlocked,
		CodeAgentPromptFailed,
		CodeInvalidAgentArgument,
		CodeInvalidAgentName,
		"agent_not_found",
		"agent_name_not_found",
		CodePaneNotFound,
	} {
		f := &recordingDispatch{promptErr: &APIError{Code: code, Message: "no"}}
		sl := &recordingSleep{}
		stats, err := PromptAgentWithRetry(context.Background(), f, "tkb-25", "go", sl.sleep)
		if ErrorCode(err) != code {
			t.Errorf("%s: err = %v", code, err)
		}
		if len(f.calls) != 1 {
			t.Errorf("%s: agent.prompt calls = %d, want 1", code, len(f.calls))
		}
		if len(sl.waits) != 0 {
			t.Errorf("%s: waited %v before giving up", code, sl.waits)
		}
		if stats.Attempts != 1 {
			t.Errorf("%s: stats = %+v", code, stats)
		}
	}
	// A dial failure is not retried either.
	f := &recordingDispatch{promptErr: errors.New("connection refused")}
	if _, err := PromptAgentWithRetry(context.Background(), f, "tkb-25", "go", (&recordingSleep{}).sleep); err == nil {
		t.Fatal("a dial failure must be reported")
	}
	if len(f.calls) != 1 {
		t.Fatalf("agent.prompt calls = %d, want 1", len(f.calls))
	}
}

// A context that ends mid-backoff stops the retries and reports both halves.
func TestPromptAgentStopsWhenTheContextEndsMidBackoff(t *testing.T) {
	nr := notReady("tkb-25")
	f := &recordingDispatch{promptErrs: []error{nr, nr, nr}}
	sl := &recordingSleep{err: context.DeadlineExceeded}

	stats, err := PromptAgentWithRetry(context.Background(), f, "tkb-25", "go", sl.sleep)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if !errors.Is(err, nr) {
		t.Errorf("err = %v, want the last herdr reply wrapped too", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("agent.prompt calls = %d, want 1", len(f.calls))
	}
	if stats.Waited != 0 {
		t.Errorf("stats.Waited = %v, want 0", stats.Waited)
	}
}

// A nil Sleeper is the real one, as for the start.
func TestPromptAgentDefaultsItsSleeper(t *testing.T) {
	f := &recordingDispatch{}
	if _, err := PromptAgentWithRetry(context.Background(), f, "tkb-25", "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v", f.methods())
	}
}

// And through the whole sequence: the prompt that used to be dropped now lands,
// and the result records what it took.
func TestSequenceRetriesTheFirstPromptAndRecordsIt(t *testing.T) {
	f := &recordingDispatch{
		create:     createdWorktree(),
		start:      startedAgent(),
		promptErrs: []error{notReady("tkb-25"), notReady("tkb-25")},
	}
	sl := &recordingSleep{}

	res, _ := runSequence(t, f, testPlan(), PreflightResult{}, sl.sleep)
	if !res.Prompted || res.Err != nil {
		t.Fatalf("result = %+v, want a prompted agent", res)
	}
	want := []string{"worktree.create", "agent.start", "agent.prompt", "agent.prompt", "agent.prompt"}
	if !slices.Equal(f.methods(), want) {
		t.Fatalf("method sequence = %v, want %v", f.methods(), want)
	}
	if res.Prompt.Attempts != 3 || res.Prompt.NotReady != 2 {
		t.Errorf("prompt stats = %+v", res.Prompt)
	}
	if want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}; !slices.Equal(sl.waits, want) {
		t.Errorf("waits = %v, want %v", sl.waits, want)
	}
}

// The live failure, replayed end to end: the prompt never becomes acceptable.
// The agent IS dispatched — that is what makes this the one failure that still
// moves the lane — and the result says exactly how hard we tried.
func TestSequencePromptExhaustedLeavesADispatchedAgent(t *testing.T) {
	errs := make([]error, 10)
	for i := range errs {
		errs[i] = notReady("tkb-25")
	}
	f := &recordingDispatch{create: createdWorktree(), start: startedAgent(), promptErrs: errs}

	res, stages := runSequence(t, f, testPlan(), PreflightResult{}, (&recordingSleep{}).sleep)
	if res.Prompted {
		t.Fatal("the prompt did not land, so nothing may say it did")
	}
	if !res.Started || res.Failed != StagePrompt {
		t.Fatalf("result = %+v", res)
	}
	if code := ErrorCode(res.Err); code != CodeAgentNotReady {
		t.Errorf("error code = %q", code)
	}
	if res.Prompt.Attempts != 7 {
		t.Errorf("prompt stats = %+v, want the whole budget spent", res.Prompt)
	}
	if !slices.Equal(stages, []Stage{StageWorktree, StageAgent, StagePrompt}) {
		t.Errorf("stages = %v", stages)
	}
	assertNothingDestroyed(t, f)
}

// A retried prompt on the wire: the same params on every attempt, pinned over a
// real socket. The retry is the new code path, and a retry that resent altered
// params would be the worst possible bug here — half a prompt, twice.
func TestSequenceRetriedPromptRequestJSONOverARealSocket(t *testing.T) {
	src, recorded := pinSequence(t, []string{
		`{"id":"a","result":{"type":"worktree_created",` +
			`"workspace":{"workspace_id":"wE","label":"TKB-25","number":5},` +
			`"tab":{"tab_id":"wE:t1","workspace_id":"wE","label":"TKB-25"},` +
			`"root_pane":{"pane_id":"wE:p1","workspace_id":"wE","tab_id":"wE:t1",` +
			`"terminal_id":"term_1","cwd":"/wt/tkb-25","focused":false},` +
			`"worktree":{"path":"/wt/tkb-25","branch":"feature/tkb-25-dispatch-a-ticket",` +
			`"label":"TKB-25","open_workspace_id":"wE","is_bare":false,"is_detached":false,` +
			`"is_linked_worktree":true,"is_prunable":false}}}`,
		`{"id":"b","result":{"type":"agent_started","argv":["claude"],` +
			`"agent":{"pane_id":"wE:p1","agent":"claude","name":"tkb-25",` +
			`"agent_status":"idle","workspace_id":"wE","tab_id":"wE:t1",` +
			`"terminal_id":"term_1","focused":false,"revision":2}}}`,
		// The live failure, verbatim.
		`{"id":"c","error":{"code":"agent_not_ready",` +
			`"message":"agent tkb-25 is not an active named agent"}}`,
		`{"id":"d","error":{"code":"agent_not_ready",` +
			`"message":"agent tkb-25 is not an active named agent"}}`,
		`{"id":"e","result":{"type":"ok"}}`,
	})

	sl := &recordingSleep{}
	res, _ := runSequence(t, src, testPlan(), PreflightResult{RepoRoot: "/src/tktban"}, sl.sleep)
	if res.Err != nil || !res.Prompted {
		t.Fatalf("result = %+v, want the retried prompt to have landed", res)
	}
	if res.Prompt.Attempts != 3 || res.Prompt.NotReady != 2 {
		t.Errorf("prompt stats = %+v", res.Prompt)
	}

	got := recorded()
	if len(got) != 5 {
		t.Fatalf("recorded %d requests, want 5: %v", len(got), got)
	}
	wantPrompt := map[string]any{"target": "tkb-25", "text": testPlan().Prompt}
	for i, line := range got[2:] {
		method, p := params(t, []byte(line))
		if method != "agent.prompt" {
			t.Errorf("request %d method = %q, want agent.prompt", i+2, method)
		}
		if !reflect.DeepEqual(p, wantPrompt) {
			t.Errorf("prompt attempt %d params = %v, want exactly %v", i+1, p, wantPrompt)
		}
		// Still no wait, on a retry as on a first attempt.
		if _, present := p["wait"]; present {
			t.Errorf("prompt attempt %d sent wait", i+1)
		}
	}
}
