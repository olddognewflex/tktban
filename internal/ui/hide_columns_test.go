package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/olddognewflex/tktban/internal/tkt"
)

// runnerWithHiddenDefault wraps the canned captureRunner but answers the
// `[ui.board] hidden_roles` config lookup with a fixed JSON array.
func runnerWithHiddenDefault(def string) func(string, []string, []string) ([]byte, []byte, int, error) {
	cr := &captureRunner{}
	return func(bin string, args, env []string) ([]byte, []byte, int, error) {
		if eq(args, "cfg", "ui.board.hidden_roles", "--json") {
			return []byte(def), nil, 0, nil
		}
		return cr.run(bin, args, env)
	}
}

func roleSet(m Model) map[string]bool {
	out := map[string]bool{}
	for _, c := range m.columns {
		out[c.Role] = true
	}
	return out
}

// Criterion 1 + 4: hide a column via the keyboard, focus moves to a visible
// column, and X shows it again.
func TestHideAndShowColumn(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m) // todo (focused) + done
	if len(m.columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(m.columns))
	}
	m = step(m, key("x")) // hide focused (todo)
	if len(m.columns) != 1 || roleSet(m)["todo"] {
		t.Fatalf("todo not hidden: %v", roleSet(m))
	}
	// Focus moved to a visible column without error.
	if card, ok := m.selectedCard(); !ok || card.Key != "TKT-2" {
		t.Fatalf("focus did not move to a visible column: %v ok=%v", card.Key, ok)
	}
	m = step(m, key("X")) // show all
	if len(m.columns) != 2 {
		t.Fatalf("X did not restore columns: %d", len(m.columns))
	}
}

// Refuse to hide the last visible column.
func TestCannotHideLastColumn(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	m = step(m, key("x")) // hide todo -> only done left
	m = step(m, key("x")) // attempt to hide done (the last one)
	if len(m.columns) != 1 {
		t.Fatalf("last column should not be hidden, got %d columns", len(m.columns))
	}
}

// Criterion 2: hidden set persists across a restart via settings.toml.
func TestHiddenColumnsPersist(t *testing.T) {
	m, _ := testModel(t)
	path := m.settingsPath
	m = loadBoard(m)
	m = step(m, key("x")) // hide todo, persists

	m2 := New(m.tkt, 10, true, path) // fresh model, same settings file
	if !m2.hidden["todo"] {
		t.Fatalf("hidden set not persisted: %v", m2.hidden)
	}
	m2 = loadBoard(m2)
	if len(m2.columns) != 1 || roleSet(m2)["todo"] {
		t.Fatalf("persisted hide not applied after restart: %v", roleSet(m2))
	}
}

// Criterion 3: the [ui.board] hidden_roles config default seeds the initial
// hidden set on first run, and a user toggle overrides and persists.
func TestConfigDefaultSeedsThenUserOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.toml")
	tk := tkt.New("", "tkt").WithRunner(runnerWithHiddenDefault(`["done"]`))

	m := New(tk, 10, true, path) // first run: settings.toml absent -> seed default
	if !m.hidden["done"] {
		t.Fatalf("config default not seeded: %v", m.hidden)
	}
	m = loadBoard(m)
	if len(m.columns) != 1 || roleSet(m)["done"] {
		t.Fatalf("config default not applied: %v", roleSet(m))
	}
	// User shows all -> overrides + persists, so the default no longer applies.
	m = step(m, key("X"))

	m2 := New(tk, 10, true, path) // settings.toml now exists -> default ignored
	if m2.hidden["done"] {
		t.Fatalf("user override did not persist over config default: %v", m2.hidden)
	}
	m2 = loadBoard(m2)
	if len(m2.columns) != 2 {
		t.Fatalf("override not applied: %d columns", len(m2.columns))
	}
}

// The seeded config default survives a Save triggered by an unrelated setting
// (theme cycle) — it must not be clobbered with an empty value.
func TestConfigDefaultSurvivesThemeChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.toml")
	tk := tkt.New("", "tkt").WithRunner(runnerWithHiddenDefault(`["done"]`))

	m := New(tk, 10, true, path) // first run: seed ["done"]
	m = loadBoard(m)
	m = step(m, key("t")) // cycle theme -> Save writes settings.toml

	m2 := New(tk, 10, true, path) // file now exists; seeded default must persist
	if !m2.hidden["done"] {
		t.Fatalf("seeded default lost after theme change: %v", m2.hidden)
	}
}

// Criterion 5: a corrupt settings file falls back to showing all columns.
func TestCorruptSettingsShowsAllColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.toml")
	if err := os.WriteFile(path, []byte("this is not valid = = toml\n[broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := testModel(t)
	m = New(m.tkt, 10, true, path) // Load falls back to defaults, no crash
	m = loadBoard(m)
	if len(m.columns) != 2 {
		t.Fatalf("corrupt settings should show all columns, got %d", len(m.columns))
	}
}

// An invalid hidden set that would hide every column falls back to showing all.
func TestHidingEveryColumnFallsBackToAll(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	m.hidden = map[string]bool{"todo": true, "done": true}
	m.applyHidden()
	if len(m.columns) != 2 {
		t.Fatalf("hiding all columns should fall back to all, got %d", len(m.columns))
	}
}
