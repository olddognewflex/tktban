package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/olddognewflex/tktban/internal/settings"
)

// TKB-24: notify = false is set by hand for the herdr hook, often while a
// board is open. The board's next save (theme cycle, column hide) must write
// only its own keys and leave that edit — and keys it does not know — alone.
func TestBoardSaveKeepsHandEditedSettings(t *testing.T) {
	for _, press := range []string{"t", "x"} { // theme cycle, hide a column
		m, _ := testModel(t)
		path := m.settingsPath
		m = loadBoard(m) // settings are in memory now
		if err := os.WriteFile(path, []byte("notify = false\nfuture_key = \"kept\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		m = step(m, key(press))

		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got := settings.Load(path)
		if got["notify"] != false {
			t.Errorf("%q: notify after a board save = %v; file:\n%s", press, got["notify"], raw)
		}
		if !strings.Contains(string(raw), `future_key = "kept"`) {
			t.Errorf("%q: unknown key dropped; file:\n%s", press, raw)
		}
		if press == "t" && got["theme"] != m.themeName {
			t.Errorf("theme not saved: %v, want %v", got["theme"], m.themeName)
		}
		if press == "x" && !strings.Contains(str(got["hidden_roles"]), "todo") {
			t.Errorf("hidden set not saved: %v", got["hidden_roles"])
		}
	}
}
