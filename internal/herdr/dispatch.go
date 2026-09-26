package herdr

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Dispatch is handing one ticket to a herdr agent: a branch, a worktree for it,
// an agent started in that worktree's pane, and a first prompt.
//
// The file is in two halves. Everything up to "the create sequence" is pure or
// read-only: building a plan and preflighting it creates nothing, which is what
// lets the confirm dialog show a whole dispatch before any of it happens.
// Everything after it acts on a plan, and every herdr parameter, error code and
// retry decision lives there rather than in the board.
//
// The plan is built once, off a card, and carried whole. Nothing downstream
// re-reads the board selection, so an auto-refresh landing mid-flight cannot
// re-point a dispatch at a different ticket.

// ---- slugs and branches ----

// slugMax and slugWords cap a slug: enough of the summary to recognise the
// branch, not so much that the branch name is a paragraph.
const (
	slugMax   = 40
	slugWords = 5
)

// SlugFromSummary turns a ticket summary into the {slug} half of a branch
// name: lowercased, every run of non-alphanumeric characters collapsed to a
// single '-', capped at slugWords words and slugMax characters, with no
// leading or trailing separator.
//
// Only ASCII [a-z0-9] survives. A branch name is a git ref that people type,
// paste into shells and read in a pane title, and KeysFromBranch has to find
// the ticket key in it, so non-ASCII is dropped rather than transliterated: a
// summary that is entirely non-ASCII slugifies to "", which RenderBranch
// handles by leaving the slug off altogether.
func SlugFromSummary(summary string) string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(summary) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	if len(words) > slugWords {
		words = words[:slugWords]
	}
	slug := strings.Join(words, "-")
	if len(slug) > slugMax {
		slug = slug[:slugMax]
	}
	return strings.Trim(slug, "-")
}

// RenderBranch fills a tkt branch_fmt ("feature/{key-lower}-{slug}") in Go.
//
// tkt's own `tkt cfg vcs.branch_fmt --ticket X` renders the format without a
// slug, which leaves a branch ending in a bare separator ("feature/tkb-25-"),
// so the caller owns slugification either way. An empty slug is handled here:
// runs of separators are collapsed and trailing ones are trimmed, so a
// summary that slugifies to nothing gives "feature/tkb-25" and not
// "feature/tkb-25-".
func RenderBranch(format, key, slug string) string {
	rep := strings.NewReplacer(
		"{key-lower}", strings.ToLower(key),
		"{key}", key,
		"{key-upper}", strings.ToUpper(key),
		"{slug}", slug,
	)
	out := rep.Replace(format)
	out = collapseSeps.ReplaceAllString(out, "$1")
	return strings.Trim(out, "-_./")
}

// collapseSeps matches a run of branch separators, keeping the first.
var collapseSeps = regexp.MustCompile(`([-_./])[-_./]+`)

// BranchNamesKey reports whether branch names key the way the board reads
// branches — through KeysFromBranch, the same function that joins herdr's
// agent panes to tickets.
//
// A dispatch refuses a branch that fails this, up front. The badge on the card
// and the o jump both work by reading a pane's branch back, so a branch_fmt
// with no {key-lower} in it would produce an agent the board could never badge
// or jump to: dispatched and then invisible, which is worse than refused.
func BranchNamesKey(branch, key string) bool {
	return slices.Contains(KeysFromBranch(branch), strings.ToUpper(key))
}

// ---- agent names ----

// agentNameMax is herdr's own cap: a name must match ^[a-z][a-z0-9_-]{0,31}$
// and be unique among live agents.
const agentNameMax = 32

// AgentName is the herdr agent name for a ticket key: the key lowercased,
// with anything herdr would reject dropped. "TKB-25" gives "tkb-25", which is
// what makes an agent recognisable in herdr's own agent list and sidebar.
//
// A key that cannot start a herdr name (one that does not begin with a letter,
// which no real ticket key does) is prefixed rather than rejected, so this
// always returns a name herdr will accept.
func AgentName(key string) string { return AgentNameN(key, 1) }

// AgentNameN is the nth candidate name for a key: n == 1 is AgentName, and
// each later n appends "-n" ("tkb-25", "tkb-25-2", "tkb-25-3"). The suffix
// shares the 32-character budget — the base is shortened to make room — so
// every candidate is a name herdr accepts.
//
// It exists for agent_name_taken: a second agent on the same ticket (an
// earlier one still live in another pane) needs a name of its own.
func AgentNameN(key string, n int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	base := strings.TrimLeft(b.String(), "-_0123456789")
	if base == "" {
		base = "agent"
	}
	suffix := ""
	if n > 1 {
		suffix = "-" + strconv.Itoa(n)
	}
	if room := agentNameMax - len(suffix); len(base) > room {
		base = base[:room]
	}
	return strings.TrimRight(base, "-_") + suffix
}

// ---- the plan ----

// DefaultPrompt is the first prompt a dispatched agent gets when the
// dispatch_prompt setting is empty. It says which ticket and how to read it,
// and nothing about how to do the work: that is the ticket's job.
const DefaultPrompt = "Work ticket {key}: {summary}\n\n" +
	"Read it with `tkt view {key}` first. You are on branch {branch}; " +
	"the ticket moves to {lane} when you start."

// Plan is everything a dispatch does, as data. It is what the confirm modal
// renders, and what the create sequence consumes — the same value, unchanged,
// so nothing is created that nobody was shown.
type Plan struct {
	Key     string // the ticket, in the board's spelling (uppercase)
	Summary string
	Branch  string // rendered from the repo's branch_fmt
	Base    string // the branch to cut from (vcs.default_branch)
	Dir     string // the checkout the dispatch works in; herdr resolves the repo from it
	Repo    string // the repo as the tkt config names it, e.g. "owner/name"

	// SourceRole / SourceLane are the column the card is in; TargetRole /
	// TargetLane are where the ticket moves when the agent picks it up: the
	// first agent-owned transition out of the source role.
	SourceRole string
	SourceLane string
	TargetRole string
	TargetLane string

	AgentKind string   // herdr agent kind, e.g. "claude"
	AgentName string   // AgentName(Key)
	AgentArgs []string // extra argv for the agent, from settings
	Prompt    string   // the first prompt, already filled in
}

// PlanInput is what BuildPlan needs. It is all strings and slices on purpose:
// the caller has already read the tkt config and the settings, so building a
// plan touches nothing.
type PlanInput struct {
	Key        string
	Summary    string
	Dir        string
	Repo       string
	BranchFmt  string
	Base       string
	SourceRole string
	SourceLane string
	TargetRole string
	TargetLane string
	AgentKind  string
	AgentArgs  []string
	Prompt     string // "" takes DefaultPrompt
}

// BuildPlan assembles a Plan. Pure: same input, same plan, no I/O.
func BuildPlan(in PlanInput) Plan {
	key := strings.ToUpper(strings.TrimSpace(in.Key))
	branch := RenderBranch(in.BranchFmt, key, SlugFromSummary(in.Summary))
	p := Plan{
		Key:        key,
		Summary:    strings.TrimSpace(in.Summary),
		Branch:     branch,
		Base:       in.Base,
		Dir:        in.Dir,
		Repo:       in.Repo,
		SourceRole: in.SourceRole,
		SourceLane: in.SourceLane,
		TargetRole: in.TargetRole,
		TargetLane: in.TargetLane,
		AgentKind:  in.AgentKind,
		AgentName:  AgentName(key),
		AgentArgs:  in.AgentArgs,
	}
	tmpl := in.Prompt
	if strings.TrimSpace(tmpl) == "" {
		tmpl = DefaultPrompt
	}
	lane := p.TargetLane
	if lane == "" {
		lane = p.TargetRole
	}
	p.Prompt = strings.NewReplacer(
		"{key}", p.Key,
		"{summary}", p.Summary,
		"{branch}", p.Branch,
		"{base}", p.Base,
		"{role}", p.TargetRole,
		"{lane}", lane,
	).Replace(tmpl)
	return p
}

// ---- where a dispatch runs ----

// DispatchDir is the checkout a dispatch works in: the herdr context's focused
// pane, then its workspace root, then the process working directory.
//
// The refusal: a herdr *plugin* process that was given no context at all does
// not fall back to its working directory. herdr starts a plugin pane in the
// plugin's own install checkout unless the launcher passes --cwd (the TKB-23
// finding), so falling back would dispatch against tktban's own checkout —
// and two repositories here share one board directory, so the result could be
// a real branch and a real worktree of the wrong repository for a real
// ticket. A status message is a cheap price for never doing that.
//
// It is deliberately narrower than SelectKey's otherwise identical refusal,
// which turns on InHerdr alone. HERDR_ENV is set in every herdr pane, so that
// rule also refuses `tktban` typed in an ordinary herdr terminal — where the
// working directory is exactly what the person meant, and refusing is simply
// wrong. Only a process carrying herdr's plugin markers (Env.IsPlugin) can
// have the install-directory problem, so only that one is refused. The
// asymmetry is on purpose: mis-selecting a card costs a keystroke, and
// mis-resolving a repository costs a worktree in the wrong repo.
//
// ok is false when there is nothing safe to dispatch from.
func DispatchDir(e Env, cwd string) (string, bool) {
	dirs := []string{e.Context.FocusedPaneCwd, e.Context.WorkspaceCwd}
	if !e.IsPlugin() || dirs[0] != "" || dirs[1] != "" {
		dirs = append(dirs, cwd)
	}
	for _, dir := range dirs {
		if dir != "" {
			return dir, true
		}
	}
	return "", false
}

// ---- preflight ----

// Typed refusals from Preflight. They are sentinels rather than messages so
// the UI owns the wording, and errors.Is finds them through a wrap.
var (
	// ErrNotGitWorktree means the dispatch directory is not a git checkout.
	ErrNotGitWorktree = errors.New("herdr: not a git work tree")
	// ErrLinkedWorktreeSource means the dispatch directory is itself a linked
	// worktree. herdr will not cut a worktree from one, and a dispatch from a
	// worktree is almost certainly a mis-aimed board anyway.
	ErrLinkedWorktreeSource = errors.New("herdr: dispatch source is itself a linked worktree")
)

// WorktreeLister is the read-only slice of the herdr client a preflight needs.
// A narrow interface keeps the fake in the tests to one method, and keeps it
// impossible for a preflight to create anything.
type WorktreeLister interface {
	WorktreeList(ctx context.Context, cwd string) (WorktreeListResult, error)
}

// PreflightResult is what one worktree.list told us about a plan.
type PreflightResult struct {
	// RepoRoot is the repository herdr resolved from the plan's directory —
	// the thing to show a person before they agree to branch it.
	RepoRoot string
	// RepoName is herdr's own label for that repository.
	RepoName string
	// ExistingWorktreePath is set when the plan's branch is already checked
	// out in a worktree. A dispatch then opens that worktree instead of
	// creating one, so a second dispatch of the same ticket rejoins the first.
	ExistingWorktreePath string
	// ExistingWorkspaceID is the herdr workspace that worktree is open in, if
	// any.
	ExistingWorkspaceID string
	// SourceIsLinked says the plan's own directory is a linked worktree. herdr
	// reports this in the listing as well as refusing a create with
	// linked_worktree_source, so it is caught before anything is created.
	SourceIsLinked bool
}

// Preflight checks a plan against herdr with exactly one read: worktree.list
// for the plan's directory. It creates nothing and changes nothing.
//
// not_git_worktree and linked_worktree_source come back as the typed refusals
// above; every other herdr error is returned as it is, for the caller to
// report generically rather than guess at.
func Preflight(ctx context.Context, c WorktreeLister, p Plan) (PreflightResult, error) {
	list, err := c.WorktreeList(ctx, p.Dir)
	if err != nil {
		switch ErrorCode(err) {
		case CodeNotGitWorktree:
			return PreflightResult{}, ErrNotGitWorktree
		case CodeLinkedWorktreeSource:
			return PreflightResult{}, ErrLinkedWorktreeSource
		}
		return PreflightResult{}, err
	}
	out := PreflightResult{
		RepoRoot: list.Source.RepoRoot,
		RepoName: list.Source.RepoName,
	}
	for _, wt := range list.Worktrees {
		if wt.Branch != "" && wt.Branch == p.Branch {
			out.ExistingWorktreePath = wt.Path
			out.ExistingWorkspaceID = wt.OpenWorkspaceID
		}
		// The source checkout appearing in its own listing as a linked
		// worktree is how herdr says "you are inside a worktree already".
		if wt.IsLinkedWorktree && wt.Path != "" && wt.Path == list.Source.SourceCheckoutPath {
			out.SourceIsLinked = true
		}
	}
	if out.SourceIsLinked {
		// The zero value, like every other refusal here: a caller that has an
		// error has nothing it may act on, and handing it a half-filled
		// result only invites someone to read RepoRoot off a refusal.
		return PreflightResult{}, ErrLinkedWorktreeSource
	}
	return out, nil
}

// ---- the create sequence ----
//
// Everything above builds a plan and reads. Everything below acts on one.
//
// The sequence lives here, and not in the board, for one reason: the board
// must hold no protocol knowledge. Which parameters herdr wants, which of
// worktree.create and worktree.open to call, which error codes are worth a
// retry and which pane an agent is started into are all decisions about
// herdr's API, and they belong next to the client that speaks it. The board
// drives the sequence one step at a time and renders the typed result.
//
// What is deliberately NOT here: any way to undo a step. There is no
// worktree.remove, no pane.close and no workspace.close anywhere in
// DispatchClient, so no failure path can reach one. A worktree that survives a
// failed agent start is left exactly where it is and recorded on the ticket;
// a board is not allowed to delete a checkout that might hold work.

// AgentStartTimeoutMS is the startup budget tktban gives herdr for
// agent.start: 20s, comfortably inside herdr's own 3000 < t <= 300000 range.
//
// It is deliberately shorter than the context the board wraps the call in (25s
// there), so herdr's own timeout is the one that fires. A herdr timeout comes
// back as a typed error reply naming what failed; our context expiring comes
// back as a closed socket and a bare deadline, which tells nobody anything.
const AgentStartTimeoutMS = 20000

// Sleeper waits for d, or gives up early when ctx ends. It is injected so the
// retry budget below can be tested without spending it: SleepCtx is the real
// one, and the tests record the delays instead of serving them.
type Sleeper func(ctx context.Context, d time.Duration) error

// agentStartBackoff is the wait before each agent.start retry — five retries,
// six attempts in all, about 3.1s of waiting at worst.
//
// The reason it exists at all: herdr's worktree.create opens the pane at a
// login shell and returns as soon as the pane is there, while agent.start
// needs that shell to have reached an interactive prompt and answers
// agent_pane_busy until it has. A plain shell is ready almost at once; a shell
// with a prompt framework, a version manager and a plugin loader in its rc
// files takes about half a second, and the tail is long. So the first retries
// are quick (a fast shell costs 100ms, not a fixed second) and the last ones
// are patient. Five doublings from 100ms covers the slow shell with room to
// spare, and giving up is safe: the pane and the worktree are already there,
// and the person can start the agent in them by hand.
var agentStartBackoff = []time.Duration{
	100 * time.Millisecond,
	200 * time.Millisecond,
	400 * time.Millisecond,
	800 * time.Millisecond,
	1600 * time.Millisecond,
}

// AgentStarter is the slice of the client StartAgentWithRetry needs.
type AgentStarter interface {
	AgentStart(ctx context.Context, p AgentStartParams) (AgentStartResult, error)
}

// AgentStartStats is what the retry loop did, for a status line and a comment
// that can say "it took four tries" rather than leaving a person wondering
// why a dispatch sat there for three seconds.
type AgentStartStats struct {
	Name     string        // the name the agent was finally started under
	Attempts int           // agent.start calls made, including the one that worked
	Busy     int           // agent_pane_busy replies
	Renamed  int           // agent_name_taken replies that produced a new name
	Waited   time.Duration // total time spent in the backoff
}

// StartAgentWithRetry starts p's agent in paneID, retrying the two failures
// that are worth retrying and nothing else.
//
// agent_pane_busy means "the pane's shell is not at a prompt yet", which is
// the expected answer immediately after a worktree is created — see
// agentStartBackoff. agent_name_taken means a live agent already holds the
// name, which happens when an earlier agent on this ticket is still running in
// another pane; the next candidate from AgentNameN is tried exactly once, and
// it spends a slot of the same budget rather than getting one of its own. Two
// name collisions in a row is a signal to stop, not to keep counting upwards.
//
// Every other code — invalid_agent_name, invalid_agent_argument,
// agent_blocked, a dial failure — returns at once. They will not come right by
// being asked again.
//
// Giving up is never a rollback. The worktree and its pane stay; the caller
// records them and says the agent did not start.
func StartAgentWithRetry(ctx context.Context, c AgentStarter, p Plan, paneID string, sleep Sleeper) (AgentStartResult, AgentStartStats, error) {
	if sleep == nil {
		sleep = SleepCtx
	}
	name := p.AgentName
	if name == "" {
		name = AgentName(p.Key)
	}
	stats := AgentStartStats{Name: name}
	renamed := false
	for attempt := 0; ; attempt++ {
		res, err := c.AgentStart(ctx, AgentStartParams{
			Name:      name,
			Kind:      p.AgentKind,
			PaneID:    paneID,
			Args:      p.AgentArgs,
			TimeoutMS: AgentStartTimeoutMS,
		})
		stats.Attempts = attempt + 1
		stats.Name = name
		if err == nil {
			return res, stats, nil
		}
		switch ErrorCode(err) {
		case CodeAgentPaneBusy:
			stats.Busy++
		case CodeAgentNameTaken:
			if renamed {
				// A second collision is not a counting problem. Something
				// else is holding these names and walking up the sequence
				// would just make more of them.
				return AgentStartResult{}, stats, err
			}
			renamed = true
			stats.Renamed++
			name = AgentNameN(p.Key, 2)
		default:
			return AgentStartResult{}, stats, err
		}
		if attempt >= len(agentStartBackoff) {
			return AgentStartResult{}, stats, err // the budget is spent
		}
		wait := agentStartBackoff[attempt]
		if serr := sleep(ctx, wait); serr != nil {
			// The context ended mid-backoff. Both facts matter — the deadline
			// is why we stopped, the herdr code is what we had last seen — so
			// both are wrapped and errors.Is finds either.
			return AgentStartResult{}, stats, fmt.Errorf("%w (last herdr reply: %w)", serr, err)
		}
		stats.Waited += wait
	}
}

// ---- the whole sequence ----

// Stage is one step of a dispatch, in the order they run.
type Stage int

const (
	// StageDone means there is nothing left to do — either every step ran, or
	// one of them failed and the sequence stops where it stopped.
	//
	// It is first so that it is the zero value, which is what lets
	// DispatchResult.Failed mean "nothing failed" without anyone having to
	// remember to initialise it.
	StageDone Stage = iota
	// StageWorktree creates the worktree, or opens the one the branch has.
	StageWorktree
	// StageAgent starts the agent in the worktree's root pane.
	StageAgent
	// StagePrompt hands the started agent its first prompt.
	StagePrompt
)

// String is the herdr method the stage calls, so a status line and a ticket
// comment can name the step in herdr's own vocabulary rather than inventing a
// second set of names for the same three calls. StageWorktree is the one that
// is two methods, and which of them ran is on the result (Reused).
func (s Stage) String() string {
	switch s {
	case StageWorktree:
		return "worktree.create"
	case StageAgent:
		return "agent.start"
	case StagePrompt:
		return "agent.prompt"
	case StageDone:
		return "done"
	}
	return "unknown"
}

// DispatchClient is the herdr surface a create sequence has, and it is
// exhaustive on purpose.
//
// There is no worktree.remove, no pane.close and no workspace.close in it. A
// dispatch that fails half-way therefore cannot tidy up after itself even by
// accident: a worktree may hold a checkout, a stash or an edited file, and a
// board is not the thing that gets to decide those are disposable. The failure
// path records what exists and says so; the person removes it if they want it
// gone. A test walks this interface's own method set to keep it that way.
type DispatchClient interface {
	WorktreeLister
	WorktreeCreate(ctx context.Context, p WorktreeCreateParams) (WorktreeResult, error)
	WorktreeOpen(ctx context.Context, p WorktreeOpenParams) (WorktreeResult, error)
	AgentStarter
	AgentPrompt(ctx context.Context, p AgentPromptParams) error
}

// DispatchResult is how far a dispatch got and what each step produced.
//
// Every field is filled in as soon as it is known, not only on success, and
// that is the point of the type: the ticket comment and the board's status
// line are both built from it, and both of them are most needed when the
// sequence stopped half-way. A failed agent start has to be able to say which
// worktree, which workspace and which pane are now sitting there.
type DispatchResult struct {
	// The worktree step.
	Created      bool   // a worktree is there (created or opened)
	Reused       bool   // worktree.open was called, not worktree.create
	AlreadyOpen  bool   // herdr already had that worktree open in a workspace
	WorktreePath string // where herdr put it
	WorkspaceID  string
	TabID        string
	PaneID       string // root_pane: the pane the agent is started in

	// The agent step.
	Started   bool     // agent.start succeeded: the agent is dispatched
	AgentName string   // the name it was started under, which may not be Plan's
	Argv      []string // the argv herdr actually ran
	Start     AgentStartStats

	// The prompt step.
	Prompted bool

	// Failed is the stage that failed; StageDone (the zero value) when none
	// did. Err is that stage's error, and ErrorCode(Err) names it when herdr
	// did.
	//
	// Failed and Created are independent, which matters: herdr can create a
	// worktree and then answer with no root pane to start an agent in, so a
	// StageWorktree failure does not mean nothing was created. Callers word
	// "nothing was created" off Created, never off the stage.
	Failed Stage
	Err    error
}

// Sequence is a dispatch in progress: the plan, what the preflight found, and
// the result so far. It is a value — Next returns the next one rather than
// mutating this one — so the board can carry it on a message without two
// updates ever sharing state.
type Sequence struct {
	Plan Plan
	Pre  PreflightResult
	Res  DispatchResult
}

// NewSequence starts a dispatch of plan. Nothing has happened yet.
func NewSequence(plan Plan, pre PreflightResult) Sequence {
	return Sequence{Plan: plan, Pre: pre}
}

// Stage reports the step Next would run: StageDone once the prompt has landed
// or any step has failed. Nothing retries a step that failed, and in
// particular nothing re-runs the worktree step — a retry there could leave a
// second worktree behind, which is the one mistake in this sequence that is
// expensive to undo.
func (s Sequence) Stage() Stage {
	switch {
	case s.Res.Err != nil:
		return StageDone
	case !s.Res.Created:
		return StageWorktree
	case !s.Res.Started:
		return StageAgent
	case !s.Res.Prompted:
		return StagePrompt
	}
	return StageDone
}

// Next runs exactly one step against c under ctx and returns the sequence that
// follows it. One step per call is what lets the caller give each step its own
// budget — 15s to cut a worktree is not 5s to type a prompt — and lets the
// confirm dialog say which one is running. Calling Next on a finished or failed
// sequence returns it unchanged.
func (s Sequence) Next(ctx context.Context, c DispatchClient, sleep Sleeper) Sequence {
	switch s.Stage() {
	case StageWorktree:
		return s.worktree(ctx, c)
	case StageAgent:
		return s.agent(ctx, c, sleep)
	case StagePrompt:
		return s.prompt(ctx, c)
	}
	return s
}

// worktree creates the worktree, or opens the one the branch already has.
//
// No path is sent: herdr owns worktree placement ([worktrees] directory,
// default ~/.herdr/worktrees), so a dispatched worktree lands beside the ones
// a person makes by hand. No trust_repository either — recording a repository
// as trusted is a write, and a workspace_trust_blocked refusal is a refusal to
// report, not an obstacle to route around. focus is sent as false because
// false is a decision: a dispatch does not steal the screen.
func (s Sequence) worktree(ctx context.Context, c DispatchClient) Sequence {
	var res WorktreeResult
	var err error
	if s.Pre.ExistingWorktreePath != "" {
		// The branch is already checked out somewhere. Opening it is how a
		// second dispatch of one ticket rejoins the first instead of asking
		// git for a branch it already has. No base: there is nothing to cut.
		s.Res.Reused = true
		res, err = c.WorktreeOpen(ctx, WorktreeOpenParams{
			Cwd:    s.Plan.Dir,
			Branch: s.Plan.Branch,
			Focus:  false,
		})
	} else {
		res, err = c.WorktreeCreate(ctx, WorktreeCreateParams{
			Cwd:    s.Plan.Dir,
			Branch: s.Plan.Branch,
			Base:   s.Plan.Base,
			// The label herdr gives the workspace and its tab. The ticket key
			// is what makes the workspace findable in herdr's own switcher,
			// and it is the same string the sidebar's $ticket token carries.
			Label: s.Plan.Key,
			Focus: false,
		})
	}
	if err != nil {
		s.Res.Failed, s.Res.Err = StageWorktree, err
		return s
	}
	s.Res.Created = true
	s.Res.AlreadyOpen = res.AlreadyOpen
	s.Res.WorktreePath = res.Worktree.Path
	s.Res.WorkspaceID = res.Workspace.WorkspaceID
	s.Res.TabID = res.Tab.TabID
	s.Res.PaneID = res.RootPane.PaneID
	if s.Res.PaneID == "" {
		// herdr's schema makes root_pane required, so this is herdr breaking
		// its own contract — but agent.start requires a pane_id, and sending
		// "" would be a confusing invalid_agent_argument three seconds later.
		// Say what is actually wrong, once, here.
		s.Res.Failed, s.Res.Err = StageWorktree, errors.New("herdr: worktree reply carried no root pane to start an agent in")
	}
	return s
}

// agent starts the agent in the worktree's root pane, with the retry budget
// StartAgentWithRetry documents.
func (s Sequence) agent(ctx context.Context, c DispatchClient, sleep Sleeper) Sequence {
	res, stats, err := StartAgentWithRetry(ctx, c, s.Plan, s.Res.PaneID, sleep)
	s.Res.Start = stats
	s.Res.AgentName = stats.Name
	if err != nil {
		s.Res.Failed, s.Res.Err = StageAgent, err
		return s
	}
	s.Res.Started = true
	s.Res.Argv = res.Argv
	return s
}

// prompt hands the started agent its first prompt.
//
// No wait: herdr's agent.prompt accepts a wait object that blocks until the
// agent reaches a status, and a board must never block on an agent. It is
// fire-and-forget by design — and if it does fail, the agent is already
// dispatched, which is why this is the one failure that still moves the lane.
func (s Sequence) prompt(ctx context.Context, c DispatchClient) Sequence {
	target := s.Res.AgentName
	if target == "" {
		// agent.prompt takes a pane id or an agent name; the pane is the one
		// thing we are certain herdr still knows by this point.
		target = s.Res.PaneID
	}
	if err := c.AgentPrompt(ctx, AgentPromptParams{Target: target, Text: s.Plan.Prompt}); err != nil {
		s.Res.Failed, s.Res.Err = StagePrompt, err
		return s
	}
	s.Res.Prompted = true
	return s
}
