package tkt

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olddognewflex/tktban/internal/model"
)

// fake is an injectable Runner: it records calls and replays a queue of
// responses (the last response repeats once exhausted), standing in for the
// Python tests' mocked subprocess.run.
type fake struct {
	calls     [][]string // each = bin + args
	envs      [][]string
	responses []resp
	i         int
}

type resp struct {
	stdout string
	stderr string
	code   int
	runErr error
}

func (f *fake) run(ctx context.Context, bin string, args, env []string) ([]byte, []byte, int, error) {
	f.calls = append(f.calls, append([]string{bin}, args...))
	f.envs = append(f.envs, env)
	r := f.responses[f.i]
	if f.i < len(f.responses)-1 {
		f.i++
	}
	return []byte(r.stdout), []byte(r.stderr), r.code, r.runErr
}

func (f *fake) lastArgv() []string { return f.calls[len(f.calls)-1] }
func (f *fake) lastEnv() []string  { return f.envs[len(f.envs)-1] }

func newFake(responses ...resp) (*Tkt, *fake) {
	f := &fake{responses: responses}
	return New("", "tkt").WithRunner(f.run), f
}

func TestRolesArgvAndParse(t *testing.T) {
	tk, f := newFake(resp{stdout: `{"todo": "To Do", "done": "Done"}`})
	roles, err := tk.Roles()
	if err != nil {
		t.Fatal(err)
	}
	want := []model.RolePair{{Role: "todo", Lane: "To Do"}, {Role: "done", Lane: "Done"}}
	if !reflect.DeepEqual(roles, want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	if got := f.lastArgv(); !reflect.DeepEqual(got, []string{"tkt", "cfg", "board.roles", "--json"}) {
		t.Fatalf("argv = %v", got)
	}
}

func TestConfigPassedViaEnvNotArgv(t *testing.T) {
	f := &fake{responses: []resp{{stdout: "[]"}}}
	tk := New("/x/.sdlc/config.toml", "tkt").WithRunner(f.run)
	if _, err := tk.ListAll(); err != nil {
		t.Fatal(err)
	}
	argv := f.lastArgv()
	if !reflect.DeepEqual(argv, []string{"tkt", "list", "--query", "all", "--json"}) {
		t.Fatalf("argv = %v", argv)
	}
	for _, a := range argv {
		if a == "--config" {
			t.Fatal("config must not travel as --config")
		}
	}
	if !envHas(f.lastEnv(), "TKT_CONFIG=/x/.sdlc/config.toml") {
		t.Fatalf("TKT_CONFIG not in env: %v", f.lastEnv())
	}
}

func TestNoConfigNoEnvOverride(t *testing.T) {
	tk, f := newFake(resp{stdout: "[]"})
	if _, err := tk.ListAll(); err != nil {
		t.Fatal(err)
	}
	if f.lastEnv() != nil {
		t.Fatalf("env should be nil without config, got %v", f.lastEnv())
	}
}

func TestListAllReturnsArray(t *testing.T) {
	tk, _ := newFake(resp{stdout: `[{"key": "TKT-1"}, {"key": "TKT-2"}]`})
	out, err := tk.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if out[0]["key"] != "TKT-1" || out[1]["key"] != "TKT-2" {
		t.Fatalf("out = %v", out)
	}
}

func TestCreateOmitsEmptyOptionals(t *testing.T) {
	tk, f := newFake(resp{stdout: `{"key": "TKT-9"}`})
	out, err := tk.Create("Task", "do a thing", CreateOpts{Priority: "High"})
	if err != nil {
		t.Fatal(err)
	}
	if out["key"] != "TKT-9" {
		t.Fatalf("key = %v", out["key"])
	}
	want := []string{"tkt", "create", "--type", "Task", "--summary", "do a thing", "--priority", "High", "--json"}
	if got := f.lastArgv(); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v", got)
	}
}

func TestEditArgvSendsOnlyProvidedFields(t *testing.T) {
	tk, f := newFake(resp{stdout: `{"key": "TKT-1"}`})
	summary, priority := "new", "High"
	_, err := tk.Edit("TKT-1", EditOpts{Summary: &summary, Priority: &priority, AddLabels: []string{"x"}, RemoveLabels: []string{"y"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tkt", "edit", "TKT-1", "--summary", "new", "--priority", "High", "--add-label", "x", "--remove-label", "y", "--json"}
	if got := f.lastArgv(); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v", got)
	}
}

func TestEditEmptyStringIsSentButNilIsOmitted(t *testing.T) {
	tk, f := newFake(resp{stdout: "{}"})
	empty := ""
	if _, err := tk.Edit("TKT-1", EditOpts{Assignee: &empty}); err != nil {
		t.Fatal(err)
	}
	argv := f.lastArgv()
	i := indexOf(argv, "--assignee")
	if i < 0 || argv[i+1] != "" {
		t.Fatalf("--assignee \"\" must be sent, argv = %v", argv)
	}
	if indexOf(argv, "--summary") >= 0 {
		t.Fatal("--summary must be omitted when nil")
	}
}

func TestLaneTimeIsReadOnlyAndParses(t *testing.T) {
	tk, f := newFake(resp{stdout: `[{"key": "TKT-1", "human": "6h 10m", "worklog_id": ""}]`})
	out, err := tk.LaneTime("TKT-1", "todo")
	if err != nil {
		t.Fatal(err)
	}
	if out["human"] != "6h 10m" || out["worklog_id"] != "" {
		t.Fatalf("out = %v", out)
	}
	want := []string{"tkt", "lane-time", "--keys", "TKT-1:todo", "--read-only", "--json"}
	if got := f.lastArgv(); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v", got)
	}
}

func TestLaneTimeBatchIsReadOnlyAndMapsByKey(t *testing.T) {
	tk, f := newFake(resp{stdout: `[{"key": "TKT-1", "human": "6h 10m"}, {"key": "TKT-2", "human": "1h 5m"}]`})
	out, err := tk.LaneTimeBatch([][2]string{{"TKT-1", "todo"}, {"TKT-2", "in_progress"}})
	if err != nil {
		t.Fatal(err)
	}
	if out["TKT-1"]["human"] != "6h 10m" || out["TKT-2"]["human"] != "1h 5m" {
		t.Fatalf("out = %v", out)
	}
	want := []string{"tkt", "lane-time", "--keys", "TKT-1:todo,TKT-2:in_progress", "--read-only", "--json"}
	if got := f.lastArgv(); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v", got)
	}
}

func TestLaneTimeReturnsNilOnNoHistory(t *testing.T) {
	tk, _ := newFake(resp{code: 3, stderr: "no entry into 'To Do' in history"})
	out, err := tk.LaneTime("TKT-1", "todo")
	if err != nil {
		t.Fatalf("benign no-history should not error: %v", err)
	}
	if out != nil {
		t.Fatalf("out should be nil, got %v", out)
	}
}

func TestLaneTimeReraisesGenuineError(t *testing.T) {
	tk, _ := newFake(resp{code: 2, stderr: "config error: bad board_dir"})
	if _, err := tk.LaneTime("TKT-1", "todo"); err == nil {
		t.Fatal("genuine error must not be masked as no-badge")
	}
}

func TestTransitionAndCommentArgv(t *testing.T) {
	tk, f := newFake(resp{stdout: "ok"})
	if err := tk.Transition("TKT-1", "review"); err != nil {
		t.Fatal(err)
	}
	if got := f.lastArgv(); !reflect.DeepEqual(got, []string{"tkt", "transition", "TKT-1", "review"}) {
		t.Fatalf("argv = %v", got)
	}
	if err := tk.Comment("TKT-1", "looks good"); err != nil {
		t.Fatal(err)
	}
	if got := f.lastArgv(); !reflect.DeepEqual(got, []string{"tkt", "comment", "TKT-1", "looks good"}) {
		t.Fatalf("argv = %v", got)
	}
}

func TestNonzeroExitRaisesWithCodeAndStderr(t *testing.T) {
	tk, _ := newFake(resp{code: 4, stderr: "tkt: ticket TKT-99 not found"})
	_, err := tk.View("TKT-99")
	te, ok := errAsTkt(err)
	if !ok {
		t.Fatalf("want *Error, got %T", err)
	}
	if te.ExitCode != 4 {
		t.Fatalf("exit code = %d, want 4", te.ExitCode)
	}
	if !strings.Contains(te.Error(), "not found") {
		t.Fatalf("message = %q", te.Error())
	}
	if !strings.Contains(te.Stderr, "TKT-99") {
		t.Fatalf("stderr = %q", te.Stderr)
	}
}

func TestInvalidJSONRaises(t *testing.T) {
	tk, _ := newFake(resp{stdout: "not json"})
	_, err := tk.Roles()
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("want invalid JSON error, got %v", err)
	}
}

func TestMissingBinaryRaises(t *testing.T) {
	tk, _ := newFake(resp{runErr: errStub("exec: not found")})
	_, err := tk.Roles()
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want binary-not-found error, got %v", err)
	}
}

func TestDoctorAllGreen(t *testing.T) {
	// Binary "sh" resolves on PATH, so the binary check passes without a stub.
	f := &fake{responses: []resp{{stdout: `{"todo": "To Do"}`}, {stdout: "[]"}}}
	tk := New("", "sh").WithRunner(f.run)
	checks := tk.Doctor()
	for _, c := range checks {
		if !c.OK {
			t.Fatalf("check %q failed: %s", c.Name, c.Detail)
		}
	}
	if len(checks) != 3 {
		t.Fatalf("want 3 checks, got %d", len(checks))
	}
}

func TestDoctorMissingAllQuery(t *testing.T) {
	f := &fake{responses: []resp{{stdout: `{"todo": "To Do"}`}, {code: 2, stderr: "no [queries].all"}}}
	tk := New("", "sh").WithRunner(f.run)
	for _, c := range tk.Doctor() {
		if c.Name == "'all' query present" {
			if c.OK || !strings.Contains(c.Detail, "ORDER BY key ASC") {
				t.Fatalf("all-query check = %+v", c)
			}
			return
		}
	}
	t.Fatal("'all' query check missing")
}

func TestDoctorNoBinaryShortCircuits(t *testing.T) {
	tk := New("", "definitely-not-a-real-binary-xyz")
	checks := tk.Doctor()
	if len(checks) != 1 || checks[0].OK {
		t.Fatalf("expected single failing check, got %+v", checks)
	}
}

// ---- test helpers ----

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func envHas(env []string, kv string) bool {
	return slices.Contains(env, kv)
}

type errStub string

func (e errStub) Error() string { return string(e) }

func errAsTkt(err error) (*Error, bool) {
	te, ok := err.(*Error)
	return te, ok
}

// ---- TKB-25: the config a dispatch reads ----

func TestBoardOwnershipArgvAndParse(t *testing.T) {
	tk, f := newFake(resp{stdout: `{"todo->in_progress": "agent", "in_progress->review": "agent", "review->done": "human"}`})
	got := tk.BoardOwnership()
	if got["todo->in_progress"] != "agent" || got["review->done"] != "human" {
		t.Fatalf("ownership = %v", got)
	}
	if argv := f.lastArgv(); !reflect.DeepEqual(argv, []string{"tkt", "cfg", "board.ownership", "--json"}) {
		t.Fatalf("argv = %v", argv)
	}
}

// Best-effort, like BoardHiddenRoles: a board with no ownership config must
// still open, it just has nothing to dispatch into.
func TestBoardOwnershipBestEffort(t *testing.T) {
	for _, r := range []resp{
		{stderr: "no such key", code: 4},
		{stdout: "not json"},
		{stdout: `"a string, not a map"`},
	} {
		tk, _ := newFake(r)
		if got := tk.BoardOwnership(); got != nil {
			t.Errorf("%+v: ownership = %v, want nil", r, got)
		}
	}
}

func TestVCSArgvAndParse(t *testing.T) {
	tk, f := newFake(resp{stdout: `{"provider":"github","repo":"olddognewflex/tktban",` +
		`"default_branch":"main","branch_fmt":"feature/{key-lower}-{slug}",` +
		`"hotfix_fmt":"hotfix/{key-lower}-{slug}","reviewers":[],"merge":"squash"}`})
	got := tk.VCS()
	want := VCSConfig{
		Provider:      "github",
		Repo:          "olddognewflex/tktban",
		DefaultBranch: "main",
		BranchFmt:     "feature/{key-lower}-{slug}",
		HotfixFmt:     "hotfix/{key-lower}-{slug}",
	}
	if got != want {
		t.Fatalf("vcs = %+v, want %+v", got, want)
	}
	if argv := f.lastArgv(); !reflect.DeepEqual(argv, []string{"tkt", "cfg", "vcs", "--json"}) {
		t.Fatalf("argv = %v", argv)
	}
}

// A failure reads as "no branch convention", which is what the dispatch guard
// refuses on — never as a half-filled config it might act upon.
func TestVCSBestEffort(t *testing.T) {
	for _, r := range []resp{{stderr: "config error", code: 2}, {stdout: "{"}, {runErr: errNotFound}} {
		tk, _ := newFake(r)
		if got := tk.VCS(); got != (VCSConfig{}) {
			t.Errorf("%+v: vcs = %+v, want the zero config", r, got)
		}
	}
}

var errNotFound = &Error{Message: "no binary"}

func TestAgentTarget(t *testing.T) {
	order := []string{"todo", "in_progress", "review", "done"}
	full := map[string]string{
		"todo->in_progress":   "agent",
		"in_progress->review": "agent",
		"review->done":        "agent",
	}
	cases := []struct {
		name      string
		ownership map[string]string
		from      string
		want      string
		wantOK    bool
	}{
		{"todo hands off to in_progress", full, "todo", "in_progress", true},
		{"in_progress hands off to review", full, "in_progress", "review", true},
		{"the last lane hands off to nothing", full, "done", "", false},
		{"a human-owned transition is not a dispatch", map[string]string{
			"todo->in_progress": "human",
		}, "todo", "", false},
		{"no ownership at all", nil, "todo", "", false},
		{"a malformed transition is ignored", map[string]string{
			"todo": "agent", "->x": "agent", "todo->": "agent",
		}, "todo", "", false},
		// A lane the board does not have is not somewhere a ticket may be
		// moved to: it would take the card off the board entirely.
		{"a target the board does not list is refused", map[string]string{
			"todo->in_progres": "agent", // a typo in the config
		}, "todo", "", false},
		{"a known target still wins alongside an unknown one", map[string]string{
			"todo->nowhere": "agent", "todo->review": "agent",
		}, "todo", "review", true},
		{"whitespace around the arrow is tolerated", map[string]string{
			" todo -> in_progress ": "agent",
		}, "todo", "in_progress", true},
	}
	for _, c := range cases {
		got, ok := AgentTarget(c.ownership, c.from, order)
		if got != c.want || ok != c.wantOK {
			t.Errorf("%s: AgentTarget = (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.wantOK)
		}
	}
}

// Ownership is a map, so "the first agent-owned transition" needs an order of
// its own or the answer changes between runs. Board order decides, and the
// answer has to be the same every time.
func TestAgentTargetIsDeterministicAcrossMapOrder(t *testing.T) {
	ownership := map[string]string{
		"todo->done":        "agent",
		"todo->review":      "agent",
		"todo->in_progress": "agent",
	}
	order := []string{"todo", "in_progress", "review", "done"}
	for i := range 50 {
		got, ok := AgentTarget(ownership, "todo", order)
		if !ok || got != "in_progress" {
			t.Fatalf("run %d: AgentTarget = (%q, %v), want the earliest lane on the board", i, got, ok)
		}
	}
	// Targets the board does not list are dropped, however many there are.
	unknown := map[string]string{"todo->zeta": "agent", "todo->alpha": "agent"}
	for i := range 50 {
		got, ok := AgentTarget(unknown, "todo", order)
		if ok || got != "" {
			t.Fatalf("run %d: unknown targets gave (%q, %v), want a refusal", i, got, ok)
		}
	}
	// With no board order to check against there is nothing to reject, so
	// ties break by name and the answer still never wobbles.
	for i := range 50 {
		got, ok := AgentTarget(unknown, "todo", nil)
		if !ok || got != "alpha" {
			t.Fatalf("run %d: AgentTarget with no order = (%q, %v), want alpha", i, got, ok)
		}
	}
}

// ---- TKB-25: subprocesses run under the caller's deadline ----

// The budget has to reach the subprocess, not merely the wait around it.
func TestRunnerReceivesTheCallersContext(t *testing.T) {
	var gotDeadline bool
	var hadDeadline bool
	tk := New("", "tkt").WithRunner(func(ctx context.Context, _ string, _, _ []string) ([]byte, []byte, int, error) {
		_, hadDeadline = ctx.Deadline()
		gotDeadline = true
		return []byte(`{}`), nil, 0, nil
	})

	// Without a context, an ordinary read is unbounded, as every other board
	// read has always been.
	tk.BoardOwnership()
	if !gotDeadline {
		t.Fatal("the runner never ran")
	}
	if hadDeadline {
		t.Error("a plain Tkt put a deadline on its subprocess")
	}

	// With one, the deadline is handed down.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tk.WithContext(ctx).BoardOwnership()
	if !hadDeadline {
		t.Error("WithContext did not hand its deadline to the runner")
	}
}

// WithContext is a copy: a caller putting a two-second budget on one read
// must not put it on the board's refresh as well.
func TestWithContextDoesNotMutateTheReceiver(t *testing.T) {
	var deadlines []bool
	tk := New("", "tkt").WithRunner(func(ctx context.Context, _ string, _, _ []string) ([]byte, []byte, int, error) {
		_, ok := ctx.Deadline()
		deadlines = append(deadlines, ok)
		return []byte(`{}`), nil, 0, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	bounded := tk.WithContext(ctx)
	bounded.BoardOwnership()
	tk.BoardOwnership() // the original, afterwards
	if len(deadlines) != 2 || !deadlines[0] || deadlines[1] {
		t.Fatalf("deadlines seen = %v, want [true false]", deadlines)
	}
	if bounded == tk {
		t.Error("WithContext returned the receiver itself")
	}
}

// A killed process reports an exit code of its own, so without this the
// caller is told "exit -1" for what is really a timeout.
func TestEndedContextReportsATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fake{responses: []resp{{stdout: `{"a":"b"}`}}}
	tk := New("", "tkt").WithRunner(f.run).WithContext(ctx)

	_, err := tk.Roles()
	if err == nil {
		t.Fatal("a read under an ended context must fail")
	}
	if !strings.Contains(err.Error(), "did not finish in time") {
		t.Fatalf("err = %q, want it to name the timeout", err)
	}
	// And the best-effort readers answer their zero value, which is what the
	// dispatch guards refuse on.
	if got := tk.BoardOwnership(); got != nil {
		t.Errorf("BoardOwnership under an ended context = %v, want nil", got)
	}
	if got := tk.VCS(); got != (VCSConfig{}) {
		t.Errorf("VCS under an ended context = %+v, want the zero config", got)
	}
}

// defaultRunner must use CommandContext: with an already-cancelled context a
// real subprocess has to come back at once rather than run to completion.
// `sleep 30` is the witness — exec.Command would wait for all of it, and this
// test spends no wall-clock time at all.
func TestDefaultRunnerKillsRatherThanWaits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, _, _, runErr := defaultRunner(ctx, "sh", []string{"-c", "sleep 30"}, nil)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the subprocess ran for %v: the context is not bounding it", elapsed)
	}
	if runErr == nil {
		t.Fatal("a cancelled context must fail the run")
	}
}
