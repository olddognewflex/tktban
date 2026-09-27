package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/model"
	"github.com/olddognewflex/tktban/internal/settings"
	"github.com/olddognewflex/tktban/internal/tkt"
)

// Dispatcher is the optional slice of a live source that can dispatch — a
// separate interface from LiveSource for the same reason PaneFocuser is: a
// source that only reads status still drives the badges, it just has no D key.
//
// It is herdr.DispatchClient exactly, and that interface is deliberately
// exhaustive: there is no worktree.remove, no pane.close and no
// workspace.close in it, so no failure path here can reach one. A worktree
// that survives a failed agent start is left where it is and recorded on the
// ticket.
//
// The board holds no herdr parameters of its own. Every request this file
// causes is built by herdr.Sequence; the board's job is to give each step a
// budget, say which one is running, and word the outcome.
type Dispatcher interface {
	herdr.DispatchClient
}

// dispatchPrepTimeout bounds the whole preparation — two tkt config reads and
// one herdr worktree.list. It is longer than the 1s every other herdr call
// gets because a tkt invocation is a Python process start, and shorter than a
// person's patience for a key that opens a dialog.
//
// It has to bound all of it, not just the herdr call. startDispatch latches
// m.dispatching until a dispatchPrepMsg comes back, so a preparation that
// never returns would leave D answering "Already preparing a dispatch" for
// the rest of the session. The budget is what guarantees the latch clears:
// every path out of dispatchPrepCmd returns a message, and the context makes
// sure every path is reached — tk.WithContext puts the deadline on the tkt
// subprocesses themselves, so a wedged tkt is killed rather than waited on.
const dispatchPrepTimeout = 2 * time.Second

// dispatchPrepContext mints that budget. A variable for the same reason
// settings.rename and herdr.symbolicRef are: a test needs to see what the
// code does when the budget has run out, and waiting two real seconds to
// find out is not a test, it is a delay.
var dispatchPrepContext = func() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), dispatchPrepTimeout)
}

// dispatchTarget is the card a dispatch was started from, captured whole at
// the keypress.
//
// This is the point of the type. Preparation is asynchronous, and the board
// auto-refreshes underneath it; if any later stage re-read the selection, a
// refresh that reordered a column — or a person pressing j — would silently
// re-point a dispatch at a different ticket between the keypress and the
// confirm. Nothing downstream looks at the model's selection again.
type dispatchTarget struct {
	key     string // the ticket, normalized
	summary string
	role    string // the card's own column role, for the ownership lookup
	dir     string // the checkout to dispatch in (DispatchDir resolved it)

	roles []model.RolePair // board order, for ranking and naming the target lane

	// configFile is the tkt config to name in a refusal that asks for a
	// configuration change. Captured with the rest of the target, so the
	// command never reaches back into the model.
	configFile string

	agentKind string
	agentArgs []string
	prompt    string // "" takes herdr.DefaultPrompt
}

// dispatchRefusal is a reason not to dispatch, already worded for the status
// line. It keeps the config-level refusals (no branch_fmt, a branch_fmt that
// does not name the key, no agent-owned transition) in the same shape as the
// typed refusals herdr sends back, so onDispatchPrep has one path.
type dispatchRefusal struct{ msg string }

func (e dispatchRefusal) Error() string { return e.msg }

func refuse(format string, a ...any) error {
	return dispatchRefusal{msg: fmt.Sprintf(format, a...)}
}

// dispatchPrepMsg is a prepared plan, or the reason there is none.
type dispatchPrepMsg struct {
	plan herdr.Plan
	pre  herdr.PreflightResult
	err  error
}

// dispatchResultMsg is the confirm modal's outcome, and it carries the whole
// plan back rather than just the ticket key.
//
// The plan and its preflight are what the create sequence acts on — the
// branch, the base, the agent name, and above all ExistingWorktreePath, which
// is the entire create-a-worktree versus open-the-existing-one decision. They
// were computed once, shown to the person, and agreed to; recomputing them
// after the confirm would mean acting on something nobody was shown, and
// re-reading the selection would re-point the dispatch. So they travel with
// the answer.
type dispatchResultMsg struct {
	plan      herdr.Plan
	pre       herdr.PreflightResult
	confirmed bool
}

// DispatchOpts is how a board is told about the D key. The caller resolves
// the directory (herdr.DispatchDir) and reads the opt-in setting, because
// only it knows the herdr environment and the command line.
//
// OptedOut is separate from an empty Dir because they are different refusals:
// "--no-herdr-dispatch was given" is a thing the person did, and telling them
// "don't know which repo to dispatch in" instead would send them looking for
// a configuration problem they do not have.
type DispatchOpts struct {
	Dir      string
	OptedOut bool
}

// WithDispatch turns the D key on and says which checkout it dispatches in.
// A zero DispatchOpts leaves the key refusing, which is what a board outside
// herdr does.
func (m Model) WithDispatch(o DispatchOpts) Model {
	m.dispatchDir = o.Dir
	m.dispatchOptedOut = o.OptedOut
	return m
}

// startDispatch is the D key: the guard ladder, then the preparation command.
//
// Every refusal says which one it was and opens nothing, because the key
// otherwise does nothing visible. The ladder runs in the order the guards get
// cheaper to be wrong about: the opt-in, then herdr, then the card, then the
// repo. The last two rungs — the branch convention and the agent-owned
// transition — need `tkt cfg` reads, so they run inside the command rather
// than on the UI loop; they still refuse before anything touches herdr, and
// they still produce a status and no modal.
func (m Model) startDispatch() (tea.Model, tea.Cmd) {
	if m.dispatchOptedOut {
		return m, m.setStatus("Dispatch is off for this board (--no-herdr-dispatch)", "warn")
	}
	if on, _ := m.settings["dispatch"].(bool); !on {
		// Name the file. There are two of them — herdr's plugin state dir and
		// the standalone config dir — and a person who has just set
		// dispatch = true in the other one is owed the path, not a noun.
		return m, m.setStatus("Dispatch is off — set dispatch = true in "+m.settingsFile(), "warn")
	}
	if m.live.src == nil {
		return m, m.setStatus("Live agent status is off", "warn")
	}
	disp, canDispatch := m.live.src.(Dispatcher)
	if !canDispatch {
		return m, m.setStatus("This board can't dispatch herdr agents", "warn")
	}
	if !m.live.on {
		return m, m.setStatus("herdr live status unavailable", "warn")
	}
	// A dispatch that is actually creating things, as opposed to preparing.
	// This is the rung that stops two agents ending up on one branch: the
	// progress dialog swallows keys, but a dialog can be displaced by an
	// asynchronous modal landing, and dispatching below covers only the ~2s
	// preparation — it is false for the whole create phase.
	if m.dispatchBusy() {
		return m, m.setStatus(m.dispatchBusyText(), "warn")
	}
	if m.dispatching {
		return m, m.setStatus("Already preparing a dispatch", "warn")
	}
	card, ok := m.selectedCard()
	if !ok {
		return m, m.setStatus("Select a card first", "warn")
	}
	key := normalizeKey(card.Key)
	if key == "" {
		return m, m.setStatus("That card has no ticket key", "warn")
	}
	// An agent is already on this ticket. Dispatching a second one is a way to
	// end up with two agents racing the same branch, so the board sends the
	// person to the one that exists instead.
	//
	// Checked again in onDispatchResult: live status keeps polling while the
	// dialog is open, so this answer can go stale between D and enter.
	if _, live := herdr.PickPane(m.live.byKey[key]); live {
		return m, m.setStatus(key+" already has an agent pane (o focuses it)", "warn")
	}
	if m.dispatchDir == "" {
		return m, m.setStatus("Don't know which repo to dispatch "+key+
			" in — open the board from a pane in the repo", "warn")
	}
	target := dispatchTarget{
		key:        key,
		summary:    card.Summary,
		role:       m.columns[m.focusCol].Role,
		dir:        m.dispatchDir,
		roles:      m.roles,
		configFile: m.tktConfigFile(),
		agentKind:  m.dispatchAgentKind(),
		agentArgs:  strings.Fields(str(m.settings["dispatch_args"])),
		prompt:     str(m.settings["dispatch_prompt"]),
	}
	m.dispatching = true
	return m, dispatchPrepCmd(m.tkt, disp, target)
}

// dispatchAgentKind is the herdr agent kind to start. An empty or blank
// setting falls back to the default rather than asking herdr to start "".
func (m Model) dispatchAgentKind() string {
	if kind := strings.TrimSpace(str(m.settings["dispatch_agent"])); kind != "" {
		return kind
	}
	// Read from the settings defaults, so the fallback and the documented
	// default cannot drift apart.
	kind, _ := settings.Defaults["dispatch_agent"].(string)
	return kind
}

// dispatchPrepCmd builds the plan and preflights it, all off the UI loop: two
// `tkt cfg` subprocesses and exactly one herdr worktree.list, every one of
// them inside the same bounded context. It creates nothing.
func dispatchPrepCmd(tk *tkt.Tkt, d Dispatcher, t dispatchTarget) tea.Cmd {
	return func() tea.Msg {
		// First statement in the closure: everything below it, tkt
		// subprocesses included, runs under this deadline.
		ctx, cancel := dispatchPrepContext()
		defer cancel()
		// A new local, never an assignment back to the captured tk. Binding
		// the bounded copy to the parameter would outlive this invocation:
		// a second call of the same command would start out holding the first
		// call's already-cancelled context and time out every read before
		// making it. Bubble Tea runs a command once, so nothing does that
		// today — but a retry is exactly the shape that would.
		btk := tk.WithContext(ctx)

		// Both config readers are best-effort and answer a zero value for any
		// failure, a timeout included, so a timed-out read would otherwise
		// come out as "no branch_fmt configured". Say what happened instead.
		timedOut := func() *dispatchPrepMsg {
			if ctx.Err() == nil {
				return nil
			}
			return &dispatchPrepMsg{err: refuse("Timed out reading the tkt config for %s", t.key)}
		}

		vcs := btk.VCS()
		if out := timedOut(); out != nil {
			return *out
		}
		if vcs.BranchFmt == "" {
			return dispatchPrepMsg{err: refuse(
				"No [vcs] branch_fmt in %s, so there is no branch to cut", t.configFile)}
		}
		// Rendered here as well as inside BuildPlan. RenderBranch is pure, so
		// the two agree by construction, and checking it here keeps the
		// refusal ahead of the ownership read in the ladder.
		branch := herdr.RenderBranch(vcs.BranchFmt, t.key, herdr.SlugFromSummary(t.summary))
		if !herdr.BranchNamesKey(branch, t.key) {
			return dispatchPrepMsg{err: refuse(
				"branch_fmt %q in %s doesn't name %s: the board could never badge or jump to its agent",
				vcs.BranchFmt, t.configFile, t.key)}
		}
		ownership := btk.BoardOwnership()
		if out := timedOut(); out != nil {
			return *out
		}
		targetRole, ok := tkt.AgentTarget(ownership, t.role, roleOrder(t.roles))
		if !ok {
			return dispatchPrepMsg{err: refuse(
				"No agent-owned transition out of %s ([board] ownership in %s)",
				laneOf(t.roles, t.role), t.configFile)}
		}
		plan := herdr.BuildPlan(herdr.PlanInput{
			Key:        t.key,
			Summary:    t.summary,
			Dir:        t.dir,
			Repo:       vcs.Repo,
			BranchFmt:  vcs.BranchFmt,
			Base:       vcs.DefaultBranch,
			SourceRole: t.role,
			SourceLane: laneOf(t.roles, t.role),
			TargetRole: targetRole,
			TargetLane: laneOf(t.roles, targetRole),
			AgentKind:  t.agentKind,
			AgentArgs:  t.agentArgs,
			Prompt:     t.prompt,
		})
		pre, err := herdr.Preflight(ctx, d, plan)
		return dispatchPrepMsg{plan: plan, pre: pre, err: err}
	}
}

// onDispatchPrep opens the confirm modal, or says why there is nothing to
// confirm.
func (m Model) onDispatchPrep(msg dispatchPrepMsg) (tea.Model, tea.Cmd) {
	m.dispatching = false
	if msg.err != nil {
		return m, m.setStatus(dispatchRefusalText(msg.plan, msg.err), "warn")
	}
	// The person opened something else while this was in flight. Their modal
	// wins: a dialog that replaces the one you are typing in is worse than a
	// dispatch you have to ask for again — but say so, or D looks broken.
	if m.modal != nil {
		return m, m.setStatus("Dispatch for "+msg.plan.Key+" was dropped behind the open dialog — press D again", "warn")
	}
	m.modal = newDispatchModal(msg.plan, msg.pre)
	return m, nil
}

// dispatchRefusalText words a failed preparation. The two typed herdr
// refusals get their own sentences; a refusal we made ourselves is already a
// sentence; anything else is reported as itself rather than guessed at.
func dispatchRefusalText(plan herdr.Plan, err error) string {
	switch {
	case errors.Is(err, herdr.ErrNotGitWorktree):
		return "Not a git work tree: " + plan.Dir
	case errors.Is(err, herdr.ErrLinkedWorktreeSource):
		return "This board is open in a worktree, not the main checkout, so there is nothing to branch from"
	}
	var refusal dispatchRefusal
	if errors.As(err, &refusal) {
		return refusal.msg
	}
	return "Couldn't prepare a dispatch: " + err.Error()
}

// ---- the create sequence ----

// The budgets for the staged calls. Each stage gets its own, because they are
// not comparable amounts of work: cutting a branch and opening a worktree is
// filesystem work on a repository that may be large, starting an agent waits
// for a login shell to reach a prompt and then for a process to come up, and
// typing a prompt into a running agent is one socket round trip.
//
// dispatchAgentTimeout bounds the agent stage, and the stage is the whole retry
// loop rather than one call — so it has to cover herdr's own
// AgentStartTimeoutMS (20s) PLUS the backoff the loop may have spent getting to
// its last attempt (herdr.AgentStartBackoffTotal, 3.1s), with margin. 25s would
// have left the final attempt 1.9s, which is the wrong way round: herdr's timer
// must fire first, because a herdr timeout is a typed error naming what failed,
// while ours is a closed socket that cannot even say whether the agent started.
//
// What it deliberately does not cover is every attempt burning a full 20s in
// turn. That cannot happen: agent_pane_busy is herdr answering immediately, so
// a run that retries is a run of fast replies, and the only attempt that can
// take 20s is one herdr is actually working on.
//
// A test asserts the relation rather than the number, so changing the schedule
// cannot quietly invert it again.
const (
	dispatchWorktreeTimeout = 15 * time.Second
	dispatchAgentTimeout    = 30 * time.Second
	dispatchPromptTimeout   = 5 * time.Second
	dispatchWriteTimeout    = 5 * time.Second
)

// dispatchStageBudget is how long one herdr stage gets.
func dispatchStageBudget(stage herdr.Stage) time.Duration {
	switch stage {
	case herdr.StageWorktree:
		return dispatchWorktreeTimeout
	case herdr.StageAgent:
		return dispatchAgentTimeout
	case herdr.StagePrompt:
		return dispatchPromptTimeout
	}
	return dispatchPromptTimeout
}

// dispatchStageContext / dispatchWriteContext mint those budgets, and
// dispatchSleep is the wait between agent.start retries. All three are
// variables for the same reason dispatchPrepContext is: a test has to be able
// to see what the code does when a budget runs out, and to read the retry
// schedule without spending three real seconds on it.
var (
	dispatchStageContext = func(stage herdr.Stage) (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), dispatchStageBudget(stage))
	}
	dispatchWriteContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), dispatchWriteTimeout)
	}
	dispatchSleep herdr.Sleeper = herdr.SleepCtx
)

// Every message a dispatch sends carries the run it belongs to, and every
// handler drops one whose stamp is not the current run's.
//
// It is not theoretical tidiness. A stage can be in flight for fifteen seconds;
// if a run is ever abandoned — the board refuses a second dispatch now, but a
// future path may not — its outstanding stage message would otherwise be
// applied on top of the newer run's state, splicing two dispatches into one
// report that describes neither. The stamp makes a late message inert instead
// of dangerous.

// dispatchStageMsg is one finished step of the create sequence, carrying the
// whole sequence forward. The sequence is a value, so the message holds the
// state and the model just stores it: two updates never share it.
type dispatchStageMsg struct {
	run int
	seq herdr.Sequence
}

// dispatchLaneMsg is the outcome of the dispatch's `tkt transition`.
type dispatchLaneMsg struct {
	run int
	err error
	// skipped means no transition was attempted, and why says which reason —
	// already worded, because only the board knows the lane names.
	skipped bool
	why     string
}

// dispatchDoneMsg is the outcome of the dispatch's `tkt comment`, which is the
// last thing a dispatch does.
type dispatchDoneMsg struct {
	run int
	err error
}

// dispatchReport is everything the status line and the ticket comment are
// built from: what herdr did, and what the two tkt writes did afterwards.
type dispatchReport struct {
	plan herdr.Plan
	res  herdr.DispatchResult

	laneMoved   bool
	laneSkipped bool
	laneWhy     string // why the lane was left alone, already worded
	laneErr     error

	commentErr error
}

// onDispatchResult acts on the confirm dialog's answer. enter starts the
// create sequence; esc closes the dialog and says nothing, because cancelling
// a dialog needs no announcement.
//
// The plan arrives on the message rather than being rebuilt here, so what is
// created is exactly what the person was shown. Nothing below this line reads
// the board's selection again.
func (m Model) onDispatchResult(msg dispatchResultMsg) (tea.Model, tea.Cmd) {
	// First, above every other branch including the cancel: an answer to a
	// dialog whose run has already started is stale by definition, and the only
	// safe thing to do with it is nothing.
	//
	// The cancel is the one that has to be here. `send` runs the modal's answer
	// as a command, and dispatchModal.Update mutates nothing it is called on, so
	// a dialog that has not yet been swapped for the progress display accepts
	// enter AND a following esc — two dispatchResultMsgs in flight for one
	// dialog. The confirmed one starts the run and installs the progress
	// display; the cancel then nil'd it, and progressModal cannot restore a nil.
	// The dispatch went on cutting a worktree behind a board showing nothing,
	// with the ordinary modal keys reachable again and auto-refresh resumed.
	if m.dispatchBusy() {
		if !msg.confirmed {
			return m, nil // a stale cancel, as silent as any other cancel
		}
		return m, m.setStatus(m.dispatchBusyText(), "warn")
	}
	if !msg.confirmed {
		m.modal = nil
		return m, nil
	}
	// The keypress guard is not enough on its own. Live status keeps polling
	// the whole time the dialog is open, so an agent can appear on this ticket
	// between D and enter — someone else dispatching it, or a pane checking
	// the branch out by hand. This is what stops two agents racing one branch,
	// so it is checked on the plan's own key, and not on whatever the
	// selection has become since.
	if _, live := herdr.PickPane(m.live.byKey[msg.plan.Key]); live {
		m.modal = nil
		return m, m.setStatus(msg.plan.Key+" picked up an agent while the dialog was open (o focuses it)", "warn")
	}
	disp, ok := m.live.src.(Dispatcher)
	if !ok {
		// Unreachable through the D key, which checks this before opening the
		// dialog. Refusing beats a nil dereference if it ever becomes
		// reachable.
		m.modal = nil
		return m, m.setStatus("This board can't dispatch herdr agents", "warn")
	}
	m.dispatchRun++
	m.dispatchSrc = disp
	m.dispatchSeq = herdr.NewSequence(msg.plan, msg.pre)
	m.dispatchRep = dispatchReport{plan: msg.plan}
	// The dialog stays open, now as a progress display. That is the first line
	// of defence against a second enter: it swallows every keystroke. The
	// dispatchBusy rungs above are the second, for the case where an
	// asynchronous modal displaces it.
	m.modal = newDispatchModal(msg.plan, msg.pre).
		withProgress(dispatchStageLabel(m.dispatchSeq))
	return m, dispatchStageCmd(disp, m.dispatchSeq, m.dispatchRun)
}

// ours reports whether a dispatch message belongs to the run in flight.
func (m Model) ours(run int) bool { return m.dispatchBusy() && run == m.dispatchRun }

// dispatchStageCmd runs exactly one step of the sequence, under that step's
// own budget.
//
// One step per command is the whole staging design. It is what gives each step
// its own deadline, what lets the dialog say which step is running, and — the
// reason it is a tea.Cmd and never a bare goroutine — what keeps every write
// this feature makes on Bubble Tea's own command path, where the test harness
// can see it. A write issued from a `go func()` would escape that, and the
// test runner has no mutex precisely so the race detector reports it.
func dispatchStageCmd(d Dispatcher, seq herdr.Sequence, run int) tea.Cmd {
	stage := seq.Stage()
	return func() tea.Msg {
		ctx, cancel := dispatchStageContext(stage)
		defer cancel()
		return dispatchStageMsg{run: run, seq: seq.Next(ctx, d, dispatchSleep)}
	}
}

// onDispatchStage stores a finished step and runs the next one, or moves on to
// the ticket writes once herdr's half is over.
func (m Model) onDispatchStage(msg dispatchStageMsg) (tea.Model, tea.Cmd) {
	if !m.ours(msg.run) {
		return m, nil // a torn-down or superseded run: inert
	}
	m.dispatchSeq = msg.seq
	m.dispatchRep.res = msg.seq.Res
	if msg.seq.Stage() != herdr.StageDone {
		m.modal = m.progressModal(dispatchStageLabel(msg.seq))
		return m, dispatchStageCmd(m.dispatchSrc, msg.seq, m.dispatchRun)
	}
	return m.startDispatchWrites()
}

// startDispatchWrites runs the ticket half: move the lane, then record what
// happened.
//
// The lane moves only when the agent actually started. A worktree that exists
// with no agent in it is not a ticket somebody is working on, so moving it
// would be a lie on the board — and the failure matrix says so for both the
// worktree and the agent rows.
func (m Model) startDispatchWrites() (tea.Model, tea.Cmd) {
	if !m.dispatchSeq.Res.Started {
		return m.startDispatchComment()
	}
	plan := m.dispatchSeq.Plan
	// Only from the lane the plan was built for. `tkt transition` is a move
	// between two named roles, and the ticket may have moved since D was
	// pressed — a refresh, a colleague, another board. Moving it from wherever
	// it is now to the plan's target is not the transition the person agreed
	// to, so it is skipped and said.
	if why, skip := m.laneSkipReason(plan); skip {
		return m, send(dispatchLaneMsg{run: m.dispatchRun, skipped: true, why: why})
	}
	m.modal = m.progressModal("Moving " + plan.Key + " to " + dispatchLaneName(plan) + "…")
	return m, dispatchLaneCmd(m.tkt, plan, m.dispatchRun)
}

// laneSkipReason says whether to leave the ticket's lane alone, and why.
//
// The interesting case is the ticket not being on the board at all. On a
// filtered board that means nothing — the card is hidden, not moved — so the
// plan's own move still runs. On an unfiltered board it means the ticket is
// genuinely not in any lane this board shows any more (deleted, re-keyed, moved
// to a role that is not in [board.roles]), and guessing that it is still in the
// source lane would transition it from a lane it has left.
func (m Model) laneSkipReason(plan herdr.Plan) (string, bool) {
	role, found := m.roleOfKey(plan.Key)
	switch {
	case found && role != plan.SourceRole:
		return plan.Key + " had already left " + dispatchSourceLaneName(plan) +
			" (it is in " + laneOf(m.roles, role) + " now)", true
	case found:
		return "", false
	case m.filter != (filterState{}):
		// A filter is not a transition.
		return "", false
	}
	return plan.Key + " is no longer on the board", true
}

// dispatchLaneCmd is the dispatch's own `tkt transition`, under its own budget.
func dispatchLaneCmd(tk *tkt.Tkt, plan herdr.Plan, run int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := dispatchWriteContext()
		defer cancel()
		return dispatchLaneMsg{run: run, err: tk.WithContext(ctx).Transition(plan.Key, plan.TargetRole)}
	}
}

func (m Model) onDispatchLane(msg dispatchLaneMsg) (tea.Model, tea.Cmd) {
	if !m.ours(msg.run) {
		return m, nil
	}
	m.dispatchRep.laneErr = msg.err
	m.dispatchRep.laneSkipped = msg.skipped
	m.dispatchRep.laneWhy = msg.why
	m.dispatchRep.laneMoved = !msg.skipped && msg.err == nil
	return m.startDispatchComment()
}

// startDispatchComment records the dispatch on the ticket. It runs on every
// path, success and failure alike: a worktree that was left behind, a pane an
// agent never started in and a prompt that did not land are all things the
// next person to open the ticket needs to know, and the comment is the only
// place they survive the board being closed.
func (m Model) startDispatchComment() (tea.Model, tea.Cmd) {
	m.modal = m.progressModal("Recording the dispatch on " + m.dispatchRep.plan.Key + "…")
	return m, dispatchCommentCmd(m.tkt, m.dispatchRep, m.dispatchRun)
}

func dispatchCommentCmd(tk *tkt.Tkt, rep dispatchReport, run int) tea.Cmd {
	body := dispatchCommentBody(rep)
	return func() tea.Msg {
		ctx, cancel := dispatchWriteContext()
		defer cancel()
		return dispatchDoneMsg{run: run, err: tk.WithContext(ctx).Comment(rep.plan.Key, body)}
	}
}

// onDispatchDone closes the dialog and says what happened.
//
// It does not quit, even as herdr's popup. A successful dispatch leaves an
// agent to go and look at, and the person follows with o; quitting here would
// take the board away at the exact moment it has something to show.
func (m Model) onDispatchDone(msg dispatchDoneMsg) (tea.Model, tea.Cmd) {
	if !m.ours(msg.run) {
		return m, nil
	}
	m.dispatchRep.commentErr = msg.err
	rep := m.dispatchRep
	m.modal = nil
	m.dispatchSrc = nil
	m.dispatchSeq = herdr.Sequence{}
	m.dispatchRep = dispatchReport{}
	text, kind := dispatchStatusText(rep)
	// The lane may have moved, so the board is out of date either way.
	return m, tea.Batch(m.setStatus(text, kind), m.refreshCmd())
}

// progressModal re-renders the confirm dialog with the step now running. A
// board that is somehow not showing that dialog any more is left alone.
func (m Model) progressModal(step string) modal {
	dm, ok := m.modal.(dispatchModal)
	if !ok {
		return m.modal
	}
	return dm.withProgress(step)
}

// roleOfKey is the column role a ticket is in right now, across hidden columns
// too — a hidden lane is still the lane the ticket is in.
func (m Model) roleOfKey(key string) (string, bool) {
	for _, col := range m.allColumns {
		for _, c := range col.Cards {
			if normalizeKey(c.Key) == key {
				return col.Role, true
			}
		}
	}
	return "", false
}

// ---- wording ----

// dispatchStageLabel is what the dialog says it is doing.
func dispatchStageLabel(seq herdr.Sequence) string {
	switch seq.Stage() {
	case herdr.StageWorktree:
		if seq.Pre.ExistingWorktreePath != "" {
			return "Opening the worktree at " + seq.Pre.ExistingWorktreePath + "…"
		}
		return "Cutting " + seq.Plan.Branch + " and opening a worktree…"
	case herdr.StageAgent:
		return "Starting " + seq.Plan.AgentKind + " in the worktree…"
	case herdr.StagePrompt:
		return "Sending the first prompt…"
	}
	return "Finishing up…"
}

// dispatchLaneName is the plan's target lane, falling back to the role key so
// a message never comes out blank.
func dispatchLaneName(plan herdr.Plan) string {
	if plan.TargetLane != "" {
		return plan.TargetLane
	}
	return plan.TargetRole
}

// dispatchSourceLaneName is the plan's source lane, same fallback.
func dispatchSourceLaneName(plan herdr.Plan) string {
	if plan.SourceLane != "" {
		return plan.SourceLane
	}
	return plan.SourceRole
}

// dispatchStepName is the herdr method that failed. The worktree stage is two
// methods and which one ran is on the result, so it is named accordingly:
// "worktree.open failed" sends someone to a different part of herdr's log than
// "worktree.create failed" does.
func dispatchStepName(res herdr.DispatchResult) string {
	if res.Failed == herdr.StageWorktree && res.Reused {
		return "worktree.open"
	}
	return res.Failed.String()
}

// herdrRefused reports whether err is herdr's own typed error reply.
//
// This is the single most important distinction in the whole failure matrix.
// herdr answering with a code means herdr decided not to do the thing, so we
// know it did not happen. Anything else — a closed socket, an expired deadline —
// means we do not know whether it happened, and every sentence built from such
// a failure has to admit that rather than assert a clean one. It is also why
// nothing ever retries the worktree step: a retry after "we don't know" is how
// a ticket ends up with two worktrees.
func herdrRefused(err error) bool {
	// A deadline that expired mid-retry wraps BOTH the context error and the
	// last thing herdr said, so errors.As finds an *APIError on it — and that
	// is not a refusal. herdr was still answering "not yet"; it was our own
	// budget that ended the attempt, and we do not know what herdr did next.
	// Checking the context error first is what keeps agent_pane_busy from
	// reading as a decision.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiErr *herdr.APIError
	return errors.As(err, &apiErr)
}

// dispatchFailureText words one herdr failure: the step, and either herdr's
// own code and message or the fact that herdr never answered at all.
func dispatchFailureText(res herdr.DispatchResult) string {
	step := dispatchStepName(res)
	var apiErr *herdr.APIError
	// herdrRefused, not a bare errors.As: a deadline that expired mid-retry
	// wraps the last herdr reply as well, and reporting only that reply would
	// turn "we stopped asking" into "herdr said no".
	if herdrRefused(res.Err) && errors.As(res.Err, &apiErr) {
		return step + " failed — " + apiErr.Code + ": " + apiErr.Message
	}
	// The error's own text carries both halves when there are two — the
	// deadline, and the last thing herdr said before it.
	return step + " did not answer (" + res.Err.Error() + "), so the dispatch may be incomplete"
}

// dispatchStatusText words the whole outcome for the status line, and says
// whether it is a warning.
//
// One function, one place: every row of the failure matrix is worded here, and
// the tests walk the same table.
func dispatchStatusText(rep dispatchReport) (string, string) {
	plan, res := rep.plan, rep.res
	var b strings.Builder
	kind := "warn"
	switch {
	case res.Err != nil && res.Failed == herdr.StageWorktree && !res.Created && !herdrRefused(res.Err):
		// The sibling of the agent-stage arm below, on the more consequential
		// stage. herdr never answered, so we do not know whether it cut the
		// branch and opened a workspace before the socket dropped. "Nothing was
		// created" would be the guess that leaves an orphan worktree nobody
		// ever goes looking for.
		b.WriteString(plan.Key + " may be half dispatched: " + dispatchFailureText(res) +
			" — a worktree may or may not exist for " + plan.Branch +
			", so check `herdr worktree list` before dispatching again. " +
			"Nothing was removed and " + plan.Key + " did not move")

	case res.Err != nil && res.Failed == herdr.StageWorktree && !res.Created:
		// AC2: herdr refused, so nothing at all was created.
		b.WriteString(plan.Key + " was not dispatched: " + dispatchFailureText(res) +
			". Nothing was created and " + plan.Key + " did not move")

	case res.Err != nil && res.Failed == herdr.StageWorktree:
		// herdr made the worktree and then answered without a pane to start an
		// agent in. It is still there, and nothing here removes it.
		b.WriteString(plan.Key + " was not dispatched: " + dispatchFailureText(res) +
			". The worktree at " + res.WorktreePath + " is still there; nothing was removed")

	case res.Failed == herdr.StageAgent && !herdrRefused(res.Err):
		// We asked herdr to start an agent and got no answer. It may be
		// running. Saying "the agent did not start" here would be a guess, and
		// the guess that sends somebody to start a second one.
		b.WriteString(plan.Key + "'s worktree is ready at " + res.WorktreePath +
			" but " + dispatchFailureText(res) +
			" — the agent may or may not be running, so check the pane (o) before dispatching again. " +
			"Nothing was removed and " + plan.Key + " did not move")

	case res.Failed == herdr.StageAgent:
		b.WriteString(plan.Key + "'s worktree is ready at " + res.WorktreePath +
			" but the agent did not start — " + dispatchFailureText(res) +
			". Nothing was removed and " + plan.Key + " did not move")

	case res.Failed == herdr.StagePrompt:
		// The agent IS dispatched, so this is a partial success: the lane
		// moves and the prompt is the person's to paste.
		b.WriteString("Dispatched " + plan.Key + " to " + res.AgentName +
			", but the first prompt did not land — " + dispatchFailureText(res) +
			". o focuses the pane so you can paste it")

	default:
		kind = ""
		b.WriteString("Dispatched " + plan.Key + " to " + res.AgentName + " in " + res.WorktreePath +
			" (o focuses it)")
		if res.Reused {
			b.WriteString(", reusing its worktree")
		}
		if res.Start.Busy > 0 {
			// Otherwise a dispatch that sat there for three seconds looks
			// broken rather than patient.
			b.WriteString(fmt.Sprintf(", after waiting %s for the shell", res.Start.Waited.Round(100*time.Millisecond)))
		}
		if res.Start.Renamed > 0 {
			b.WriteString(" (the name " + herdr.AgentName(plan.Key) + " was taken)")
		}
	}

	switch {
	case rep.laneSkipped:
		why := rep.laneWhy
		if why == "" {
			why = plan.Key + " had already left " + dispatchSourceLaneName(plan)
		}
		b.WriteString(" — " + why + ", so the lane was left alone")
		kind = "warn"
	case rep.laneErr != nil:
		b.WriteString(" — but the lane did not move: " + rep.laneErr.Error())
		kind = "warn"
	case rep.laneMoved:
		b.WriteString(" — moved to " + dispatchLaneName(plan))
	}

	if rep.commentErr != nil {
		// A comment is a record, not a gate: it never blocks and never undoes
		// anything, it just says it did not land.
		b.WriteString(" (not recorded on the ticket: " + rep.commentErr.Error() + ")")
		kind = "warn"
	}
	return b.String(), kind
}

// dispatchCommentBody is the comment a dispatch leaves on the ticket.
//
// It is the only record that outlives the board, and the case it exists for is
// the half-finished one: a worktree that was left in place, the pane an agent
// never started in, herdr's own error code. Somebody reading the ticket a week
// later has to be able to find that worktree and decide what to do with it, so
// every identifier the sequence learned goes in.
func dispatchCommentBody(rep dispatchReport) string {
	plan, res := rep.plan, rep.res
	var b strings.Builder
	if res.Err == nil {
		b.WriteString("tktban dispatched " + plan.Key + " to a herdr agent.\n\n")
	} else {
		b.WriteString("tktban could not finish dispatching " + plan.Key +
			" to a herdr agent; " + dispatchFailureText(res) + ".\n\n")
	}
	line := func(label, value string) {
		if value != "" {
			b.WriteString("- " + label + ": " + value + "\n")
		}
	}
	line("branch", plan.Branch+" (from "+plan.Base+")")
	switch {
	case res.WorktreePath != "":
		what := "created"
		if res.Reused {
			what = "reused"
			if res.AlreadyOpen {
				what = "reused, already open"
			}
		}
		line("worktree", res.WorktreePath+" ("+what+")")
	case res.Err != nil && res.Failed == herdr.StageWorktree && !herdrRefused(res.Err):
		// There is no path to report, because herdr never told us one — which
		// is exactly why this line has to exist. It is the only record that an
		// orphan may be sitting under herdr's worktrees directory, and the
		// comment is the only place that record survives the board closing.
		what := "may or may not have been created"
		if res.Reused {
			what = "already existed; whether herdr opened it is unknown"
		}
		line("worktree", what+" — herdr did not answer "+dispatchStepName(res)+
			". Check `herdr worktree list` for "+plan.Branch+
			" before dispatching again; nothing was removed")
	}
	line("workspace", res.WorkspaceID)
	line("tab", res.TabID)
	line("pane", res.PaneID)
	if res.Started {
		agent := res.AgentName + " (" + plan.AgentKind + ")"
		if len(res.Argv) > 0 {
			agent += ", argv: " + strings.Join(res.Argv, " ")
		}
		line("agent", agent)
	} else if res.Created {
		tried := res.Start.Name
		if tried != "" {
			tried = " (" + tried + ")"
		}
		switch {
		case res.Failed != herdr.StageAgent:
			// The sequence stopped before agent.start was ever called, so
			// "not started" would imply herdr refused it.
			line("agent", "not started: the dispatch stopped before agent.start")
		case herdrRefused(res.Err):
			line("agent", "not started"+tried+"; the worktree and its pane were left in place")
		default:
			// This is the bullet that used to contradict the header. herdr did
			// not answer, so we cannot say it did not start — and the person
			// has to check before dispatching again.
			line("agent", "may or may not have started"+tried+
				": herdr did not answer. Check the pane before dispatching again; nothing was removed")
		}
	}
	// On both paths, not just the successful one. One attempt and six are very
	// different stories — a dispatch that spent three seconds being told the
	// shell was not ready, and then hit our own deadline, reads as a mystery
	// without this — and the failure path is where the reader most needs it.
	if res.Start.Attempts > 1 {
		line("agent start", fmt.Sprintf("%d attempts, %d busy, %d renamed, waited %s",
			res.Start.Attempts, res.Start.Busy, res.Start.Renamed, res.Start.Waited))
	}
	if res.Prompted {
		line("prompt", "sent")
	} else if res.Started {
		line("prompt", "NOT sent — paste it into the pane by hand")
	}
	switch {
	case rep.laneMoved:
		line("lane", dispatchSourceLaneName(plan)+" → "+dispatchLaneName(plan))
	case rep.laneSkipped:
		why := rep.laneWhy
		if why == "" {
			why = plan.Key + " had already left " + dispatchSourceLaneName(plan)
		}
		line("lane", "left alone — "+why)
	case rep.laneErr != nil:
		line("lane", "did not move: "+rep.laneErr.Error())
	default:
		line("lane", "unchanged ("+dispatchSourceLaneName(plan)+")")
	}
	if res.Prompted {
		b.WriteString("\nPrompt:\n\n" + plan.Prompt + "\n")
	}
	return b.String()
}

// roleOrder is the board's role keys in board order.
func roleOrder(roles []model.RolePair) []string {
	out := make([]string, len(roles))
	for i, r := range roles {
		out[i] = r.Role
	}
	return out
}

// laneOf is a role's human lane name, falling back to the role key itself so a
// message never comes out blank.
func laneOf(roles []model.RolePair, role string) string {
	for _, r := range roles {
		if r.Role == role && r.Lane != "" {
			return r.Lane
		}
	}
	return role
}
