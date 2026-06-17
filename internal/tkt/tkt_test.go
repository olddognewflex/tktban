package tkt

import (
	"reflect"
	"slices"
	"strings"
	"testing"

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

func (f *fake) run(bin string, args, env []string) ([]byte, []byte, int, error) {
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
