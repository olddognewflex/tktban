package main

import (
	"os"
	"path/filepath"
	"testing"
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
		if got := liveSource(envOf(c.env), c.disabled); (got != nil) != c.want {
			t.Errorf("%s: live source = %v, want present=%v", c.name, got, c.want)
		}
	}
}
