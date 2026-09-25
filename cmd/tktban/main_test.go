package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/tkt"
	"github.com/olddognewflex/tktban/internal/ui"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestHerdrSetup(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".sdlc"), 0o755); err != nil {
		t.Fatal(err)
	}
	found := filepath.Join(repo, ".sdlc", "config.toml")
	if err := os.WriteFile(found, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(base, "bare")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	linkState := filepath.Join(base, "linkstate")
	if err := os.MkdirAll(linkState, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "elsewhere.toml"), filepath.Join(linkState, "settings.toml")); err != nil {
		t.Fatal(err)
	}
	ctx := `{"workspace_id":"w1","focused_pane_cwd":"` + repo + `"}`

	cases := []struct {
		name         string
		config       string
		env          map[string]string
		wantConfig   string
		wantSettings string
		wantState    string
	}{
		{"outside herdr is a no-op", "", map[string]string{
			"HERDR_PLUGIN_STATE_DIR": "/s", "HERDR_PLUGIN_CONTEXT_JSON": ctx,
		}, "", "", ""},
		{"outside herdr keeps explicit config", "/explicit.toml", nil, "/explicit.toml", "", ""},
		{"explicit config wins in herdr", "/explicit.toml", map[string]string{
			"HERDR_ENV": "1", "HERDR_PLUGIN_STATE_DIR": "/s", "HERDR_PLUGIN_CONTEXT_JSON": ctx,
		}, "/explicit.toml", filepath.Join("/s", "settings.toml"), "/s"},
		{"config resolved from context", "", map[string]string{
			"HERDR_ENV": "1", "HERDR_PLUGIN_STATE_DIR": "/s", "HERDR_PLUGIN_CONTEXT_JSON": ctx,
		}, found, filepath.Join("/s", "settings.toml"), "/s"},
		{"no state dir keeps standalone settings", "", map[string]string{
			"HERDR_ENV": "1", "HERDR_PLUGIN_CONTEXT_JSON": ctx,
		}, found, "", ""},
		{"symlinked plugin settings fall back to standalone", "", map[string]string{
			"HERDR_ENV": "1", "HERDR_PLUGIN_STATE_DIR": linkState, "HERDR_PLUGIN_CONTEXT_JSON": ctx,
		}, found, "", linkState},
		{"nothing found leaves tkt discovery in charge", "", map[string]string{
			"HERDR_ENV": "1",
		}, "", "", ""},
	}
	for _, c := range cases {
		gotConfig, gotSettings, gotState := herdrSetup(c.config, envOf(c.env), bare, os.Stat, os.Lstat)
		if gotConfig != c.wantConfig || gotSettings != c.wantSettings || gotState != c.wantState {
			t.Errorf("%s: got (%q, %q, %q) want (%q, %q, %q)",
				c.name, gotConfig, gotSettings, gotState, c.wantConfig, c.wantSettings, c.wantState)
		}
	}
}

func TestLiveSourceOnlyInsideHerdr(t *testing.T) {
	inHerdr := map[string]string{"HERDR_ENV": "1", "HERDR_SOCKET_PATH": "/run/herdr.sock"}
	cases := []struct {
		name     string
		env      map[string]string
		disabled bool
		want     bool
	}{
		{"outside herdr", map[string]string{"HERDR_SOCKET_PATH": "/run/herdr.sock"}, false, false},
		{"no socket", map[string]string{"HERDR_ENV": "1"}, false, false},
		{"--no-herdr-live", inHerdr, true, false},
		{"inside herdr", inHerdr, false, true},
	}
	for _, c := range cases {
		// Compare the interface itself: a typed nil pointer inside it would read
		// as "live on" to the board.
		if got := liveSource(envOf(c.env), herdrOpts{off: c.disabled}); (got != nil) != c.want {
			t.Errorf("%s: live source = %v, want present=%v", c.name, got, c.want)
		}
	}
}

// repoOn makes a directory that reads as a git checkout of branch, without
// needing git: KeysForDir follows .git/HEAD.
func repoOn(t *testing.T, base, name, branch string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	head := "ref: refs/heads/" + branch + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// resolve runs selectTarget the way the board does: take the key it already
// has, or run the derivation it handed back.
func resolve(explicit string, fromCwd bool, getenv func(string) string, cwd string) string {
	key, derive := selectTarget(explicit, fromCwd, getenv, cwd)
	if derive == nil {
		return key
	}
	return derive()
}

// An explicit --select beats --select-from-cwd, even when the branch names a
// different ticket, so a person can always ask for the board they want.
func TestSelectFlagWins(t *testing.T) {
	base := t.TempDir()
	pane := repoOn(t, base, "pane", "feature/tkb-99-other")
	env := envOf(map[string]string{
		"HERDR_ENV":                 "1",
		"HERDR_PLUGIN_CONTEXT_JSON": `{"focused_pane_cwd":"` + pane + `"}`,
	})
	cases := []struct {
		name     string
		explicit string
		fromCwd  bool
		want     string
	}{
		{"--select with --select-from-cwd", "TKB-23", true, "TKB-23"},
		{"--select alone", "TKB-23", false, "TKB-23"},
		{"neither flag reads no branch", "", false, ""},
		{"--select empty is no selection", "", false, ""},
		{"a junk --select is passed on for the board to report", "not a key", true, "not a key"},
	}
	for _, c := range cases {
		if got := resolve(c.explicit, c.fromCwd, env, base); got != c.want {
			t.Errorf("%s: selectTarget = %q, want %q", c.name, got, c.want)
		}
	}
	// An explicit key needs no derivation at all, so nothing touches the disk.
	if _, derive := selectTarget("TKB-23", true, env, base); derive != nil {
		t.Error("--select still handed back a branch derivation to run")
	}
	// --select-from-cwd defers its work instead of doing it up front.
	if key, derive := selectTarget("", true, env, base); key != "" || derive == nil {
		t.Errorf("--select-from-cwd resolved eagerly: key=%q derive=%v", key, derive != nil)
	}
}

// AC2: the board key pressed inside an agent pane opens the board on that
// pane's ticket. The launcher passes the pane's directory and forwards its
// herdr context, because plugin.pane.open carries cwd and env but no argv.
func TestSelectFromCwdUsesHerdrContext(t *testing.T) {
	base := t.TempDir()
	pane := repoOn(t, base, "pane", "feature/tkb-23-agent-pane-jump")
	ws := repoOn(t, base, "ws", "hotfix/tkb-7-thing")
	plain := repoOn(t, base, "plain", "main")
	ctx := func(pane, ws string) string {
		return `{"workspace_id":"w1","focused_pane_cwd":"` + pane + `","workspace_cwd":"` + ws + `"}`
	}
	cases := []struct {
		name string
		env  map[string]string
		cwd  string
		want string
	}{
		{"focused pane's branch", map[string]string{
			"HERDR_ENV": "1", "HERDR_PLUGIN_CONTEXT_JSON": ctx(pane, ws),
		}, plain, "TKB-23"},
		{"workspace root when the pane names nothing", map[string]string{
			"HERDR_ENV": "1", "HERDR_PLUGIN_CONTEXT_JSON": ctx(plain, ws),
		}, plain, "TKB-7"},
		{"the pane's own directory once the context named one", map[string]string{
			"HERDR_ENV": "1", "HERDR_PLUGIN_CONTEXT_JSON": `{"workspace_cwd":"` + plain + `"}`,
		}, pane, "TKB-23"},
		{"a herdr pane with no context does not guess", map[string]string{
			"HERDR_ENV": "1",
		}, pane, ""},
		{"outside herdr it is this directory's branch", nil, pane, "TKB-23"},
		{"a branch with no key preselects nothing", nil, plain, ""},
	}
	for _, c := range cases {
		if got := resolve("", true, envOf(c.env), c.cwd); got != c.want {
			t.Errorf("%s: selectTarget = %q, want %q", c.name, got, c.want)
		}
	}
}

// The popup behaviour is the manifest's --popup, not --herdr: --herdr is a
// documented flag someone may pass to a board in an ordinary pane, and that
// board must not exit when they jump to an agent pane.
func TestPopupFlagWiring(t *testing.T) {
	cases := []struct {
		argv []string
		want bool
	}{
		{nil, false},
		{[]string{"--herdr"}, false},
		{[]string{"--popup"}, true},
		{[]string{"--herdr", "--popup"}, true},
	}
	orig := board
	t.Cleanup(func() { board = orig })
	for _, c := range cases {
		got := false
		ran := false
		board = func(_ *tkt.Tkt, _ float64, _ bool, _ string, _ ui.LiveSource, _ string, _ string, _ func() string, popup bool) int {
			ran, got = true, popup
			return 0
		}
		if code := run(c.argv); code != 0 {
			t.Fatalf("%v: run = %d", c.argv, code)
		}
		if !ran {
			t.Fatalf("%v: the board never ran", c.argv)
		}
		if got != c.want {
			t.Errorf("%v: popup = %v, want %v", c.argv, got, c.want)
		}
	}
}

// --no-herdr-tokens has to reach the live source itself, not just parse: the
// board never sees it, the SocketSource does.
func TestNoHerdrTokensFlag(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SOCKET_PATH", "/run/herdr.sock")
	cases := []struct {
		argv []string
		want bool
	}{
		{nil, true},
		{[]string{"--herdr"}, true},
		{[]string{"--no-herdr-tokens"}, false},
		{[]string{"--herdr", "--no-herdr-tokens"}, false},
	}
	orig := board
	t.Cleanup(func() { board = orig })
	for _, c := range cases {
		var got ui.LiveSource
		board = func(_ *tkt.Tkt, _ float64, _ bool, _ string, live ui.LiveSource, _ string, _ string, _ func() string, _ bool) int {
			got = live
			return 0
		}
		if code := run(c.argv); code != 0 {
			t.Fatalf("%v: run = %d", c.argv, code)
		}
		src, ok := got.(*herdr.SocketSource)
		if !ok {
			t.Fatalf("%v: live source = %T, want *herdr.SocketSource", c.argv, got)
		}
		if src.Tokens != c.want {
			t.Errorf("%v: Tokens = %v, want %v", c.argv, src.Tokens, c.want)
		}
	}
}

// --no-herdr-live takes the whole source away, so it takes the tokens with it.
func TestNoHerdrLiveAlsoStopsTokens(t *testing.T) {
	if got := liveSource(envOf(map[string]string{
		"HERDR_ENV": "1", "HERDR_SOCKET_PATH": "/run/herdr.sock",
	}), herdrOpts{off: true}); got != nil {
		t.Fatalf("live source = %v, want none", got)
	}
}

// doctor goes nowhere near the board — and so nowhere near the select-key
// derivation, which now lives inside the board branch and never runs here.
func TestDoctorDoesNotRunTheBoard(t *testing.T) {
	origBoard, origDoctor := board, doctor
	t.Cleanup(func() { board, doctor = origBoard, origDoctor })
	board = func(*tkt.Tkt, float64, bool, string, ui.LiveSource, string, string, func() string, bool) int {
		t.Error("doctor ran the board")
		return 1
	}
	ran := false
	doctor = func(*tkt.Tkt) int { ran = true; return 0 }
	if code := run([]string{"--select-from-cwd", "doctor"}); code != 0 {
		t.Fatalf("doctor exit = %d", code)
	}
	if !ran {
		t.Fatal("doctor did not run")
	}
}

// ---- TKB-25: the D key is off unless three things agree ----

// writeSettings puts a settings file at path with the given TOML body.
func writeSettings(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDispatchDirGating(t *testing.T) {
	on := "dispatch = true\n"
	off := "dispatch = false\n"
	inHerdrWithContext := map[string]string{
		"HERDR_ENV":                 "1",
		"HERDR_PLUGIN_CONTEXT_JSON": `{"workspace_id":"w1","focused_pane_cwd":"/pane"}`,
	}
	cases := []struct {
		name     string
		settings string
		env      map[string]string
		opt      herdrOpts
		cwd      string
		want     string
	}{
		{"off by default", "", nil, herdrOpts{}, "/work", ""},
		{"explicitly off", off, nil, herdrOpts{}, "/work", ""},
		{"on, outside herdr, uses the working directory", on, nil, herdrOpts{}, "/work", "/work"},
		{"on, inside herdr, uses the focused pane", on, inHerdrWithContext, herdrOpts{}, "/work", "/pane"},
		// The guard that matters: a plugin pane with no context runs in
		// tktban's own install checkout, so there is no safe directory.
		{"on, a plugin pane with no context at all", on, map[string]string{
			"HERDR_ENV": "1", "HERDR_PLUGIN_STATE_DIR": "/state/odnf.tktban",
		}, herdrOpts{}, "/plugin/install", ""},
		// An ordinary herdr terminal pane is not a plugin process: its
		// working directory is exactly the repo the person meant.
		{"on, a plain herdr terminal pane", on, map[string]string{
			"HERDR_ENV": "1", "HERDR_SOCKET_PATH": "/run/herdr.sock",
		}, herdrOpts{}, "/src/tktban", "/src/tktban"},
		{"--no-herdr-dispatch wins over the setting", on, nil, herdrOpts{noDispatch: true}, "/work", ""},
	}
	for _, c := range cases {
		path := writeSettings(t, c.settings)
		if got := dispatchDir(envOf(c.env), c.cwd, path, c.opt); got != c.want {
			t.Errorf("%s: dispatchDir = %q, want %q", c.name, got, c.want)
		}
	}
}

// --no-herdr-dispatch reaches the board as an empty directory, which is what
// leaves the key refusing, and it does not disturb the live-status opt-outs
// that sit next to it in the same struct.
func TestNoHerdrDispatchFlagWiring(t *testing.T) {
	origBoard := board
	t.Cleanup(func() { board = origBoard })

	// run() reads the real process environment, and these tests may
	// themselves be running inside a herdr pane — where a plugin process with
	// no context is exactly the case DispatchDir refuses. Pin it to "outside
	// herdr" so this test is about the flag and not about where it ran.
	t.Setenv("HERDR_ENV", "")
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", "")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "")

	// The standalone settings file run() reads is
	// $XDG_CONFIG_HOME/tktban/settings.toml, so turn the setting on there.
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "tktban"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "tktban", "settings.toml"),
		[]byte("dispatch = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		argv    []string
		wantDir bool
	}{
		{nil, true},
		{[]string{"--no-herdr-dispatch"}, false},
		// The other opt-outs must not take the dispatch with them.
		{[]string{"--no-herdr-live"}, true},
		{[]string{"--no-herdr-tokens"}, true},
	} {
		var got string
		ran := false
		board = func(_ *tkt.Tkt, _ float64, _ bool, _ string, _ ui.LiveSource, dir string, _ string, _ func() string, _ bool) int {
			ran, got = true, dir
			return 0
		}
		if code := run(c.argv); code != 0 {
			t.Fatalf("%v: run = %d", c.argv, code)
		}
		if !ran {
			t.Fatalf("%v: the board never ran", c.argv)
		}
		if (got != "") != c.wantDir {
			t.Errorf("%v: dispatch dir = %q, want present=%v", c.argv, got, c.wantDir)
		}
	}
}
