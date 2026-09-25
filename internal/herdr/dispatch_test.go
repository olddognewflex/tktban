package herdr

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSlugFromSummary(t *testing.T) {
	cases := []struct {
		name    string
		summary string
		want    string
	}{
		{"plain words", "Dispatch a ticket", "dispatch-a-ticket"},
		{"case folded", "Dispatch A Ticket", "dispatch-a-ticket"},
		{"punctuation collapses", "fix: the thing (again!)", "fix-the-thing-again"},
		{"leading and trailing junk", "  --hello--  ", "hello"},
		{"underscores and slashes separate", "a_b/c.d", "a-b-c-d"},
		{"digits survive", "TKB 25 dry run", "tkb-25-dry-run"},
		{"capped at five words", "one two three four five six seven", "one-two-three-four-five"},
		// Only ASCII [a-z0-9] survives: a branch name is a git ref people
		// type and paste, and KeysFromBranch has to read a key out of it.
		{"unicode is dropped, not transliterated", "Grüße, Welt!", "gr-e-welt"},
		{"all unicode slugifies to empty", "日本語テキスト", ""},
		{"emoji only", "🚀🚀", ""},
		{"punctuation only", "!!! ??? ...", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := SlugFromSummary(c.summary); got != c.want {
			t.Errorf("%s: SlugFromSummary(%q) = %q, want %q", c.name, c.summary, got, c.want)
		}
	}
}

// A long summary must be capped by both rules, and must never end on a
// separator — a branch ending in "-" is ugly where it shows and awkward where
// it is typed.
func TestSlugFromSummaryLongSummaryCapped(t *testing.T) {
	long := strings.Repeat("averylongwordindeed ", 10) // 200 characters
	got := SlugFromSummary(long)
	if len(got) > slugMax {
		t.Fatalf("slug is %d chars (%q), want at most %d", len(got), got, slugMax)
	}
	if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
		t.Fatalf("slug %q starts or ends on a separator", got)
	}
	if strings.Contains(got, "--") {
		t.Fatalf("slug %q has a doubled separator", got)
	}
	// One 19-character word repeated: five of them exceed 40 chars, so the
	// character cap is what bites, and it must not leave a dangling dash.
	if got != "averylongwordindeed-averylongwordindeed" {
		t.Fatalf("slug = %q", got)
	}
}

func TestRenderBranch(t *testing.T) {
	const fmtStr = "feature/{key-lower}-{slug}"
	cases := []struct {
		name   string
		format string
		key    string
		slug   string
		want   string
	}{
		{"normal", fmtStr, "TKB-25", "dry-run", "feature/tkb-25-dry-run"},
		// The one that matters: an empty slug must not leave a trailing dash.
		{"empty slug", fmtStr, "TKB-25", "", "feature/tkb-25"},
		{"hotfix format", "hotfix/{key-lower}-{slug}", "TKB-9", "oops", "hotfix/tkb-9-oops"},
		{"key placeholder", "wip/{key}/{slug}", "TKB-25", "x", "wip/TKB-25/x"},
		{"upper placeholder", "{key-upper}-{slug}", "tkb-25", "x", "TKB-25-x"},
		{"no slug placeholder", "feature/{key-lower}", "TKB-25", "ignored", "feature/tkb-25"},
		{"doubled separators collapse", "feature/{slug}--{key-lower}", "TKB-25", "a", "feature/a-tkb-25"},
		{"empty slug mid format", "feature/{slug}/{key-lower}", "TKB-25", "", "feature/tkb-25"},
	}
	for _, c := range cases {
		if got := RenderBranch(c.format, c.key, c.slug); got != c.want {
			t.Errorf("%s: RenderBranch(%q, %q, %q) = %q, want %q",
				c.name, c.format, c.key, c.slug, got, c.want)
		}
	}
}

// A branch the board could not read a key back out of is refused up front:
// the ⚙ badge and the o jump both work by reading a pane's branch, so such an
// agent would be dispatched and then invisible.
func TestBranchNamesKey(t *testing.T) {
	cases := []struct {
		branch string
		key    string
		want   bool
	}{
		{"feature/tkb-25-dry-run", "TKB-25", true},
		{"feature/tkb-25", "TKB-25", true},
		{"feature/tkb-25-dry-run", "tkb-25", true}, // key case does not matter
		{"agents/tkb-25/work", "TKB-25", true},
		{"feature/dry-run", "TKB-25", false},        // no key at all
		{"feature/tkb-250-x", "TKB-25", false},      // a longer key is not this one
		{"feature/xtkb-25", "TKB-25", false},        // not at a segment start
		{"feature/dry-run-tkb-25", "TKB-25", false}, // key not first in its segment
		{"", "TKB-25", false},
	}
	for _, c := range cases {
		if got := BranchNamesKey(c.branch, c.key); got != c.want {
			t.Errorf("BranchNamesKey(%q, %q) = %v, want %v", c.branch, c.key, got, c.want)
		}
	}
}

// Every name must satisfy herdr's own rule: ^[a-z][a-z0-9_-]{0,31}$.
func conformsToHerdr(name string) bool {
	if name == "" || len(name) > agentNameMax {
		return false
	}
	if name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, r := range name[1:] {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func TestAgentName(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{"TKB-25", "tkb-25"},
		{"tkb-25", "tkb-25"},
		{"MY_PROJ-7", "my_proj-7"},
		{"TKB 25", "tkb25"}, // a space is not a legal name character
		{"tkb-25!", "tkb-25"},
	}
	for _, c := range cases {
		got := AgentName(c.key)
		if got != c.want {
			t.Errorf("AgentName(%q) = %q, want %q", c.key, got, c.want)
		}
		if !conformsToHerdr(got) {
			t.Errorf("AgentName(%q) = %q, which herdr would reject", c.key, got)
		}
	}
}

// Whatever a key looks like, the name has to be one herdr accepts — including
// keys no real board produces, because a name herdr rejects is an
// invalid_agent_name after the worktree already exists.
func TestAgentNameAlwaysConforms(t *testing.T) {
	keys := []string{
		"TKB-25",
		"",
		"-",
		"123",
		"123-456",
		"日本-1",
		"__--__",
		strings.Repeat("A", 80) + "-1",
		strings.Repeat("A", 31),
		strings.Repeat("A", 32),
	}
	for _, key := range keys {
		for n := 1; n <= 12; n++ {
			got := AgentNameN(key, n)
			if !conformsToHerdr(got) {
				t.Errorf("AgentNameN(%q, %d) = %q, which herdr would reject", key, n, got)
			}
		}
	}
}

// The -2 fallback shares the 32-character budget with the base, so a long key
// still yields distinct, legal names.
func TestAgentNameNTruncatesToMakeRoomForTheSuffix(t *testing.T) {
	long := strings.Repeat("a", 40)
	base := AgentNameN(long, 1)
	if len(base) != agentNameMax {
		t.Fatalf("base name is %d chars, want %d: %q", len(base), agentNameMax, base)
	}
	second := AgentNameN(long, 2)
	if len(second) != agentNameMax {
		t.Fatalf("second name is %d chars, want %d: %q", len(second), agentNameMax, second)
	}
	if !strings.HasSuffix(second, "-2") {
		t.Fatalf("second name %q does not carry the -2 suffix", second)
	}
	if second == base {
		t.Fatalf("the fallback name equals the base name (%q)", base)
	}
	if AgentNameN("TKB-25", 1) != AgentName("TKB-25") {
		t.Fatal("n=1 must be AgentName itself")
	}
	if got := AgentNameN("TKB-25", 3); got != "tkb-25-3" {
		t.Fatalf("AgentNameN(TKB-25, 3) = %q", got)
	}
	// A base that would end on a separator once truncated must not keep it.
	if got := AgentNameN("ab"+strings.Repeat("c", 29)+"-x", 2); strings.Contains(got, "--") {
		t.Fatalf("truncation left a doubled separator: %q", got)
	}
}

func TestBuildPlanIsPureAndFillsThePrompt(t *testing.T) {
	in := PlanInput{
		Key:        "tkb-25",
		Summary:    "  Dispatch a ticket to a herdr agent  ",
		Dir:        "/src/tktban",
		Repo:       "olddognewflex/tktban",
		BranchFmt:  "feature/{key-lower}-{slug}",
		Base:       "main",
		SourceRole: "todo",
		SourceLane: "To Do",
		TargetRole: "in_progress",
		TargetLane: "In Progress",
		AgentKind:  "claude",
		AgentArgs:  []string{"--flag"},
	}
	p := BuildPlan(in)
	if p.Key != "TKB-25" {
		t.Errorf("key = %q, want the board's uppercase spelling", p.Key)
	}
	if p.Summary != "Dispatch a ticket to a herdr agent" {
		t.Errorf("summary = %q, want it trimmed", p.Summary)
	}
	if p.Branch != "feature/tkb-25-dispatch-a-ticket-to-a" {
		t.Errorf("branch = %q", p.Branch)
	}
	if p.AgentName != "tkb-25" {
		t.Errorf("agent name = %q", p.AgentName)
	}
	for _, want := range []string{"TKB-25", "Dispatch a ticket", p.Branch, "In Progress"} {
		if !strings.Contains(p.Prompt, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p.Prompt)
		}
	}
	if strings.Contains(p.Prompt, "{") {
		t.Errorf("prompt still has an unfilled placeholder:\n%s", p.Prompt)
	}
	// Pure: same input, same plan.
	if again := BuildPlan(in); again.Branch != p.Branch || again.Prompt != p.Prompt || again.AgentName != p.AgentName {
		t.Fatal("BuildPlan is not deterministic")
	}
}

func TestBuildPlanCustomPromptAndEmptySlug(t *testing.T) {
	p := BuildPlan(PlanInput{
		Key:        "TKB-25",
		Summary:    "日本語",
		BranchFmt:  "feature/{key-lower}-{slug}",
		TargetRole: "in_progress",
		Prompt:     "go: {key} on {branch} ({lane})",
	})
	if p.Branch != "feature/tkb-25" {
		t.Errorf("branch = %q, want no trailing separator", p.Branch)
	}
	// No lane given: the prompt falls back to the role rather than a blank.
	if p.Prompt != "go: TKB-25 on feature/tkb-25 (in_progress)" {
		t.Errorf("prompt = %q", p.Prompt)
	}
	// A prompt that is only whitespace is no prompt at all.
	blank := BuildPlan(PlanInput{Key: "TKB-25", Prompt: "   \n "})
	if !strings.Contains(blank.Prompt, "Work ticket TKB-25") {
		t.Errorf("a blank prompt setting did not fall back to the default: %q", blank.Prompt)
	}
}

// The important guard. A herdr *plugin* process given no context at all must
// refuse rather than dispatch against its own working directory: that is
// tktban's install checkout, and branching the wrong repository for a real
// ticket is not something a status message can undo.
//
// Deliberately narrower than SelectKey's refusal: HERDR_ENV is set in every
// herdr pane, so an ordinary terminal pane would be caught by the broader
// rule even though its working directory is exactly what the person meant.
func TestDispatchDir(t *testing.T) {
	cases := []struct {
		name  string
		env   Env
		cwd   string
		want  string
		wantB bool
	}{
		{"outside herdr uses the working directory", Env{}, "/work", "/work", true},
		{"outside herdr with no cwd has nothing", Env{}, "", "", false},
		{"focused pane wins", Env{
			InHerdr: true,
			Context: Context{FocusedPaneCwd: "/pane", WorkspaceCwd: "/ws"},
		}, "/work", "/pane", true},
		{"workspace root when there is no focused pane", Env{
			InHerdr: true,
			Context: Context{WorkspaceCwd: "/ws"},
		}, "/work", "/ws", true},
		{"in herdr with some context still falls back to cwd", Env{
			InHerdr: true,
			Context: Context{FocusedPaneCwd: "", WorkspaceCwd: "/ws"},
		}, "/work", "/ws", true},
		// A plugin process: herdr sets HERDR_PLUGIN_STATE_DIR (or the plugin
		// id) for these, and starts them in the plugin's own checkout.
		{"a plugin process with no context refuses", Env{
			InHerdr: true, StateDir: "/state/odnf.tktban",
		}, "/plugin/install/dir", "", false},
		{"the plugin id alone is enough to refuse", Env{
			InHerdr: true, PluginID: "odnf.tktban",
		}, "/plugin/install/dir", "", false},
		// A person's shell inside a herdr pane: no plugin markers, and the
		// working directory is exactly the repo they meant.
		{"a plain herdr terminal pane uses its working directory", Env{
			InHerdr: true, SocketPath: "/run/herdr.sock",
		}, "/src/tktban", "/src/tktban", true},
		{"a plugin process with context still uses it", Env{
			InHerdr: true, StateDir: "/state", Context: Context{WorkspaceCwd: "/ws"},
		}, "/plugin/install/dir", "/ws", true},
	}
	for _, c := range cases {
		got, ok := DispatchDir(c.env, c.cwd)
		if got != c.want || ok != c.wantB {
			t.Errorf("%s: DispatchDir = (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.wantB)
		}
	}
}

// fakeLister is a WorktreeLister that records every cwd it was asked about.
// It is the whole herdr surface a preflight has, which is the point: a
// preflight cannot create anything because it has nothing to create with.
type fakeLister struct {
	asked  []string
	result WorktreeListResult
	err    error
}

func (f *fakeLister) WorktreeList(_ context.Context, cwd string) (WorktreeListResult, error) {
	f.asked = append(f.asked, cwd)
	return f.result, f.err
}

func TestPreflightReadsOnceAndReportsTheRepo(t *testing.T) {
	f := &fakeLister{result: WorktreeListResult{
		Source: WorktreeSource{
			RepoKey:            "github.com/olddognewflex/tktban",
			RepoName:           "tktban",
			RepoRoot:           "/src/tktban",
			SourceCheckoutPath: "/src/tktban",
		},
		Worktrees: []WorktreeInfo{
			{Path: "/src/tktban", Branch: "main"},
			{Path: "/wt/tkb-24", Branch: "feature/tkb-24-x", IsLinkedWorktree: true},
		},
	}}
	plan := Plan{Key: "TKB-25", Branch: "feature/tkb-25-x", Dir: "/src/tktban"}
	got, err := Preflight(context.Background(), f, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.asked) != 1 || f.asked[0] != "/src/tktban" {
		t.Fatalf("worktree.list calls = %v, want exactly one for the plan's directory", f.asked)
	}
	if got.RepoRoot != "/src/tktban" || got.RepoName != "tktban" {
		t.Fatalf("preflight = %+v", got)
	}
	if got.ExistingWorktreePath != "" || got.SourceIsLinked {
		t.Fatalf("preflight invented a worktree or a linked source: %+v", got)
	}
}

// The branch already has a worktree: the preflight says where, so the create
// sequence can open it instead of cutting a second one.
func TestPreflightFindsAnExistingWorktreeForTheBranch(t *testing.T) {
	f := &fakeLister{result: WorktreeListResult{
		Source: WorktreeSource{RepoRoot: "/src/tktban", SourceCheckoutPath: "/src/tktban"},
		Worktrees: []WorktreeInfo{
			{Path: "/src/tktban", Branch: "main"},
			{Path: "/wt/tkb-25", Branch: "feature/tkb-25-x", IsLinkedWorktree: true, OpenWorkspaceID: "wE"},
		},
	}}
	got, err := Preflight(context.Background(), f, Plan{Branch: "feature/tkb-25-x", Dir: "/src/tktban"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExistingWorktreePath != "/wt/tkb-25" || got.ExistingWorkspaceID != "wE" {
		t.Fatalf("preflight = %+v, want the existing worktree", got)
	}
	if got.SourceIsLinked {
		t.Fatal("a linked worktree that is not the source must not read as a linked source")
	}
}

// A worktree with no branch (detached, bare) must not match a plan whose
// branch is "" — nor anything else.
func TestPreflightIgnoresBranchlessWorktrees(t *testing.T) {
	f := &fakeLister{result: WorktreeListResult{
		Worktrees: []WorktreeInfo{{Path: "/wt/detached", Branch: ""}},
	}}
	got, err := Preflight(context.Background(), f, Plan{Branch: "", Dir: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExistingWorktreePath != "" {
		t.Fatalf("a branchless worktree matched an empty plan branch: %+v", got)
	}
}

func TestPreflightMapsHerdrRefusals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"not a git work tree", &APIError{Code: CodeNotGitWorktree, Message: "not a git work tree"}, ErrNotGitWorktree},
		{"linked worktree source", &APIError{Code: CodeLinkedWorktreeSource, Message: "source is a linked worktree"}, ErrLinkedWorktreeSource},
	}
	for _, c := range cases {
		f := &fakeLister{err: c.err}
		got, err := Preflight(context.Background(), f, Plan{Dir: "/x"})
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
		if got != (PreflightResult{}) {
			t.Errorf("%s: a refused preflight returned %+v", c.name, got)
		}
	}

	// A code the UI does not special-case comes back as itself, so it is
	// reported rather than mistaken for one of the two above.
	other := &APIError{Code: CodeWorktreeOperationInProgress, Message: "busy"}
	_, err := Preflight(context.Background(), &fakeLister{err: other}, Plan{Dir: "/x"})
	if !errors.Is(err, other) {
		t.Fatalf("err = %v, want the herdr error itself", err)
	}
	if errors.Is(err, ErrNotGitWorktree) || errors.Is(err, ErrLinkedWorktreeSource) {
		t.Fatalf("an unrelated code mapped to a typed refusal: %v", err)
	}
}

// herdr reports a source checkout that is itself a linked worktree in the
// listing as well as refusing a create with linked_worktree_source, so the
// dispatch is refused before anything is created.
func TestPreflightRefusesALinkedSourceFromTheListing(t *testing.T) {
	f := &fakeLister{result: WorktreeListResult{
		Source: WorktreeSource{RepoRoot: "/src/tktban", SourceCheckoutPath: "/wt/tkb-24"},
		Worktrees: []WorktreeInfo{
			{Path: "/src/tktban", Branch: "main"},
			{Path: "/wt/tkb-24", Branch: "feature/tkb-24-x", IsLinkedWorktree: true},
		},
	}}
	got, e := Preflight(context.Background(), f, Plan{Branch: "feature/tkb-25-x", Dir: "/wt/tkb-24"})
	if !errors.Is(e, ErrLinkedWorktreeSource) {
		t.Fatalf("err = %v, want ErrLinkedWorktreeSource", e)
	}
	// Every refusal returns the zero value, so nothing can read a repo root
	// off a preflight that refused.
	if got != (PreflightResult{}) {
		t.Fatalf("a refused preflight returned %+v", got)
	}
}

// IsPlugin is what separates "herdr started this" from "someone typed this in
// a herdr pane", and DispatchDir's refusal turns on it.
func TestEnvIsPlugin(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want bool
	}{
		{"outside herdr", Env{StateDir: "/state"}, false},
		{"plain herdr pane", Env{InHerdr: true, SocketPath: "/run/herdr.sock"}, false},
		{"plugin with a state dir", Env{InHerdr: true, StateDir: "/state"}, true},
		{"plugin with only an id", Env{InHerdr: true, PluginID: "odnf.tktban"}, true},
	}
	for _, c := range cases {
		if got := c.env.IsPlugin(); got != c.want {
			t.Errorf("%s: IsPlugin = %v, want %v", c.name, got, c.want)
		}
	}
}

// FromEnv has to read the plugin id, or IsPlugin can never see it.
func TestFromEnvReadsThePluginID(t *testing.T) {
	e := FromEnv(env(map[string]string{
		"HERDR_ENV":       "1",
		"HERDR_PLUGIN_ID": "odnf.tktban",
	}))
	if e.PluginID != "odnf.tktban" {
		t.Fatalf("PluginID = %q", e.PluginID)
	}
	if !e.IsPlugin() {
		t.Fatal("a process with a plugin id is a plugin process")
	}
}

func TestErrorCode(t *testing.T) {
	if got := ErrorCode(&APIError{Code: CodeAgentPaneBusy}); got != "agent_pane_busy" {
		t.Errorf("ErrorCode = %q", got)
	}
	if got := ErrorCode(errors.New("dial unix: no such file")); got != "" {
		t.Errorf("a non-herdr error has no code, got %q", got)
	}
	if got := ErrorCode(nil); got != "" {
		t.Errorf("ErrorCode(nil) = %q", got)
	}
}
