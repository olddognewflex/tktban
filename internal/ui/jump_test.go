package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
}

func (f *fakeFocus) FocusPane(_ context.Context, paneID string) error {
	f.focused = append(f.focused, paneID)
	return f.focusErr
}

// quits reports whether cmd closes the board. tea.Quit answers at once, while
// the status-expiry tick every other path returns sits on a timer for seconds,
// so a command that has not spoken up promptly is not the quit.
func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msgs := make(chan tea.Msg, 1)
	go func() { msgs <- cmd() }()
	select {
	case msg := <-msgs:
		switch msg := msg.(type) {
		case tea.QuitMsg:
			return true
		case tea.BatchMsg: // a quit batched behind a status still closes the board
			for _, c := range msg {
				if quits(c) {
					return true
				}
			}
		}
		return false
	case <-time.After(250 * time.Millisecond):
		return false
	}
}

// jumped reports whether cmd is a pane.focus rather than a status tick.
func jumped(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msgs := make(chan tea.Msg, 1)
	go func() { msgs <- cmd() }()
	select {
	case msg := <-msgs:
		_, ok := msg.(jumpMsg)
		return ok
	case <-time.After(250 * time.Millisecond):
		return false
	}
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

	m, cmd := update(m, key("o"))
	if m.status != "No agent pane for TKT-1" || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
	if jumped(cmd) {
		t.Fatal("focused a pane although the ticket has none")
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
	m, cmd := update(m, key("o"))
	if m.status != "Live agent status is off" || m.statusKind != "warn" {
		t.Fatalf("no source: status = %q (%s)", m.status, m.statusKind)
	}
	if jumped(cmd) {
		t.Fatal("jumped without a live source")
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
	m, _ := testModel(t)
	m = loadBoard(m)
	m.status = "" // the footer only shows while no status is up
	if !strings.Contains(m.View(), "o agent pane") {
		t.Fatalf("footer missing the jump key:\n%s", m.renderStatus())
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
