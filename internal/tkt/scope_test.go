package tkt

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// TKB-32: the board's whole-board scope rides on `tkt list --all-projects`
// (TKT-70) and on ticketing.project naming "this" project.

// scopeTkt is a fake tkt whose list verb does or does not know --all-projects.
// advertise is what `list --help` says; accept is what the parser does. calls
// records every argv.
type scopeTkt struct {
	advertise, accept bool
	project           string // "" = the key is unset (exit 4)
	calls             [][]string
}

func (s *scopeTkt) run(_ context.Context, _ string, args, _ []string) ([]byte, []byte, int, error) {
	s.calls = append(s.calls, args)
	switch {
	case slices.Equal(args, []string{"list", "--help"}):
		help := "usage: tkt list [-h] [--json] (--tier TIER | --query QUERY)\n"
		if s.advertise {
			help = "usage: tkt list [-h] [--json] [--all-projects] (--tier TIER | --query QUERY)\n"
		}
		return []byte(help), nil, 0, nil
	case slices.Equal(args, []string{"cfg", "ticketing.project"}):
		if s.project == "" {
			return nil, []byte("tkt: config key not found: ticketing.project"), 4, nil
		}
		return []byte(s.project + "\n"), nil, 0, nil
	case args[0] == "list" && slices.Contains(args, "--all-projects") && !s.accept:
		return nil, []byte("tkt: error: unrecognized arguments: --all-projects"), 2, nil
	case args[0] == "list":
		return []byte(`[{"key":"TKB-1"}]`), nil, 0, nil
	}
	return []byte(`{"todo": "To Do"}`), nil, 0, nil
}

func (s *scopeTkt) count(want ...string) int {
	n := 0
	for _, c := range s.calls {
		if slices.Equal(c, want) {
			n++
		}
	}
	return n
}

func TestListAllScopedByDefault(t *testing.T) {
	s := &scopeTkt{advertise: true, accept: true, project: "TKB"}
	if _, err := New("", "tkt").WithRunner(s.run).ListAll(ListOpts{}); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 1 || !slices.Equal(s.calls[0], []string{"list", "--query", "all", "--json"}) {
		t.Fatalf("a scoped list probed or flagged: %v", s.calls)
	}
}

func TestListAllPassesAllProjects(t *testing.T) {
	s := &scopeTkt{advertise: true, accept: true, project: "TKB"}
	tk := New("", "tkt").WithRunner(s.run)
	for range 2 {
		if _, err := tk.ListAll(ListOpts{AllProjects: true}); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.count("list", "--query", "all", "--all-projects", "--json"); n != 2 {
		t.Fatalf("flagged lists = %d, want 2: %v", n, s.calls)
	}
	// Probed once, by any copy of the board's Tkt.
	tk.WithContext(context.Background()).SupportsAllProjects()
	if n := s.count("list", "--help"); n != 1 {
		t.Fatalf("help probed %d times, want 1", n)
	}
}

// An old tkt never sees the flag: the probe answers first.
func TestListAllOldTktIsUnsupported(t *testing.T) {
	s := &scopeTkt{project: "TKB"}
	_, err := New("", "tkt").WithRunner(s.run).ListAll(ListOpts{AllProjects: true})
	if !errors.Is(err, ErrAllProjectsUnsupported) {
		t.Fatalf("err = %v, want ErrAllProjectsUnsupported", err)
	}
	for _, c := range s.calls {
		if slices.Contains(c, "--all-projects") {
			t.Fatalf("an old tkt was passed the flag: %v", s.calls)
		}
	}
}

// Help that advertises the flag but a parser that refuses it: the refusal is
// the truth, and it sticks.
func TestListAllRejectedFlagIsUnsupported(t *testing.T) {
	s := &scopeTkt{advertise: true, project: "TKB"}
	tk := New("", "tkt").WithRunner(s.run)
	if _, err := tk.ListAll(ListOpts{AllProjects: true}); !errors.Is(err, ErrAllProjectsUnsupported) {
		t.Fatalf("err = %v, want ErrAllProjectsUnsupported", err)
	}
	if tk.SupportsAllProjects() {
		t.Fatal("still claims support after the parser refused the flag")
	}
	if _, err := tk.ListAll(ListOpts{AllProjects: true}); !errors.Is(err, ErrAllProjectsUnsupported) {
		t.Fatalf("second err = %v", err)
	}
	if n := s.count("list", "--query", "all", "--all-projects", "--json"); n != 1 {
		t.Fatalf("flagged list tried %d times, want 1", n)
	}
}

// Any other list failure is still the list failing, not "unsupported".
func TestListAllOtherFailureIsNotUnsupported(t *testing.T) {
	tk, _ := newFake(resp{stdout: "usage --all-projects"}, resp{code: 2, stderr: "no [queries].all"})
	_, err := tk.ListAll(ListOpts{AllProjects: true})
	if err == nil || errors.Is(err, ErrAllProjectsUnsupported) {
		t.Fatalf("err = %v, want the list's own failure", err)
	}
}

func TestProject(t *testing.T) {
	s := &scopeTkt{project: "TKB"}
	tk := New("", "tkt").WithRunner(s.run)
	if got := tk.Project(); got != "TKB" {
		t.Fatalf("Project = %q, want TKB", got)
	}
	tk.Project()
	if n := s.count("cfg", "ticketing.project"); n != 1 {
		t.Fatalf("project read %d times, want 1", n)
	}
	if got := New("", "tkt").WithRunner((&scopeTkt{}).run).Project(); got != "" {
		t.Fatalf("unset Project = %q, want empty", got)
	}
}

// scopeOf finds the whole-board doctor check.
func scopeOf(t *testing.T, checks []Check) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == "whole-board scope" {
			return c
		}
	}
	t.Fatalf("whole-board scope check missing: %+v", checks)
	return Check{}
}

func TestDoctorReportsScope(t *testing.T) {
	cases := []struct {
		name string
		s    *scopeTkt
		hint bool
		want string
	}{
		{"available", &scopeTkt{advertise: true, accept: true, project: "TKB"}, false, "TKB ↔ all projects"},
		{"old tkt", &scopeTkt{project: "TKB"}, true, "--all-projects"},
		{"no project", &scopeTkt{advertise: true, accept: true}, true, "ticketing.project"},
	}
	for _, c := range cases {
		got := scopeOf(t, New("", "sh").WithRunner(c.s.run).Doctor())
		if !got.OK || got.Hint != c.hint || !strings.Contains(got.Detail, c.want) {
			t.Errorf("%s: check = %+v, want OK, hint=%v, detail with %q", c.name, got, c.hint, c.want)
		}
	}
}

// A failed project read is not cached: the next read tries again, and a
// successful one is kept.
func TestProjectRetriesAFailedRead(t *testing.T) {
	tk, f := newFake(resp{code: 3, stderr: "provider timeout"}, resp{stdout: "TKB\n"})
	if got := tk.Project(); got != "" {
		t.Fatalf("failed read = %q", got)
	}
	if got := tk.Project(); got != "TKB" {
		t.Fatalf("retry = %q, want TKB", got)
	}
	tk.Project()
	if len(f.calls) != 2 {
		t.Fatalf("project read %d times, want 2", len(f.calls))
	}
}

// Doctor says an unreadable project is unreadable, not unset, and skips the
// scope check on a board whose list already failed.
func TestDoctorScopeWording(t *testing.T) {
	run := func(_ context.Context, _ string, args, _ []string) ([]byte, []byte, int, error) {
		if slices.Equal(args, []string{"cfg", "ticketing.project"}) {
			return nil, []byte("provider timeout"), 3, nil
		}
		if args[0] == "list" {
			return []byte("[]"), nil, 0, nil
		}
		return []byte(`{"todo": "To Do"}`), nil, 0, nil
	}
	c := scopeOf(t, New("", "sh").WithRunner(run).Doctor())
	if !c.Hint || !strings.Contains(c.Detail, "could not read ticketing.project") {
		t.Fatalf("check = %+v", c)
	}

	f := &fake{responses: []resp{{stdout: `{"todo": "To Do"}`}, {code: 2, stderr: "no [queries].all"}}}
	for _, c := range New("", "sh").WithRunner(f.run).Doctor() {
		if c.Name == "whole-board scope" {
			t.Fatalf("scope checked on a failing list: %+v", c)
		}
	}
}
