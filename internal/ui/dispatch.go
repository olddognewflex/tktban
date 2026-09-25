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

// Dispatcher is the optional slice of a live source that can prepare a
// dispatch — a separate interface from LiveSource for the same reason
// PaneFocuser is: a source that only reads status still drives the badges, it
// just has no D key.
//
// Today it is one read. Creating the worktree and starting the agent land with
// the change that actually does them; this interface widens then, and the fake
// in the tests is what proves the dry run calls nothing else.
type Dispatcher interface {
	WorktreeList(ctx context.Context, cwd string) (herdr.WorktreeListResult, error)
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
		return m, m.setStatus("Dispatch is off (set dispatch = true in tktban's settings.toml)", "warn")
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
		return m, m.setStatus("Don't know which repo to dispatch "+key+" in", "warn")
	}
	target := dispatchTarget{
		key:       key,
		summary:   card.Summary,
		role:      m.columns[m.focusCol].Role,
		dir:       m.dispatchDir,
		roles:     m.roles,
		agentKind: m.dispatchAgentKind(),
		agentArgs: strings.Fields(str(m.settings["dispatch_args"])),
		prompt:    str(m.settings["dispatch_prompt"]),
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
		tk = tk.WithContext(ctx)

		// Both config readers are best-effort and answer a zero value for any
		// failure, a timeout included, so a timed-out read would otherwise
		// come out as "no branch_fmt configured". Say what happened instead.
		timedOut := func() *dispatchPrepMsg {
			if ctx.Err() == nil {
				return nil
			}
			return &dispatchPrepMsg{err: refuse("Timed out reading the tkt config for %s", t.key)}
		}

		vcs := tk.VCS()
		if out := timedOut(); out != nil {
			return *out
		}
		if vcs.BranchFmt == "" {
			return dispatchPrepMsg{err: refuse("No [vcs] branch_fmt in the tkt config, so there is no branch to cut")}
		}
		// Rendered here as well as inside BuildPlan. RenderBranch is pure, so
		// the two agree by construction, and checking it here keeps the
		// refusal ahead of the ownership read in the ladder.
		branch := herdr.RenderBranch(vcs.BranchFmt, t.key, herdr.SlugFromSummary(t.summary))
		if !herdr.BranchNamesKey(branch, t.key) {
			return dispatchPrepMsg{err: refuse(
				"branch_fmt %q doesn't name %s: the board could never badge or jump to its agent",
				vcs.BranchFmt, t.key)}
		}
		ownership := tk.BoardOwnership()
		if out := timedOut(); out != nil {
			return *out
		}
		targetRole, ok := tkt.AgentTarget(ownership, t.role, roleOrder(t.roles))
		if !ok {
			return dispatchPrepMsg{err: refuse("No agent-owned transition out of %s", laneOf(t.roles, t.role))}
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

// onDispatchResult closes the confirm modal and acts on the answer.
//
// This is the whole of the dry run: enter says what would have happened and
// nothing happens. No worktree, no agent, no transition, no comment — the only
// herdr call the D key makes at all is the worktree.list in the preflight
// above. esc says nothing, because cancelling a dialog needs no announcement.
//
// The plan arrives on the message rather than being rebuilt here, so whatever
// acts on it acts on exactly what the person was shown.
func (m Model) onDispatchResult(msg dispatchResultMsg) (tea.Model, tea.Cmd) {
	m.modal = nil
	if !msg.confirmed {
		return m, nil
	}
	// The keypress guard is not enough on its own. Live status keeps polling
	// the whole time the dialog is open, so an agent can appear on this
	// ticket between D and enter — someone else dispatching it, or a pane
	// checking the branch out by hand. Today that only changes what the board
	// says; once enter creates things it is what stops two agents racing one
	// branch, so the check is here, on the plan's own key, and not on
	// whatever the selection has become since.
	if _, live := herdr.PickPane(m.live.byKey[msg.plan.Key]); live {
		return m, m.setStatus(msg.plan.Key+" picked up an agent while the dialog was open (o focuses it)", "warn")
	}
	return m, m.setStatus("Dry run — dispatch lands in the next change; nothing was created for "+msg.plan.Key, "")
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
