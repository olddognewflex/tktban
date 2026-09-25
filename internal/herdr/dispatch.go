package herdr

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Dispatch is the plan for handing one ticket to a herdr agent: a branch, a
// worktree for it, an agent started in that worktree's pane, and a first
// prompt. Everything here is pure or read-only — building a plan and checking
// whether it could run creates nothing. The board's D key shows the plan and
// stops; the calls that act on it are wired up separately.
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

// Plan is everything a dispatch would do, as data. It is what the confirm
// modal renders, and what the create sequence will consume.
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
