package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olddognewflex/tktban/internal/settings"
	"github.com/olddognewflex/tktban/internal/tkt"
)

// TKB-24 AC2: the b key silences the herdr notification hook, both ways, and
// says which way it went.
func TestBKeyTogglesNotify(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	if m.settings["notify"] != true {
		t.Fatalf("notify starts at %v, want the default true", m.settings["notify"])
	}

	m = step(m, key("b"))
	if m.settings["notify"] != false {
		t.Errorf("notify after one b = %v, want false", m.settings["notify"])
	}
	if m.status != "herdr notifications off" || m.statusKind != "" {
		t.Errorf("status after one b = %q (%q)", m.status, m.statusKind)
	}

	m = step(m, key("b"))
	if m.settings["notify"] != true {
		t.Errorf("notify after two b = %v, want true", m.settings["notify"])
	}
	if m.status != "herdr notifications on" || m.statusKind != "" {
		t.Errorf("status after two b = %q (%q)", m.status, m.statusKind)
	}
}

// The hook reads the file, not the board's memory, so the toggle is worth
// nothing unless it lands on disk — both ways.
func TestBKeyPersistsNotify(t *testing.T) {
	m, _ := testModel(t)
	path := m.settingsPath
	m = loadBoard(m)

	m = step(m, key("b"))
	if got := settings.Load(path)["notify"]; got != false {
		t.Fatalf("notify on disk after b = %v, want false (%s)", got, readFile(t, path))
	}

	m = step(m, key("b"))
	if got := settings.Load(path)["notify"]; got != true {
		t.Fatalf("notify on disk after a second b = %v, want true (%s)", got, readFile(t, path))
	}
}

// The toggle goes through the board's own write path: everything else in the
// file stays as it is, including a key this version does not know and the
// board's own theme / hidden_roles.
func TestBKeyKeepsOtherKeys(t *testing.T) {
	m, _ := testModel(t)
	path := m.settingsPath
	m = loadBoard(m)
	m = step(m, key("t")) // a theme the board owns
	m = step(m, key("x")) // a hidden column the board owns
	theme, hidden := m.themeName, str(m.settings["hidden_roles"])
	if hidden == "" {
		t.Fatal("x hid no column, so there is no hidden set to preserve")
	}

	// Hand-edited while the board is open, as a person would.
	raw := readFile(t, path) + "future_key = \"kept\"\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	m = step(m, key("b"))

	after := readFile(t, path)
	got := settings.Load(path)
	if got["notify"] != false {
		t.Errorf("notify = %v, want false; file:\n%s", got["notify"], after)
	}
	if !strings.Contains(after, `future_key = "kept"`) {
		t.Errorf("unknown key dropped by the notify write; file:\n%s", after)
	}
	if got["theme"] != theme {
		t.Errorf("theme = %v, want %q; file:\n%s", got["theme"], theme, after)
	}
	if str(got["hidden_roles"]) != hidden {
		t.Errorf("hidden_roles = %v, want %q; file:\n%s", got["hidden_roles"], hidden, after)
	}
}

// Outside herdr the board writes the standalone settings file, which the hook
// never reads once herdr's plugin file exists. The status line has to say so
// rather than promise a silence the user will not get.
func TestBKeyOutsideHerdrSaysWhereItLanded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	cr := &captureRunner{}
	m := New(tkt.New("", "tkt").WithRunner(cr.run), 10, true, "") // no plugin state dir
	m.width, m.height = 120, 30
	m = loadBoard(m)

	m = step(m, key("b"))
	if !strings.HasPrefix(m.status, "herdr notifications off") {
		t.Fatalf("status = %q", m.status)
	}
	if m.status == "herdr notifications off" {
		t.Errorf("standalone board did not say the herdr board keeps its own setting: %q", m.status)
	}
	standalone := filepath.Join(home, "tktban", "settings.toml")
	if got := settings.Load(standalone)["notify"]; got != false {
		t.Errorf("notify in %s = %v, want false", standalone, got)
	}
}

// A herdr board (settings in the plugin state dir) promises nothing extra.
func TestBKeyInHerdrSaysNothingExtra(t *testing.T) {
	m, _ := testModel(t) // testModel passes an explicit settings path, as --herdr does
	m = loadBoard(m)
	m = step(m, key("b"))
	if m.status != "herdr notifications off" {
		t.Fatalf("status = %q, want the bare line", m.status)
	}
}

func TestFooterListsNotifyKey(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	if !strings.Contains(m.View(), "b notify") {
		t.Fatalf("footer missing the notify key:\n%s", m.renderStatus())
	}
}

// While herdr status is live the subtitle says whether its toasts are muted.
func TestSubtitleMarksNotifyOff(t *testing.T) {
	src := &fakeLive{byKey: live("TKT-1", "working")}
	m, _ := liveBoard(t, src, "")
	m = goLive(t, m)
	if strings.Contains(m.View(), "muted") {
		t.Fatalf("subtitle says muted with notifications on:\n%s", m.View())
	}
	m = step(m, key("b"))
	if !strings.Contains(m.View(), "herdr live (muted)") {
		t.Fatalf("subtitle missing the muted marker:\n%s", m.View())
	}
}

// Off herdr there is no live label to hang the marker on, so nothing is said.
func TestSubtitleQuietWithoutLiveStatus(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	m = step(m, key("b"))
	if strings.Contains(m.View(), "muted") {
		t.Fatalf("subtitle claims muted with no live status:\n%s", m.View())
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
