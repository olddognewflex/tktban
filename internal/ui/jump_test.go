package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/model"
)

// fakeFocus is a fakeLive that can also focus panes, recording every pane id
// it was asked for. A plain fakeLive deliberately cannot: the board must still
// work with a source that only reads status.
type fakeFocus struct {
	fakeLive
	focused  []string
	focusErr error
	// listed records worktree.list calls. fakeFocus is a Dispatcher as well
	// as a PaneFocuser so the inert-behind-a-modal table can prove that D
	// does nothing from behind one either (TKB-25).
	listed []string
}

func (f *fakeFocus) FocusPane(_ context.Context, paneID string) error {
	f.focused = append(f.focused, paneID)
	return f.focusErr
}

func (f *fakeFocus) WorktreeList(_ context.Context, cwd string) (herdr.WorktreeListResult, error) {
	f.listed = append(f.listed, cwd)
	return herdr.WorktreeListResult{}, nil
}

// quits reports whether cmd closes the board. tea.Quit is declared as a
// genuine top-level function (`func Quit() Msg`), not a closure minted by a
// generic helper, so its code pointer is the same wherever it's referenced —
// stable across platforms and inlining decisions. A closure returned from a
// helper (jumpCmd, or bubbletea's own Batch/compactCmds) has no such
// guarantee: the compiler is free to duplicate its body per call site under
// more aggressive inlining, which is exactly what made an earlier version of
// this file's identity-sampled jumped()/batchID checks flaky on linux/amd64
// CI while passing locally. No jump path in this package ever wraps tea.Quit
// in a tea.Batch, so recognizing tea.Quit itself is sufficient; tests that
// need to know whether a jump actually ran check the observable effect
// instead (the resulting jumpMsg, or the fake's recorded FocusPane calls).
func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	return reflect.ValueOf(cmd).Pointer() == reflect.ValueOf(tea.Quit).Pointer()
}

// panes builds a live map with one ticket's panes spelled out.
func panes(key string, st herdr.Status, refs ...herdr.PaneRef) map[string]herdr.Live {
	return map[string]herdr.Live{key: {Status: st, Panes: refs}}
}

func ref(id string, st herdr.Status, focused bool) herdr.PaneRef {
	return herdr.PaneRef{PaneID: id, WorkspaceID: "wC", TabID: "wC:t1", Status: st, Focused: focused}
}

// jumpBoard is a loaded, live board whose TKT-1 has the given panes.
func jumpBoard(t *testing.T, src *fakeFocus) Model {
	t.Helper()
	m, _ := testModel(t)
	m = m.WithLive(src)
	m = loadBoard(m)
	return goLive(t, m)
}

// AC1: o focuses the herdr pane of the selected card's agent, over the socket,
// with the pane PickPane chose.
func TestJumpFocusesSelectedCardsPane(t *testing.T) {
	src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusBlocked,
		ref("wC:p1", herdr.StatusWorking, false),
		ref("wD:p9", herdr.StatusBlocked, false),
	)}}
	m := jumpBoard(t, src)

	_, cmd := m.Update(key("o"))
	if cmd == nil {
		t.Fatal("o did not start a jump")
	}
	msg, ok := cmd().(jumpMsg)
	if !ok {
		t.Fatalf("o produced %T, want jumpMsg", cmd())
	}
	if msg.err != nil || msg.key != "TKT-1" || msg.paneID != "wD:p9" {
		t.Fatalf("jump = %+v, want the blocked pane wD:p9 of TKT-1", msg)
	}
	if strings.Join(src.focused, ",") != "wD:p9" {
		t.Fatalf("focused %v, want exactly [wD:p9]", src.focused)
	}
}

// The board is a herdr popup with no pane id of its own: the focus round trip
// is already complete, so quitting (which closes the popup) is what leaves the
// agent pane in front.
func TestJumpQuitsPopupOnSuccess(t *testing.T) {
	src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
	m := jumpBoard(t, src).WithPopup(true)

	m, cmd := update(m, jumpMsg{key: "TKT-1", paneID: "wC:p1"})
	if !quits(cmd) {
		t.Fatal("a successful jump in a popup must quit so the popup closes")
	}
	if m.status != "" {
		t.Fatalf("status %q shown on a board that is about to exit", m.status)
	}
}

// Outside a popup (a board in a plain herdr pane) there is nothing to close:
// the board stays open and says where focus went.
func TestJumpKeepsBoardOpenOutsidePopup(t *testing.T) {
	src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
	m := jumpBoard(t, src)

	m, cmd := update(m, jumpMsg{key: "TKT-1", paneID: "wC:p1"})
	if quits(cmd) {
		t.Fatal("a board outside a popup must not quit on a jump")
	}
	if m.status != "Focused TKT-1 agent pane" || m.statusKind != "" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
}

func TestJumpNoPaneWarns(t *testing.T) {
	src := &fakeFocus{fakeLive: fakeLive{byKey: map[string]herdr.Live{}}}
	m := jumpBoard(t, src)

	m, _ = update(m, key("o"))
	if m.status != "No agent pane for TKT-1" || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
	if len(src.focused) != 0 {
		t.Fatalf("focused %v with no pane to jump to", src.focused)
	}
	// A ticket herdr knows but whose panes have all closed is the same case.
	src.byKey = panes("TKT-1", herdr.StatusUnknown)
	m = poll(t, m)
	m, _ = update(m, key("o"))
	if m.status != "No agent pane for TKT-1" || len(src.focused) != 0 {
		t.Fatalf("empty pane list: status = %q focused = %v", m.status, src.focused)
	}
}

// Live off has two shapes — no source at all (outside herdr) and a source that
// is not answering — and each says so rather than doing nothing.
func TestJumpLiveOffWarns(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	m, _ = update(m, key("o"))
	if m.status != "Live agent status is off" || m.statusKind != "warn" {
		t.Fatalf("no source: status = %q (%s)", m.status, m.statusKind)
	}

	// A source that has gone quiet: still a source, but its pane map is stale,
	// so the jump refuses rather than focusing a pane that may be gone.
	src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
	m2 := jumpBoard(t, src)
	src.pollErr = errors.New("connection refused")
	for range liveMaxFails {
		m2 = poll(t, m2)
	}
	if m2.live.on {
		t.Fatal("setup: live should be off after the failure threshold")
	}
	m2, _ = update(m2, key("o"))
	if m2.status != "herdr live status unavailable" || m2.statusKind != "warn" {
		t.Fatalf("live off: status = %q (%s)", m2.status, m2.statusKind)
	}
	if len(src.focused) != 0 {
		t.Fatalf("focused %v while live status was off", src.focused)
	}
}

func TestJumpNoSelectionWarns(t *testing.T) {
	src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
	m := jumpBoard(t, src)
	// Empty every column, so there is nothing selected to jump from.
	for i := range m.columns {
		m.columns[i].Cards = nil
	}
	m, _ = update(m, key("o"))
	if m.status != "Select a card first" || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
	if len(src.focused) != 0 {
		t.Fatalf("focused %v with no card selected", src.focused)
	}
}

// The pane closed between the last poll and the keypress: herdr says
// pane_not_found, and the board stays open and says so rather than exiting to
// a pane that is no longer there.
func TestJumpPaneGoneWarns(t *testing.T) {
	src := &fakeFocus{
		fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))},
		focusErr: &herdr.APIError{Code: "pane_not_found", Message: "pane wC:p1 not found"},
	}
	m := jumpBoard(t, src).WithPopup(true)

	_, cmd := m.Update(key("o"))
	msg, ok := cmd().(jumpMsg)
	if !ok || msg.err == nil {
		t.Fatalf("want a failed jumpMsg, got %#v", cmd())
	}
	m, cmd = update(m, msg)
	if m.status != "Agent pane for TKT-1 is gone" || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
	if quits(cmd) {
		t.Fatal("a failed jump closed the board")
	}

	// Any other herdr failure is reported too, just without the gone wording.
	m, _ = update(m, jumpMsg{key: "TKT-1", paneID: "wC:p1", err: context.DeadlineExceeded})
	if m.statusKind != "warn" || !strings.Contains(m.status, "TKT-1") {
		t.Fatalf("timeout: status = %q (%s)", m.status, m.statusKind)
	}
}

// AC2: --select lands the board on that ticket, selecting its column and card.
func TestSelectKeySelectsCardOnLoad(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithSelect("tkt-2") // case-insensitive, as a branch spells it
	m = loadBoard(m)
	card, ok := m.selectedCard()
	if !ok || card.Key != "TKT-2" {
		t.Fatalf("selected %v ok=%v, want TKT-2", card.Key, ok)
	}
	if m.columns[m.focusCol].Role != "done" {
		t.Fatalf("focused column = %q, want TKT-2's done", m.columns[m.focusCol].Role)
	}
	if m.status != "Selected TKT-2" || m.statusKind != "" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
}

func TestSelectKeyMissingWarns(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithSelect("TKB-404")
	m = loadBoard(m)
	if m.status != "TKB-404 is not on the board" || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
	// The board is still usable, on its normal default selection.
	card, ok := m.selectedCard()
	if !ok || card.Key != "TKT-1" {
		t.Fatalf("board unusable after a miss: %v ok=%v", card.Key, ok)
	}
}

// A ticket in a column the user hid is found but cannot be selected; the warn
// names the lane and the key that brings it back.
func TestSelectKeyInHiddenColumnWarns(t *testing.T) {
	m, _ := testModel(t)
	m.hidden = map[string]bool{"done": true}
	m = m.WithSelect("TKT-2")
	m = loadBoard(m)
	if m.statusKind != "warn" {
		t.Fatalf("status kind = %q, want warn (%q)", m.statusKind, m.status)
	}
	for _, want := range []string{"TKT-2", "Done", "X"} {
		if !strings.Contains(m.status, want) {
			t.Fatalf("status %q missing %q", m.status, want)
		}
	}
	if card, _ := m.selectedCard(); card.Key == "TKT-2" {
		t.Fatal("selected a card in a hidden column")
	}
}

// The key is consumed by the load that applies it: a later auto-refresh must
// not yank the selection back from wherever the user moved it.
func TestSelectKeyConsumedOnce(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithSelect("TKT-2")
	m = loadBoard(m)
	if m.selectKey != "" {
		t.Fatalf("select key not consumed: %q", m.selectKey)
	}
	m = step(m, key("h")) // back to the todo column
	m.status, m.statusKind = "", ""
	m = loadBoard(m) // auto-refresh
	card, ok := m.selectedCard()
	if !ok || card.Key != "TKT-1" {
		t.Fatalf("refresh re-applied --select: selected %v", card.Key)
	}
	if m.status != "" {
		t.Fatalf("refresh re-announced the selection: %q", m.status)
	}
}

func TestFooterShowsAgentPaneKey(t *testing.T) {
	// Against the key line itself, not a render of it: the terminal wraps that
	// line wherever the width falls, which would split a hint in two and fail
	// for no reason. That the line reaches the screen is
	// TestFooterListsEveryKey's job.
	if !strings.Contains(footerKeys, "o agent pane") {
		t.Fatalf("footer missing the jump key: %s", footerKeys)
	}
}

// The jump reads the card's key case-insensitively, exactly as the badges do.
func TestJumpKeyIsCaseInsensitive(t *testing.T) {
	src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
	m := jumpBoard(t, src)
	m.columns[0].Cards[0] = model.Card{Key: "tkt-1"}
	_, cmd := m.Update(key("o"))
	msg, ok := cmd().(jumpMsg)
	if !ok || msg.paneID != "wC:p1" || msg.key != "TKT-1" {
		t.Fatalf("lowercase card key did not resolve: %#v", cmd())
	}
}

// selectCmd is the command a --select-from-cwd board runs to work its key out;
// tests fire it when they want it to land.
func selectCmd(t *testing.T, m Model) tea.Cmd {
	t.Helper()
	cmd := m.selectInit()
	if cmd == nil {
		t.Fatal("no derive command with a select func")
	}
	return cmd
}

// A derived key that lands after the board has loaded is applied at once,
// rather than waiting for the next refresh.
func TestSelectFuncAppliesAfterLoad(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithSelectFunc(func() string { return "tkt-2" })
	cmd := selectCmd(t, m)
	m = loadBoard(m) // the board paints before the key is known
	if c, _ := m.selectedCard(); c.Key != "TKT-1" {
		t.Fatalf("setup: board should open on its usual card, got %v", c.Key)
	}
	m, _ = update(m, cmd())
	card, ok := m.selectedCard()
	if !ok || card.Key != "TKT-2" {
		t.Fatalf("selected %v ok=%v, want TKT-2", card.Key, ok)
	}
	if m.status != "Selected TKT-2" || m.statusKind != "" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
	if m.selectKey != "" {
		t.Fatalf("select key not consumed: %q", m.selectKey)
	}
}

// A derived key that lands before the first load waits for it.
func TestSelectFuncAppliesOnLoad(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithSelectFunc(func() string { return "TKT-2" })
	m, cmd := update(m, selectCmd(t, m)())
	if cmd != nil {
		t.Fatal("a key that arrived before the board must wait for it")
	}
	if m.selectKey != "TKT-2" {
		t.Fatalf("key not held for the load: %q", m.selectKey)
	}
	m = loadBoard(m)
	if card, _ := m.selectedCard(); card.Key != "TKT-2" {
		t.Fatalf("selected %v, want TKT-2", card.Key)
	}
}

// An empty derivation (a branch naming no ticket) changes nothing at all.
func TestSelectFuncEmptyIsQuiet(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithSelectFunc(func() string { return "" })
	m, _ = update(m, selectCmd(t, m)())
	m = loadBoard(m)
	if card, _ := m.selectedCard(); card.Key != "TKT-1" {
		t.Fatalf("selected %v, want the usual TKT-1", card.Key)
	}
	if m.status != "" {
		t.Fatalf("status = %q, want silence", m.status)
	}
}

// --select wins over a key derived from the branch, whenever it lands.
func TestExplicitSelectBeatsDerived(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithSelect("TKT-1").WithSelectFunc(func() string { return "TKT-2" })
	cmd := selectCmd(t, m)
	m = loadBoard(m)
	m, _ = update(m, cmd())
	if card, _ := m.selectedCard(); card.Key != "TKT-1" {
		t.Fatalf("derived key overrode --select: selected %v", card.Key)
	}
}

// A hidden column is a standing choice of the user's. Saying so is useful
// when they asked for that ticket by name, and nagging when the board worked
// the key out for itself on an open it will repeat every day.
func TestSelectKeyInHiddenColumnQuietWhenDerived(t *testing.T) {
	m, _ := testModel(t)
	m.hidden = map[string]bool{"done": true}
	m = m.WithSelectFunc(func() string { return "TKT-2" })
	m, _ = update(m, selectCmd(t, m)())
	m = loadBoard(m)
	if m.statusKind != "" {
		t.Fatalf("derived key warned about a hidden column: %q (%s)", m.status, m.statusKind)
	}
	if !strings.Contains(m.status, "TKT-2") || !strings.Contains(m.status, "Done") {
		t.Fatalf("status %q should still say where TKT-2 is", m.status)
	}
}

// A branch whose ticket is not on this board at all is not worth a word: the
// board just opens.
func TestSelectKeyDerivedMissIsSilent(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithSelectFunc(func() string { return "TKB-404" })
	m, _ = update(m, selectCmd(t, m)())
	m = loadBoard(m)
	if m.status != "" {
		t.Fatalf("status = %q, want silence for a derived miss", m.status)
	}
	if card, _ := m.selectedCard(); card.Key != "TKT-1" {
		t.Fatalf("selected %v, want the usual TKT-1", card.Key)
	}
}

// An empty or junk --select must not break the board.
func TestSelectKeyOddValues(t *testing.T) {
	cases := []struct {
		name       string
		key        string
		wantStatus string
		wantKind   string
	}{
		{"empty", "", "", ""},
		{"whitespace only", "   ", "", ""},
		{"junk", "not a key", "NOT A KEY is not on the board", "warn"},
		{"a key of the wrong project", "OTHER-1", "OTHER-1 is not on the board", "warn"},
	}
	for _, c := range cases {
		m, _ := testModel(t)
		m = m.WithSelect(c.key)
		m = loadBoard(m)
		if m.status != c.wantStatus || m.statusKind != c.wantKind {
			t.Errorf("%s: status = %q (%s), want %q (%s)", c.name, m.status, m.statusKind, c.wantStatus, c.wantKind)
		}
		card, ok := m.selectedCard()
		if !ok || card.Key != "TKT-1" {
			t.Errorf("%s: board unusable, selected %v ok=%v", c.name, card.Key, ok)
		}
	}
}

// A board warning and a selection on the same load: the warning is the rarer
// thing, so it stays on screen, and the selection still moved.
func TestSelectKeyKeepsBoardWarning(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithSelect("TKT-2")
	m, _ = update(m, boardMsg{
		roles:   []model.RolePair{{Role: "todo", Lane: "To Do"}, {Role: "done", Lane: "Done"}},
		columns: []model.Column{{Lane: "To Do", Role: "todo", Cards: []model.Card{{Key: "TKT-1"}}}, {Lane: "Done", Role: "done", Cards: []model.Card{{Key: "TKT-2"}}}},
		warn:    "lane-time unavailable",
	})
	if m.status != "lane-time unavailable" || m.statusKind != "warn" {
		t.Fatalf("board warning swallowed: status = %q (%s)", m.status, m.statusKind)
	}
	if card, _ := m.selectedCard(); card.Key != "TKT-2" {
		t.Fatalf("selection lost to the warning: %v", card.Key)
	}
}

// Keys belong to the modal while one is open: o and ga must type into it, not
// jump behind it.
func TestJumpInertWhileModalOpen(t *testing.T) {
	src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
	for _, keys := range [][]string{{"o"}, {"g", "a"}} {
		m := jumpBoard(t, src).WithPopup(true)
		m = step(m, key("f")) // open the filter modal
		if m.modal == nil {
			t.Fatal("setup: filter modal did not open")
		}
		var cmd tea.Cmd
		for _, k := range keys {
			m, cmd = update(m, key(k))
			if quits(cmd) {
				t.Fatalf("%v behind a modal quit the board", keys)
			}
		}
		if m.modal == nil {
			t.Fatalf("%v closed the modal", keys)
		}
		if len(src.focused) != 0 {
			t.Fatalf("%v focused %v from behind a modal", keys, src.focused)
		}
	}
}

// A live source that only reads status (no FocusPane) drives the badges but
// cannot jump, and says that rather than claiming live status is off.
func TestJumpStatusOnlySourceWarns(t *testing.T) {
	src := &fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}
	m, _ := testModel(t)
	m = goLive(t, loadBoard(m.WithLive(src)))
	if !m.live.on {
		t.Fatal("setup: a status-only source must still go live")
	}
	m, _ = update(m, key("o"))
	if m.status != "This board can't focus herdr panes" || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
}
