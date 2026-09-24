package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olddognewflex/tktban/internal/settings"
	"github.com/olddognewflex/tktban/internal/tkt"
)

// herdrBoard is a loaded board whose settings file is herdr's plugin settings
// file — the one the notification hook reads — as --herdr gives it.
func herdrBoard(t *testing.T) Model {
	t.Helper()
	m, _ := testModel(t)
	return loadBoard(m.WithPluginSettings(true))
}

// TKB-24 AC2: the b key silences the herdr notification hook, both ways, and
// says which way it went.
func TestBKeyTogglesNotify(t *testing.T) {
	m := herdrBoard(t)
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
	m := herdrBoard(t)
	path := m.settingsPath

	m = step(m, key("b"))
	if got := settings.Load(path)["notify"]; got != false {
		t.Fatalf("notify on disk after b = %v, want false (%s)", got, readFile(t, path))
	}

	m = step(m, key("b"))
	if got := settings.Load(path)["notify"]; got != true {
		t.Fatalf("notify on disk after a second b = %v, want true (%s)", got, readFile(t, path))
	}
}

// b flips what the file says now, not what this board read at startup: the
// hook reads the file, and so does a hand edit or a second board. Flipping a
// stale value would write back the value already there and claim a change.
func TestBKeyFlipsWhatIsOnDiskNotWhatWasLoaded(t *testing.T) {
	m := herdrBoard(t)
	path := m.settingsPath
	if err := os.WriteFile(path, []byte("notify = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m = step(m, key("b"))

	if got := settings.Load(path)["notify"]; got != true {
		t.Errorf("notify on disk = %v, want true: b flipped the value loaded at startup (%s)",
			got, readFile(t, path))
	}
	if m.settings["notify"] != true {
		t.Errorf("in-memory notify = %v, want true", m.settings["notify"])
	}
	if m.status != "herdr notifications on" {
		t.Errorf("status = %q, want the on line", m.status)
	}
}

// The hook reads anything that is not the TOML boolean as on (`!ok || v`), so
// the board has to agree: the first b turns it off, not back on.
func TestBKeyNonBooleanNotifyReadsAsOn(t *testing.T) {
	m := herdrBoard(t)
	path := m.settingsPath
	if err := os.WriteFile(path, []byte("notify = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m = step(m, key("b"))

	if got := settings.Load(path)["notify"]; got != false {
		t.Errorf("notify on disk = %v, want false (%s)", got, readFile(t, path))
	}
	if m.status != "herdr notifications off" {
		t.Errorf("status = %q, want the off line", m.status)
	}
}

// The toggle goes through the board's own write path: everything else in the
// file stays as it is, including a key this version does not know and the
// board's own theme / hidden_roles.
func TestBKeyKeepsOtherKeys(t *testing.T) {
	m := herdrBoard(t)
	path := m.settingsPath
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

// A save the board cannot make must not leave the board claiming it did: the
// in-memory value goes back, so the subtitle cannot outrun the file.
func TestBKeySaveFailureKeepsBoardHonest(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithPluginSettings(true)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.settingsPath = filepath.Join(blocker, "settings.toml") // parent is a file
	m = loadBoard(m)

	m = step(m, key("b"))

	if !strings.Contains(m.status, "not saved") || m.statusKind != "warn" {
		t.Errorf("status = %q (%q), want a warned not-saved line", m.status, m.statusKind)
	}
	if m.settings["notify"] != true {
		t.Errorf("in-memory notify = %v after a failed save, want the unchanged true", m.settings["notify"])
	}
}

// Outside herdr's own board the settings file is not the one the hook reads,
// so the status line says where the flip landed rather than promising quiet.
func TestBKeyOutsideHerdrSaysWhereItLanded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	cr := &captureRunner{}
	m := New(tkt.New("", "tkt").WithRunner(cr.run), 10, true, "") // no plugin settings file
	m.width, m.height = 120, 30
	m = loadBoard(m)

	m = step(m, key("b"))
	if m.status != "herdr notifications off (herdr's own board keeps a separate setting)" {
		t.Fatalf("status = %q", m.status)
	}
	standalone := filepath.Join(home, "tktban", "settings.toml")
	if got := settings.Load(standalone)["notify"]; got != false {
		t.Errorf("notify in %s = %v, want false", standalone, got)
	}
}

// herdr's own board promises nothing extra.
func TestBKeyInHerdrSaysNothingExtra(t *testing.T) {
	m := herdrBoard(t)
	m = step(m, key("b"))
	if m.status != "herdr notifications off" {
		t.Fatalf("status = %q, want the bare line", m.status)
	}
}

// saveSettings writes settings, not whatever the caller names: a key the
// settings package does not know never reaches the file.
func TestSaveSettingsIgnoresUnknownKeys(t *testing.T) {
	m := herdrBoard(t)
	m.settings["bogus"] = "nope"
	if err := m.saveSettings("bogus"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, m.settingsPath); strings.Contains(got, "bogus") {
		t.Fatalf("unknown key written:\n%s", got)
	}
}

func TestFooterListsNotifyKey(t *testing.T) {
	if !strings.Contains(footerKeys, "b notify") {
		t.Fatalf("footer missing the notify key: %s", footerKeys)
	}
}

// ... and the footer is what an idle board actually shows.
func TestFooterRendersWhenNoStatus(t *testing.T) {
	m := herdrBoard(t)
	m.status = ""
	if !strings.Contains(m.renderStatus(), "r refresh") {
		t.Fatalf("idle board does not render the key hints:\n%s", m.renderStatus())
	}
}

// While herdr status is live, herdr's own board says whether its toasts are
// muted.
func TestSubtitleMarksNotifyOff(t *testing.T) {
	src := &fakeLive{byKey: live("TKT-1", "working")}
	m, _ := liveBoard(t, src, "")
	m = goLive(t, m.WithPluginSettings(true))
	if strings.Contains(m.View(), "muted") {
		t.Fatalf("subtitle says muted with notifications on:\n%s", m.View())
	}
	m = step(m, key("b"))
	if !strings.Contains(m.View(), "herdr live (muted)") {
		t.Fatalf("subtitle missing the muted marker:\n%s", m.View())
	}
}

// Live status comes on in any herdr pane, but a board that is not writing the
// plugin settings file has muted nothing: the hook reads that file and keeps
// toasting. The subtitle must not say otherwise.
func TestSubtitleNeverClaimsMutedOffThePluginFile(t *testing.T) {
	src := &fakeLive{byKey: live("TKT-1", "working")}
	m, _ := liveBoard(t, src, "") // live on, standalone settings: a plain herdr pane
	m = goLive(t, m)
	m = step(m, key("b"))
	if m.settings["notify"] != false {
		t.Fatalf("setup: b did not flip notify (%v)", m.settings["notify"])
	}
	view := m.View()
	if strings.Contains(view, "muted") {
		t.Fatalf("subtitle claims muted while the hook reads another file:\n%s", view)
	}
	if !strings.Contains(view, "herdr live") {
		t.Fatalf("subtitle lost the live label:\n%s", view)
	}
}

// Off herdr there is no live label to hang the marker on, so nothing is said.
func TestSubtitleQuietWithoutLiveStatus(t *testing.T) {
	m := herdrBoard(t)
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
