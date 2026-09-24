package herdr

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olddognewflex/tktban/internal/settings"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnvOutsideHerdr(t *testing.T) {
	e := FromEnv(env(nil))
	if e.InHerdr || e.StateDir != "" || e.SocketPath != "" || e.Context != (Context{}) {
		t.Fatalf("empty env should give zero Env, got %+v", e)
	}
}

func TestFromEnvParsesContext(t *testing.T) {
	e := FromEnv(env(map[string]string{
		"HERDR_ENV":                 "1",
		"HERDR_PLUGIN_STATE_DIR":    "/state/odnf.tktban",
		"HERDR_SOCKET_PATH":         "/run/herdr.sock",
		"HERDR_PLUGIN_CONTEXT_JSON": `{"workspace_id":"w1","workspace_cwd":"/ws","focused_pane_cwd":"/ws/repo","focused_pane_status":"idle","extra":1}`,
	}))
	if !e.InHerdr || e.StateDir != "/state/odnf.tktban" || e.SocketPath != "/run/herdr.sock" {
		t.Fatalf("env flags not read: %+v", e)
	}
	want := Context{WorkspaceID: "w1", WorkspaceCwd: "/ws", FocusedPaneCwd: "/ws/repo"}
	if e.Context != want {
		t.Fatalf("context = %+v, want %+v", e.Context, want)
	}
}

func TestFromEnvToleratesBadContext(t *testing.T) {
	for _, raw := range []string{"not json", "[]", `{"workspace_cwd":42}`} {
		e := FromEnv(env(map[string]string{"HERDR_ENV": "1", "HERDR_PLUGIN_CONTEXT_JSON": raw}))
		if !e.InHerdr {
			t.Fatalf("%q: InHerdr lost", raw)
		}
		if e.Context != (Context{}) {
			t.Fatalf("%q: want zero context, got %+v", raw, e.Context)
		}
	}
}

func TestFromEnvRequiresExactHerdrFlag(t *testing.T) {
	for _, v := range []string{"", "0", "true", "yes"} {
		if FromEnv(env(map[string]string{"HERDR_ENV": v})).InHerdr {
			t.Fatalf("HERDR_ENV=%q must not count as inside herdr", v)
		}
	}
}

// board builds a temp tree: root/.sdlc/config.toml, root/sub/deeper, and an
// unrelated bare/x with no config above it.
func board(t *testing.T) (root, deeper, bare string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "repo")
	deeper = filepath.Join(root, "sub", "deeper")
	bare = filepath.Join(base, "bare", "x")
	for _, d := range []string{filepath.Join(root, ".sdlc"), deeper, bare} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".sdlc", "config.toml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, deeper, bare
}

func TestFindConfigWalksUp(t *testing.T) {
	root, deeper, bare := board(t)
	want := filepath.Join(root, ".sdlc", "config.toml")
	if got := FindConfig(deeper, os.Stat); got != want {
		t.Fatalf("from deeper: got %q want %q", got, want)
	}
	if got := FindConfig(root, os.Stat); got != want {
		t.Fatalf("from root: got %q want %q", got, want)
	}
	if got := FindConfig(bare, os.Stat); got != "" {
		t.Fatalf("no config above bare dir, got %q", got)
	}
	if got := FindConfig("", os.Stat); got != "" {
		t.Fatalf("empty start must find nothing, got %q", got)
	}
	if got := FindConfig(deeper+string(filepath.Separator), os.Stat); got != want {
		t.Fatalf("trailing slash: got %q want %q", got, want)
	}
}

func TestFindConfigStopsAtRoot(t *testing.T) {
	calls := 0
	stat := func(p string) (fs.FileInfo, error) { calls++; return nil, fs.ErrNotExist }
	if got := FindConfig("/a/b", stat); got != "" {
		t.Fatalf("got %q", got)
	}
	if calls != 3 { // /a/b, /a, /
		t.Fatalf("expected 3 lookups ending at root, got %d", calls)
	}
	calls = 0
	if got := FindConfig("/", stat); got != "" || calls != 1 {
		t.Fatalf("root start: got %q after %d lookups", got, calls)
	}
}

func TestFindConfigIgnoresDirectoryNamedConfig(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, ".sdlc", "config.toml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindConfig(base, os.Stat); got != "" {
		t.Fatalf("a directory is not a config file, got %q", got)
	}
}

func TestResolveConfigOrder(t *testing.T) {
	root, deeper, bare := board(t)
	want := filepath.Join(root, ".sdlc", "config.toml")

	cases := []struct {
		name string
		ctx  Context
		cwd  string
		want string
	}{
		{"focused pane wins", Context{FocusedPaneCwd: deeper, WorkspaceCwd: bare}, bare, want},
		{"workspace when pane has none", Context{FocusedPaneCwd: bare, WorkspaceCwd: root}, bare, want},
		{"cwd as last resort", Context{}, deeper, want},
		{"nothing anywhere", Context{FocusedPaneCwd: bare, WorkspaceCwd: bare}, bare, ""},
	}
	for _, c := range cases {
		if got := ResolveConfig(Env{InHerdr: true, Context: c.ctx}, c.cwd, os.Stat); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestSettingsPath(t *testing.T) {
	if got := SettingsPath(Env{}); got != "" {
		t.Fatalf("outside herdr: got %q", got)
	}
	if got := SettingsPath(Env{InHerdr: true}); got != "" {
		t.Fatalf("no state dir: got %q", got)
	}
	if got := SettingsPath(Env{StateDir: "/s"}); got != "" {
		t.Fatalf("state dir without HERDR_ENV: got %q", got)
	}
	if got := SettingsPath(Env{InHerdr: true, StateDir: "/s"}); got != filepath.Join("/s", "settings.toml") {
		t.Fatalf("herdr: got %q", got)
	}
}

func TestSafeSettingsPath(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "settings.toml")
	if err := os.WriteFile(regular, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked.toml")
	if err := os.Symlink(filepath.Join(dir, "elsewhere.toml"), link); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.toml")

	for _, c := range []struct{ name, in, want string }{
		{"regular file kept", regular, regular},
		{"not yet created kept", missing, missing},
		{"symlink rejected", link, ""},
		{"empty stays empty", "", ""},
	} {
		if got := SafeSettingsPath(c.in, os.Lstat); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestSeedSettingsCopiesOnce(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "standalone.toml")
	dst := filepath.Join(dir, "state", "settings.toml")
	if err := os.WriteFile(src, []byte(`theme = "catppuccin-mocha"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SeedSettings(dst, src); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(dst)["theme"]; got != "catppuccin-mocha" {
		t.Fatalf("seeded theme = %v; file: %s", got, read(t, dst))
	}

	// A later standalone change must not overwrite the plugin's own file.
	if err := os.WriteFile(src, []byte(`theme = "textual-light"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SeedSettings(dst, src); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(dst)["theme"]; got != "catppuccin-mocha" {
		t.Fatalf("existing plugin settings overwritten: %v", got)
	}
}

// TKB-24: notify is the hook's setting and the hook reads only the plugin
// file, so a standalone notify = false must not travel to it — days later, on
// whichever launch happens to find no plugin file yet.
func TestSeedSettingsDropsNotify(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "standalone.toml")
	dst := filepath.Join(dir, "state", "settings.toml")
	body := "hidden_roles = \"done\"\nnotify = false\ntheme = \"catppuccin-mocha\"\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SeedSettings(dst, src); err != nil {
		t.Fatal(err)
	}
	seeded := read(t, dst)
	if strings.Contains(seeded, "notify") {
		t.Errorf("seeded file carries notify:\n%s", seeded)
	}
	if got := settings.Load(dst)["notify"]; got != true {
		t.Errorf("notify in the plugin file = %v, want the default the hook reads as on", got)
	}
	// What the seed is for still carries over.
	if got := settings.Load(dst); got["theme"] != "catppuccin-mocha" || got["hidden_roles"] != "done" {
		t.Errorf("display settings not seeded: %v", got)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSeedSettingsDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "standalone.toml")
	outside := filepath.Join(dir, "outside.toml")
	dst := filepath.Join(dir, "state", "settings.toml")
	if err := os.WriteFile(src, []byte("theme = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dst); err != nil { // dangling link
		t.Fatal(err)
	}
	if err := SeedSettings(dst, src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("seed followed the symlink and wrote outside the state dir (err=%v)", err)
	}
}

func TestSeedSettingsCorruptSourceSeedsDefaults(t *testing.T) {
	// A corrupt standalone file has nothing to read, so the plugin file starts
	// at the defaults — and is itself valid TOML, unlike the copy this used to
	// make, which a board save then had to rewrite.
	dir := t.TempDir()
	src := filepath.Join(dir, "standalone.toml")
	dst := filepath.Join(dir, "state", "settings.toml")
	if err := os.WriteFile(src, []byte("not = [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SeedSettings(dst, src); err != nil {
		t.Fatal(err)
	}
	seeded := read(t, dst)
	if strings.Contains(seeded, "[valid") {
		t.Fatalf("corrupt source copied through:\n%s", seeded)
	}
	if got := settings.Load(dst); got["theme"] != settings.Defaults["theme"] {
		t.Fatalf("seeded %v from a corrupt source; file:\n%s", got, seeded)
	}
}

func TestSeedSettingsNoSourceOrNoTarget(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "state", "settings.toml")
	if err := SeedSettings(dst, filepath.Join(dir, "missing.toml")); err != nil {
		t.Fatalf("missing source must not error: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("nothing should be written without a source, stat err=%v", err)
	}
	if err := SeedSettings("", "/whatever"); err != nil {
		t.Fatalf("empty target must be a no-op: %v", err)
	}
}

// SelectKey follows ResolveConfig's precedence: the focused pane's directory,
// then the workspace root, then the process cwd; the first key wins.
func TestSelectKeyPrecedence(t *testing.T) {
	keys := map[string][]string{
		"/pane":   {"TKB-23", "TKB-24"},
		"/ws":     {"TKB-99"},
		"/cwd":    {"TKB-7"},
		"/nokeys": nil,
	}
	keysForDir := func(dir string) []string { return keys[dir] }
	ctx := func(pane, ws string) Env {
		return Env{InHerdr: true, Context: Context{FocusedPaneCwd: pane, WorkspaceCwd: ws}}
	}
	cases := []struct {
		name string
		e    Env
		cwd  string
		want string
	}{
		{"focused pane wins", ctx("/pane", "/ws"), "/cwd", "TKB-23"},
		{"workspace next", ctx("", "/ws"), "/cwd", "TKB-99"},
		{"a keyless pane falls through to the workspace", ctx("/nokeys", "/ws"), "/cwd", "TKB-99"},
		{"a keyless pane and workspace fall through to cwd", ctx("/nokeys", "/nokeys"), "/cwd", "TKB-7"},
		{"nothing named anywhere", ctx("/nokeys", "/nokeys"), "/nokeys", ""},
		{"outside herdr it is just the cwd", Env{}, "/cwd", "TKB-7"},
		{"no directories at all", Env{}, "", ""},
	}
	for _, c := range cases {
		if got := SelectKey(c.e, c.cwd, keysForDir); got != c.want {
			t.Errorf("%s: SelectKey = %q, want %q", c.name, got, c.want)
		}
	}
}

// A herdr process given no context does not guess from its working directory:
// herdr starts a plugin pane in the plugin's own install dir unless the
// launcher passes --cwd, and that dir is a checkout with a ticket of its own.
// Outside herdr the working directory is the person's own, so it is used.
func TestSelectKeyIgnoresCwdInHerdrWithoutContext(t *testing.T) {
	keysForDir := func(string) []string { return []string{"TKB-23"} } // the plugin's own checkout
	blind := Env{InHerdr: true, StateDir: "/state/odnf.tktban"}
	if got := SelectKey(blind, "/plugins/odnf.tktban", keysForDir); got != "" {
		t.Errorf("a contextless herdr pane selected %q from its own directory", got)
	}
	// Any context at all means the launcher told us where we are, so the
	// working directory is a usable last resort again.
	withCtx := blind
	withCtx.Context.WorkspaceCwd = "/nokeys"
	only := func(dir string) []string {
		if dir == "/repo" {
			return []string{"TKB-7"}
		}
		return nil
	}
	if got := SelectKey(withCtx, "/repo", only); got != "TKB-7" {
		t.Errorf("with a context, cwd fallback = %q, want TKB-7", got)
	}
	if got := SelectKey(Env{}, "/plugins/odnf.tktban", keysForDir); got != "TKB-23" {
		t.Errorf("outside herdr the cwd must still count, got %q", got)
	}
}

// An empty directory is skipped rather than handed to keysForDir (which would
// walk up from the process cwd and pick a key from the wrong repo).
func TestSelectKeySkipsEmptyDirs(t *testing.T) {
	var asked []string
	SelectKey(Env{}, "", func(dir string) []string { asked = append(asked, dir); return nil })
	if len(asked) != 0 {
		t.Fatalf("asked for keys of %v, want nothing", asked)
	}
}
