package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/model"
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
	m = m.WithLive(src).WithDispatch(testDispatchDir)
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
var dispatchReadVerbs = map[string]bool{
	"cfg":       true, // board.roles, priorities, vcs, board.ownership
	"list":      true,
	"lane-time": true, // --read-only, so it records no worklog
	"view":      true,
}

// nonReadCall returns the first tkt invocation that was not one of those, or
// nil. A dry run must not transition a ticket, comment on one or edit one.
func nonReadCall(cr *captureRunner) []string {
	for _, call := range cr.calls {
		if len(call) > 0 && !dispatchReadVerbs[call[0]] {
			return call
		}
	}
	return nil
}

// Every rung of the ladder says which one it was and opens nothing. A key
// that silently does nothing is the failure mode this is guarding against.
func TestDispatchGuardLadder(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, m Model, src *fakeDispatch) Model
		want  string
	}{
		{"the setting is off", func(_ *testing.T, m Model, _ *fakeDispatch) Model {
			m.settings["dispatch"] = false
			return m
		}, "Dispatch is off (set dispatch = true in tktban's settings.toml)"},

		{"no live source at all", func(t *testing.T, _ Model, _ *fakeDispatch) Model {
			m, _ := testModel(t)
			m = m.WithDispatch(testDispatchDir)
			m.settings["dispatch"] = true
			return loadBoard(m)
		}, "Live agent status is off"},

		{"a source that only reads status", func(t *testing.T, _ Model, _ *fakeDispatch) Model {
			m, _ := testModel(t)
			m = m.WithLive(&fakeLive{}).WithDispatch(testDispatchDir)
			m.settings["dispatch"] = true
			return goLive(t, loadBoard(m))
		}, "This board can't dispatch herdr agents"},

		{"herdr has gone quiet", func(t *testing.T, m Model, src *fakeDispatch) Model {
			src.pollErr = errPoll
			for range liveMaxFails {
				m = poll(t, m)
			}
			if m.live.on {
				t.Fatal("setup: live should be off after the failure threshold")
			}
			return m
		}, "herdr live status unavailable"},

		{"nothing selected", func(_ *testing.T, m Model, _ *fakeDispatch) Model {
			for i := range m.columns {
				m.columns[i].Cards = nil
			}
			return m
		}, "Select a card first"},

		{"the card has no key", func(_ *testing.T, m Model, _ *fakeDispatch) Model {
			m.columns[0].Cards[0].Key = ""
			return m
		}, "That card has no ticket key"},

		{"the ticket already has an agent", func(t *testing.T, m Model, src *fakeDispatch) Model {
			src.byKey = map[string]herdr.Live{"TKT-1": {
				Status: herdr.StatusWorking,
				Panes:  []herdr.PaneRef{{PaneID: "wC:p1", Status: herdr.StatusWorking}},
			}}
			return poll(t, m)
		}, "TKT-1 already has an agent pane (o focuses it)"},

		{"no directory resolved", func(_ *testing.T, m Model, _ *fakeDispatch) Model {
			return m.WithDispatch("")
		}, "Don't know which repo to dispatch TKT-1 in"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, src, cr := dispatchBoard(t)
			m = c.setup(t, m, src)
			m, _ = update(m, key("D"))
			if m.dispatching {
				t.Fatal("a refused dispatch still started a preparation")
			}
			if m.status != c.want || m.statusKind != "warn" {
				t.Errorf("status = %q (%s), want %q (warn)", m.status, m.statusKind, c.want)
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
			want: "No [vcs] branch_fmt in the tkt config, so there is no branch to cut",
		},
		{
			name: "branch_fmt without the key",
			vcs:  `{"default_branch":"main","branch_fmt":"feature/{slug}"}`,
			want: `branch_fmt "feature/{slug}" doesn't name TKT-1: the board could never badge or jump to its agent`,
		},
		{
			name:      "no agent-owned transition out of the lane",
			ownership: `{"todo->done":"human"}`,
			want:      "No agent-owned transition out of To Do",
		},
		{
			name:      "no ownership config at all",
			ownership: failReply,
			want:      "No agent-owned transition out of To Do",
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
			if len(src.listed) != 0 {
				t.Errorf("a config refusal still called herdr: %v", src.listed)
			}
		})
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
