package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olddognewflex/tktban/internal/settings"
	"github.com/olddognewflex/tktban/internal/tkt"
)

// TKB-32: P switches the board between this project (tkt's ticketing.project,
// "TKT" in captureRunner) and every project on the shared board.

// pressP runs the P key the way the program would — the scope read when
// widening, then the refresh it asked for — and returns the loaded board.
func pressP(t *testing.T, m Model) Model {
	t.Helper()
	wasAll := m.allProjects
	m, cmd := update(m, key("P"))
	if !wasAll {
		msg, ok := cmd().(scopeMsg)
		if !ok {
			t.Fatal("P on a scoped board did not read the scope")
		}
		m, _ = update(m, msg)
	}
	return loadBoard(m)
}

// lastList is the last ticket list the board ran (not the help probe).
func lastList(cr *captureRunner) []string {
	for i := len(cr.calls) - 1; i >= 0; i-- {
		if c := cr.calls[i]; len(c) > 0 && c[0] == "list" && !slices.Contains(c, "--help") {
			return c
		}
	}
	return nil
}

func flagged(argv []string) bool { return slices.Contains(argv, "--all-projects") }

func TestScopeStartsScoped(t *testing.T) {
	m, cr := testModel(t)
	cr.allProjects = true
	m = loadBoard(m)
	if m.allProjects || flagged(lastList(cr)) {
		t.Fatalf("a fresh board listed every project: %v", lastList(cr))
	}
	if !strings.Contains(m.View(), "scope: this project") {
		t.Fatalf("subtitle missing the scope:\n%s", m.View())
	}
	if strings.Contains(m.View(), "OPS-7") {
		t.Fatal("another project's card on a scoped board")
	}
}

func TestScopeTogglePassesTheFlag(t *testing.T) {
	m, cr := testModel(t)
	cr.allProjects = true
	m = loadBoard(m)

	m = pressP(t, m)
	if !m.allProjects || !flagged(lastList(cr)) {
		t.Fatalf("P did not list every project: %v", lastList(cr))
	}
	view := m.View()
	for _, want := range []string{"OPS-7", "TKT-1", "scope: all projects"} {
		if !strings.Contains(view, want) {
			t.Fatalf("whole board missing %q:\n%s", want, view)
		}
	}

	m = pressP(t, m)
	if m.allProjects || flagged(lastList(cr)) {
		t.Fatalf("second P did not go back to this project: %v", lastList(cr))
	}
	if strings.Contains(m.View(), "OPS-7") || !strings.Contains(m.View(), "scope: this project") {
		t.Fatalf("scoped board after P P:\n%s", m.View())
	}
}

// The f prefix filter still narrows the whole board.
func TestScopeFilterNarrowsTheWholeBoard(t *testing.T) {
	m, cr := testModel(t)
	cr.allProjects = true
	m = pressP(t, loadBoard(m))
	m = loadBoard(step(m, filterResultMsg{prefix: "OPS"}))
	view := m.View()
	if !strings.Contains(view, "OPS-7") || strings.Contains(view, "TKT-1") {
		t.Fatalf("prefix filter on the whole board:\n%s", view)
	}
	if !flagged(lastList(cr)) {
		t.Fatal("filtering dropped the scope")
	}
}

func TestScopePersists(t *testing.T) {
	m, cr := testModel(t)
	cr.allProjects = true
	m = pressP(t, loadBoard(m))
	if got := settings.Load(m.settingsPath)["all_projects"]; got != true {
		t.Fatalf("all_projects on disk = %v, want true", got)
	}

	// A restart comes back on the whole board.
	m2 := New(m.tkt, 10, true, m.settingsPath)
	m2.width, m2.height = 120, 30
	m2 = loadBoard(m2)
	if !m2.allProjects || !flagged(lastList(cr)) || !strings.Contains(m2.View(), "OPS-7") {
		t.Fatalf("scope not restored after restart: %v", lastList(cr))
	}

	m2 = pressP(t, m2)
	if got := settings.Load(m.settingsPath)["all_projects"]; got != false {
		t.Fatalf("all_projects on disk = %v after going back, want false", got)
	}
}

// An old tkt (no TKT-70) or no project: P says why and the board stays as it
// was, cards and all.
func TestScopeRefusals(t *testing.T) {
	cases := []struct {
		name string
		cr   captureRunner
		want string
	}{
		{"old tkt", captureRunner{}, "--all-projects"},
		{"no project", captureRunner{allProjects: true, project: failReply}, "ticketing.project"},
	}
	for _, c := range cases {
		m, cr := testModel(t)
		*cr = c.cr
		m = pressP(t, loadBoard(m))
		if m.allProjects || flagged(lastList(cr)) {
			t.Errorf("%s: board widened anyway: %v", c.name, lastList(cr))
		}
		if m.statusKind != "warn" || !strings.Contains(m.status, c.want) {
			t.Errorf("%s: status = %q (%s), want a warning naming %q", c.name, m.status, m.statusKind, c.want)
		}
		if !strings.Contains(m.View(), "TKT-1") {
			t.Errorf("%s: board blanked:\n%s", c.name, m.View())
		}
		if got := settings.Load(m.settingsPath)["all_projects"]; got != false {
			t.Errorf("%s: a refused P saved all_projects = %v", c.name, got)
		}
	}
}

// A persisted whole-board scope on a tkt that cannot list it: the board opens
// scoped with a warning, and the setting waits for a newer tkt.
func TestScopePersistedOnOldTktFallsBack(t *testing.T) {
	cr := &captureRunner{}
	path := filepath.Join(t.TempDir(), "settings.toml")
	if err := os.WriteFile(path, []byte("all_projects = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(tkt.New("", "tkt").WithRunner(cr.run), 10, true, path)
	m.width, m.height = 120, 30
	m = loadBoard(m)
	if !m.loaded || !strings.Contains(m.View(), "TKT-1") {
		t.Fatalf("board did not load scoped:\n%s", m.View())
	}
	if m.allProjects || !strings.Contains(m.View(), "scope: this project") {
		t.Fatal("board still claims the whole board")
	}
	if m.statusKind != "warn" || !strings.Contains(m.status, "--all-projects") {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
	for _, c := range cr.calls {
		if flagged(c) {
			t.Fatalf("an old tkt was passed the flag: %v", c)
		}
	}
	if got := settings.Load(path)["all_projects"]; got != true {
		t.Fatalf("fallback rewrote all_projects to %v", got)
	}
}

func TestScopeDispatchRefusesAnotherProjectsCard(t *testing.T) {
	m, src, cr := dispatchBoard(t)
	cr.allProjects = true
	m = step(pressP(t, m), key("k")) // the selection stayed on TKT-1
	if card, _ := m.selectedCard(); card.Key != "OPS-7" {
		t.Fatalf("selected %q, want OPS-7", card.Key)
	}
	m = pressD(t, m)
	if m.dispatching || len(src.calls) > 0 {
		t.Fatalf("dispatched another project's card: herdr calls %v", src.calls)
	}
	if !strings.Contains(m.status, "OPS-7 is another project's ticket") {
		t.Fatalf("status = %q", m.status)
	}

	// This project's card on the same board still dispatches.
	m = step(m, key("j"))
	if card, _ := m.selectedCard(); card.Key != "TKT-1" {
		t.Fatalf("selected %q, want TKT-1", card.Key)
	}
	m = pressD(t, m)
	if len(src.listed) == 0 {
		t.Fatalf("this project's card did not dispatch: status %q", m.status)
	}
}

// Each project's own board archives its own: OPS-8 has been in done as long
// as TKT-2, and only TKT-2 is swept.
func TestScopeArchiveSkipsOtherProjects(t *testing.T) {
	m, cr := archiveModel(t, "all_projects = true\n")
	cr.allProjects = true
	cr.laneSeconds["OPS-8"] = 8 * day
	cr.viewRole["OPS-8"] = "done"
	msg, ok := m.refreshCmd()().(boardMsg)
	if !ok || msg.err != nil || msg.project != "TKT" {
		t.Fatalf("refresh = %+v", msg)
	}
	if !slices.Equal(msg.archive, []string{"TKT-2"}) {
		t.Fatalf("archive candidates = %v, want [TKT-2]", msg.archive)
	}
}

// New tickets go to this project on the whole board too: create is the plain
// verb, which tkt files under ticketing.project.
func TestScopeNewTicketUsesThisProject(t *testing.T) {
	m, cr := testModel(t)
	cr.allProjects = true
	m = pressP(t, loadBoard(m))
	_, cmd := m.Update(createSubmitMsg{payload: createPayload{issueType: "Task", summary: "x"}})
	if cm, ok := cmd().(createMsg); !ok || cm.err != nil {
		t.Fatalf("create = %+v", cm)
	}
	want := []string{"create", "--type", "Task", "--summary", "x", "--json"}
	if got := cr.last("create"); !slices.Equal(got, want) {
		t.Fatalf("create argv = %v, want %v", got, want)
	}
}

// Moving another project's card is an ordinary write on the shared board.
func TestScopeMoveWorksOnAnotherProjectsCard(t *testing.T) {
	m, cr := testModel(t)
	cr.allProjects = true
	m = step(pressP(t, loadBoard(m)), key("k")) // OPS-7 sorts above TKT-1
	_, cmd := m.Update(moveResultMsg{role: "done"})
	if wm, ok := cmd().(writeMsg); !ok || wm.err != nil {
		t.Fatalf("move = %+v", wm)
	}
	if got := cr.last("transition"); !slices.Equal(got, []string{"transition", "OPS-7", "done"}) {
		t.Fatalf("transition argv = %v", got)
	}
}

// A refresh asked for in one scope that lands after P changed it is dropped,
// in both directions, rather than shown under the other scope's label.
func TestScopeDropsAStaleRefresh(t *testing.T) {
	m, cr := testModel(t)
	cr.allProjects = true
	m = pressP(t, loadBoard(m))
	wide := m.refreshCmd()() // whole-board load, still in flight
	m = pressP(t, m)         // narrows, and its own scoped load lands first
	m = step(m, wide)
	if strings.Contains(m.View(), "OPS-7") || m.project != "" {
		t.Fatalf("a stale whole-board load landed on a scoped board:\n%s", m.View())
	}

	narrow := m.refreshCmd()() // scoped load, still in flight
	m = pressP(t, m)
	m = step(m, narrow)
	if !strings.Contains(m.View(), "OPS-7") || m.project != "TKT" {
		t.Fatalf("a stale scoped load replaced the whole board:\n%s", m.View())
	}
}

// P twice before tkt has answered the first: the second press cancels the
// first, and the board stays on this project.
func TestScopeSecondPressCancelsAPendingWiden(t *testing.T) {
	m, cr := testModel(t)
	cr.allProjects = true
	m = loadBoard(m)
	m, first := update(m, key("P"))
	m, _ = update(m, key("P"))
	m, cmd := update(m, first())
	if m.allProjects || cmd != nil {
		t.Fatalf("a cancelled widen widened anyway (allProjects=%v)", m.allProjects)
	}
	if got := settings.Load(m.settingsPath)["all_projects"]; got != false {
		t.Fatalf("a cancelled widen saved all_projects = %v", got)
	}

	// A third press asks again; only its answer counts.
	m, third := update(m, key("P"))
	m = step(m, first())
	if m.allProjects {
		t.Fatal("an old answer widened the board")
	}
	m = step(m, third())
	if !m.allProjects {
		t.Fatal("the live press's answer was dropped")
	}
}
