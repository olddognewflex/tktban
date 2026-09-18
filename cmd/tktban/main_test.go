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
	ctx := `{"workspace_id":"w1","focused_pane_cwd":"` + repo + `"}`

	cases := []struct {
		name         string
		config       string
		env          map[string]string
		wantConfig   string
		wantSettings string
	}{
		{"outside herdr is a no-op", "", map[string]string{
			"HERDR_PLUGIN_STATE_DIR": "/s", "HERDR_PLUGIN_CONTEXT_JSON": ctx,
		}, "", ""},
		{"outside herdr keeps explicit config", "/explicit.toml", nil, "/explicit.toml", ""},
		{"explicit config wins in herdr", "/explicit.toml", map[string]string{
			"HERDR_ENV": "1", "HERDR_PLUGIN_STATE_DIR": "/s", "HERDR_PLUGIN_CONTEXT_JSON": ctx,
		}, "/explicit.toml", filepath.Join("/s", "settings.toml")},
		{"config resolved from context", "", map[string]string{
			"HERDR_ENV": "1", "HERDR_PLUGIN_STATE_DIR": "/s", "HERDR_PLUGIN_CONTEXT_JSON": ctx,
		}, found, filepath.Join("/s", "settings.toml")},
		{"no state dir keeps standalone settings", "", map[string]string{
			"HERDR_ENV": "1", "HERDR_PLUGIN_CONTEXT_JSON": ctx,
		}, found, ""},
		{"nothing found leaves tkt discovery in charge", "", map[string]string{
			"HERDR_ENV": "1",
		}, "", ""},
	}
	for _, c := range cases {
		gotConfig, gotSettings := herdrSetup(c.config, envOf(c.env), bare, os.Stat)
		if gotConfig != c.wantConfig || gotSettings != c.wantSettings {
			t.Errorf("%s: got (%q, %q) want (%q, %q)",
				c.name, gotConfig, gotSettings, c.wantConfig, c.wantSettings)
		}
	}
}
