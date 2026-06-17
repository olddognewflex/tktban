package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
