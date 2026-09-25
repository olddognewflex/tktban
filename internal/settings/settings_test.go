package settings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultPathUsesXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdgcfg")
	want := filepath.Join("/tmp/xdgcfg", "tktban", "settings.toml")
	if got := DefaultPath(); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestDefaultPathFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	os.Unsetenv("XDG_CONFIG_HOME")
	t.Setenv("HOME", "/home/x")
	want := filepath.Join("/home/x", ".config", "tktban", "settings.toml")
	if got := DefaultPath(); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestLoadMissingReturnsDefaults(t *testing.T) {
	got := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if got["theme"] != Defaults["theme"] || len(got) != len(Defaults) {
		t.Fatalf("missing file should yield defaults, got %v", got)
	}
}

func TestLoadCorruptReturnsDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.toml")
	mustWrite(t, p, "this is = not valid toml ===")
	got := Load(p)
	if got["theme"] != Defaults["theme"] || len(got) != len(Defaults) {
		t.Fatalf("corrupt file should yield defaults, got %v", got)
	}
}

func TestLoadTakesKnownScalarKeysOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.toml")
	mustWrite(t, p, "theme = \"nord\"\nbogus = \"ignored\"\n")
	got := Load(p)
	if got["theme"] != "nord" {
		t.Fatalf("theme = %v, want nord", got["theme"])
	}
	if _, ok := got["bogus"]; ok {
		t.Fatal("unknown key 'bogus' must be dropped")
	}
}

func TestSaveLoadRoundTripIsHumanReadable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "settings.toml") // parent dir created by Save
	if err := Save(p, map[string]any{"theme": "gruvbox"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if !strings.Contains(string(raw), `theme = "gruvbox"`) {
		t.Fatalf("not human-readable: %q", raw)
	}
	if Load(p)["theme"] != "gruvbox" {
		t.Fatal("round-trip lost theme")
	}
}

func TestSavePersistsOnlyKnownKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.toml")
	if err := Save(p, map[string]any{"theme": "nord", "transient": "x"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	text := string(raw)
	if !strings.Contains(text, "theme") || strings.Contains(text, "transient") {
		t.Fatalf("only known keys should persist, got %q", text)
	}
}

func TestDumpTOMLScalarTypes(t *testing.T) {
	out := dumpTOML(map[string]any{"s": "hi", "b": true, "n": 5})
	for _, want := range []string{`s = "hi"`, "b = true", "n = 5"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dumpTOML missing %q in:\n%s", want, out)
		}
	}
}

func TestDumpTOMLEscapesSpecialCharsRoundTrip(t *testing.T) {
	// Newlines/tabs/quotes/backslashes and a control char must escape so the
	// output is valid TOML that parses back to the original string.
	value := "a\"b\\c\nd\te\x01f"
	out := dumpTOML(map[string]any{"theme": value})
	parsed, err := parseScalars(out)
	if err != nil {
		t.Fatalf("escaped output did not parse: %v\n%s", err, out)
	}
	if parsed["theme"] != value {
		t.Fatalf("round-trip mismatch: %q != %q", parsed["theme"], value)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// notify is set by hand for the herdr hook (TKB-24). A board loading and
// saving its own settings must keep a hand-set false, not drop the key.
func TestNotifySurvivesBoardSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.toml")
	if Load(path)["notify"] != true {
		t.Fatal("notify should default to true")
	}
	if err := os.WriteFile(path, []byte("theme = \"x\"\nnotify = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Load(path)
	s["theme"] = "y" // what the board does on a theme change
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	if got := Load(path)["notify"]; got != false {
		t.Fatalf("notify after a board save = %v, want false", got)
	}
}

func TestUpdateWritesOnlyGivenKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "settings.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# c\ntheme = \"a\"\nnotify = false\nother = 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, map[string]any{"theme": "b"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{`theme = "b"`, "notify = false", "other = 3"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %q in:\n%s", want, raw)
		}
	}

	// Missing file: just the given keys. Corrupt file: Defaults plus them.
	fresh := filepath.Join(t.TempDir(), "new", "settings.toml")
	if err := Update(fresh, map[string]any{"theme": "c"}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(fresh); string(raw) != "theme = \"c\"\n" {
		t.Errorf("fresh file = %q", raw)
	}
	if err := os.WriteFile(path, []byte("theme = [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, map[string]any{"theme": "d"}); err != nil {
		t.Fatal(err)
	}
	if got := Load(path); got["theme"] != "d" || got["notify"] != true {
		t.Errorf("after corrupt: %v", got)
	}
}

// TKB-24: the herdr notify hook reads this file, so a write must never leave
// it half-written. Each writer goes through a temp file and a rename.
func TestWritesAreAtomic(t *testing.T) {
	writers := map[string]func(path string) error{
		"Save":   func(p string) error { return Save(p, map[string]any{"theme": "after"}) },
		"Update": func(p string) error { return Update(p, map[string]any{"theme": "after"}) },
	}
	for name, write := range writers {
		// An interrupted write (the rename never happens) leaves the old
		// file whole and takes its temp file with it.
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.toml")
		before := "theme = \"before\"\n"
		if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
			t.Fatal(err)
		}
		orig := rename
		rename = func(string, string) error { return errors.New("interrupted") }
		err := write(path)
		rename = orig
		if err == nil {
			t.Errorf("%s: an interrupted write reported success", name)
		}
		if raw, _ := os.ReadFile(path); string(raw) != before {
			t.Errorf("%s: file after an interrupted write = %q, want %q", name, raw, before)
		}
		if left := tempFiles(t, dir); len(left) != 0 {
			t.Errorf("%s: temp files left behind: %v", name, left)
		}

		// A completed write replaces the file and leaves nothing behind.
		if err := write(path); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := Load(path)["theme"]; got != "after" {
			t.Errorf("%s: theme = %v, want after", name, got)
		}
		if left := tempFiles(t, dir); len(left) != 0 {
			t.Errorf("%s: temp files left behind: %v", name, left)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
			t.Errorf("%s: mode = %v (err %v), want 0644", name, info.Mode().Perm(), err)
		}

		// A symlink at the settings path is replaced, not written through.
		linkDir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.toml")
		if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(linkDir, "settings.toml")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if err := write(link); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if raw, _ := os.ReadFile(outside); string(raw) != "keep" {
			t.Errorf("%s: write followed the symlink: outside now %q", name, raw)
		}
		if info, err := os.Lstat(link); err != nil || !info.Mode().IsRegular() {
			t.Errorf("%s: settings path is not a regular file: %v (err %v)", name, info, err)
		}
	}
}

// A temp file an earlier crash left behind is swept once it is old; a fresh
// one (another writer's, in flight) is left alone.
func TestWriteSweepsStaleTemps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.toml")
	stale := filepath.Join(dir, "settings.toml.123"+tempSuffix)
	fresh := filepath.Join(dir, "settings.toml.456"+tempSuffix)
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, map[string]any{"theme": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temp survived (err=%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh temp removed: %v", err)
	}
}

func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*"+tempSuffix))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// TKB-25: the dispatch keys exist with safe defaults. dispatch must default
// to false — the D key cuts a branch and starts an agent, which is not
// something a board should do the first time someone leans on a key — and an
// empty dispatch_prompt means "use the built-in default", not "no prompt".
func TestDispatchDefaults(t *testing.T) {
	want := map[string]any{
		"dispatch":        false,
		"dispatch_agent":  "claude",
		"dispatch_args":   "",
		"dispatch_prompt": "",
	}
	for k, v := range want {
		got, known := Defaults[k]
		if !known {
			t.Errorf("%s is missing from Defaults", k)
			continue
		}
		if got != v {
			t.Errorf("Defaults[%q] = %v, want %v", k, got, v)
		}
	}
	// And they round-trip through the file, so a hand edit survives a reload.
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.toml")
	if err := Save(path, map[string]any{
		"dispatch": true, "dispatch_agent": "codex", "dispatch_args": "--yolo",
		"dispatch_prompt": "do {key}",
	}); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if got["dispatch"] != true || got["dispatch_agent"] != "codex" ||
		got["dispatch_args"] != "--yolo" || got["dispatch_prompt"] != "do {key}" {
		t.Fatalf("dispatch settings did not round-trip: %v", got)
	}
}
