// QA Author — adversarial tests
// Break-It dimensions covered: an APIError code the UI does not special-case,
// that a failed jump never quits under any error shape, that focusing is the
// jump command's side effect (not Update's), that jumpCmd bounds itself with
// a context deadline, vim counts and case on the o/ga keys, jumping with no
// columns at all or before the board has loaded, o/ga inertness under every
// modal kind that's cheap to open, and selectKey/selectFunc edge cases: a key
// present in several columns, an empty board, explicit arriving after a
// derived key was already queued, and a derived selection surviving an
// auto-refresh without re-announcing.
package ui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/model"
)

// An APIError herdr sends that is neither nil nor pane_not_found (e.g. some
// future or unexpected failure code) must fall through to the generic wording,
// not the "is gone" wording reserved for a pane that's actually disappeared.
func TestJumpUnexpectedAPIErrorWarnsGenerically(t *testing.T) {
	src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
	m := jumpBoard(t, src)

	m, cmd := update(m, jumpMsg{key: "TKT-1", paneID: "wC:p1", err: &herdr.APIError{Code: "internal_error", Message: "boom"}})
	if m.status != "Could not focus TKT-1 agent pane" || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s), want the generic wording", m.status, m.statusKind)
	}
	if quits(cmd) {
		t.Fatal("an unexpected APIError quit the board")
	}
}

// No shape of jump failure may ever quit the board, popup or not: a failed
// jump leaves nothing focused, so exiting would just abandon the user at a
// closed popup or an unexplained blank screen.
func TestJumpFailureNeverQuitsAnyErrorKind(t *testing.T) {
	errs := []error{
		errors.New("connection refused"),
		context.DeadlineExceeded,
		&herdr.APIError{Code: "pane_not_found", Message: "gone"},
		&herdr.APIError{Code: "agent_not_found", Message: "boom"},
		&herdr.APIError{Code: "", Message: "empty code"},
	}
	for _, popup := range []bool{false, true} {
		for _, e := range errs {
			src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
			m := jumpBoard(t, src).WithPopup(popup)
			m, cmd := update(m, jumpMsg{key: "TKT-1", paneID: "wC:p1", err: e})
			if quits(cmd) {
				t.Fatalf("popup=%v err=%v: a failed jump quit the board", popup, e)
			}
			if m.statusKind != "warn" {
				t.Fatalf("popup=%v err=%v: status kind = %q, want warn", popup, e, m.statusKind)
			}
		}
	}
}

// jumpToAgentPane must only ever *return* the focus call as a tea.Cmd; it must
// not focus the pane itself as a side effect of handling the keypress. Running
// the returned command is what a real bubbletea loop does, and only then may
// the pane actually be focused.
func TestJumpIsDeferredNotASideEffectOfUpdate(t *testing.T) {
	src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
	m := jumpBoard(t, src)

	_, cmd := m.Update(key("o"))
	if cmd == nil {
		t.Fatal("o did not produce a command")
	}
	if len(src.focused) != 0 {
		t.Fatalf("FocusPane called before the returned command ran: %v", src.focused)
	}
	cmd()
	if len(src.focused) != 1 {
		t.Fatalf("FocusPane not called after running the command: %v", src.focused)
	}
}

// capturingFocus records the context it was called with, so jumpCmd's own
// bounding can be checked without any wall-clock wait: a deadline merely
// needs to exist, not be measured.
type capturingFocus struct {
	fakeLive
	gotCtx context.Context
}

func (f *capturingFocus) FocusPane(ctx context.Context, _ string) error {
	f.gotCtx = ctx
	return nil
}

func TestJumpCmdBoundsCallWithADeadline(t *testing.T) {
	src := &capturingFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
	m, _ := testModel(t)
	m = goLive(t, loadBoard(m.WithLive(src)))

	_, cmd := m.Update(key("o"))
	if cmd == nil {
		t.Fatal("o did not produce a command")
	}
	cmd()
	if src.gotCtx == nil {
		t.Fatal("FocusPane never ran")
	}
	if _, ok := src.gotCtx.Deadline(); !ok {
		t.Fatal("jumpCmd called FocusPane with a context that has no deadline; a wedged socket could hang the board forever")
	}
}

// A vim count prefix has no meaning for o (there is only ever one selected
// card to jump from); 3o must still produce exactly one jump, and must not
// leave the count armed for whatever key comes next.
func TestJumpWithCountPrefixStillJumpsOnce(t *testing.T) {
	src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
	m := jumpBoard(t, src)

	m = step(m, key("3"))
	if m.pendingCount != 3 {
		t.Fatalf("setup: count not accumulated: %d", m.pendingCount)
	}
	_, cmd := m.Update(key("o"))
	if !jumped(cmd) {
		t.Fatal("3o did not start a jump")
	}
	cmd()
	if len(src.focused) != 1 {
		t.Fatalf("3o focused %d panes, want exactly 1", len(src.focused))
	}
	m2, _ := update(m, key("o"))
	if m2.pendingCount != 0 {
		t.Fatalf("count leaked past o: %d", m2.pendingCount)
	}
}

// O (shift-o) is not a jump alias. Only lowercase o, and ga, trigger it.
func TestUppercaseODoesNotJump(t *testing.T) {
	src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
	m := jumpBoard(t, src)

	m, cmd := update(m, key("O"))
	if jumped(cmd) || quits(cmd) {
		t.Fatal("uppercase O started a jump")
	}
	if len(src.focused) != 0 {
		t.Fatalf("uppercase O focused %v", src.focused)
	}
	if m.status != "" {
		t.Fatalf("uppercase O produced a status: %q", m.status)
	}
}

// A board with no columns at all (not merely empty ones) must still answer
// "select a card first" rather than panicking on an out-of-range column index.
func TestJumpNoColumnsAtAll(t *testing.T) {
	src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
	m := jumpBoard(t, src)
	m.columns = nil

	m, cmd := update(m, key("o"))
	if m.status != "Select a card first" || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
	if jumped(cmd) || len(src.focused) != 0 {
		t.Fatal("jumped with no columns at all")
	}
}

// Pressing o before the very first board load (live status can go up
// independently and faster than the first tkt list) must not panic and must
// say there is nothing selected yet.
func TestJumpBeforeBoardLoads(t *testing.T) {
	src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
	m, _ := testModel(t)
	m = m.WithLive(src)
	m = goLive(t, m) // live comes up; the board itself has never loaded
	if m.loaded {
		t.Fatal("setup: board should not be loaded yet")
	}

	m, cmd := update(m, key("o"))
	if m.status != "Select a card first" || m.statusKind != "warn" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
	if jumped(cmd) || len(src.focused) != 0 {
		t.Fatal("jumped before the board ever loaded a card")
	}
}

// o and ga must be inert behind every modal kind that opens synchronously off
// a single keypress, not just the filter modal jump_test.go already covers.
func TestJumpInertBehindEveryModalKind(t *testing.T) {
	openers := map[string]string{
		"move (m)":    "m",
		"comment (c)": "c",
		"date (d)":    "d",
	}
	for name, opener := range openers {
		src := &fakeFocus{fakeLive: fakeLive{byKey: panes("TKT-1", herdr.StatusWorking, ref("wC:p1", herdr.StatusWorking, false))}}
		m := jumpBoard(t, src)
		m = step(m, key(opener))
		if m.modal == nil {
			t.Fatalf("%s: modal did not open", name)
		}
		for _, keys := range [][]string{{"o"}, {"g", "a"}} {
			nm := m
			var cmd tea.Cmd
			for _, k := range keys {
				nm, cmd = update(nm, key(k))
				if jumped(cmd) || quits(cmd) {
					t.Fatalf("%s: %v behind the modal produced a jump", name, keys)
				}
			}
			if nm.modal == nil {
				t.Fatalf("%s: %v closed the modal", name, keys)
			}
			if len(src.focused) != 0 {
				t.Fatalf("%s: %v focused %v from behind the modal", name, keys, src.focused)
			}
		}
	}
}

// applySelectKey walks m.columns in order, so a key that (abnormally) shows
// up on more than one card must land on the first column that has it.
func TestSelectKeyPresentInSeveralColumnsFirstWins(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithSelect("TKT-9")
	m, _ = update(m, boardMsg{
		roles: []model.RolePair{{Role: "todo", Lane: "To Do"}, {Role: "doing", Lane: "Doing"}},
		columns: []model.Column{
			{Lane: "To Do", Role: "todo", Cards: []model.Card{{Key: "TKT-9"}}},
			{Lane: "Doing", Role: "doing", Cards: []model.Card{{Key: "TKT-9"}}},
		},
	})
	if got := m.columns[m.focusCol].Role; got != "todo" {
		t.Fatalf("focused column = %q, want the first match todo", got)
	}
	if m.status != "Selected TKT-9" || m.statusKind != "" {
		t.Fatalf("status = %q (%s)", m.status, m.statusKind)
	}
}

// A completely empty board (no roles, no columns) must not panic on either an
// explicit or a derived select key, and must report exactly as it would for
// any other miss: loud for explicit, silent for derived.
func TestSelectKeyEmptyBoard(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithSelect("TKT-1")
	m, _ = update(m, boardMsg{})
	if m.status != "TKT-1 is not on the board" || m.statusKind != "warn" {
		t.Fatalf("explicit on empty board: status = %q (%s)", m.status, m.statusKind)
	}
	if _, ok := m.selectedCard(); ok {
		t.Fatal("selectedCard reported ok on a totally empty board")
	}

	m2, _ := testModel(t)
	m2 = m2.WithSelectFunc(func() string { return "TKT-1" })
	m2, _ = update(m2, selectCmd(t, m2)())
	m2, _ = update(m2, boardMsg{})
	if m2.status != "" {
		t.Fatalf("derived miss on empty board announced: %q", m2.status)
	}
}

// However --select and --select-from-cwd race, --select must win: here the
// derived key is applied to the model first (before the board even loads),
// and an explicit --select set from the start must still not be displaced.
func TestExplicitSelectBeatsDerivedArrivingBeforeLoad(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithSelect("TKT-1").WithSelectFunc(func() string { return "TKT-2" })
	cmd := selectCmd(t, m)

	m, applyCmd := update(m, cmd())
	if applyCmd != nil {
		t.Fatal("a derived key produced a command although --select is explicit")
	}
	if m.selectKey != "TKT-1" {
		t.Fatalf("explicit select key overwritten before load: %q", m.selectKey)
	}
	m = loadBoard(m)
	if card, ok := m.selectedCard(); !ok || card.Key != "TKT-1" {
		t.Fatalf("derived key (arriving first) overrode --select: selected %v ok=%v", card.Key, ok)
	}
}

// Consuming a *derived* select key must be just as permanent as consuming an
// explicit one: an auto-refresh afterwards must not re-select or re-announce
// it (jump_test.go's TestSelectKeyConsumedOnce only exercises the explicit
// --select path).
func TestSelectFuncConsumedOnceAcrossRefresh(t *testing.T) {
	m, _ := testModel(t)
	m = m.WithSelectFunc(func() string { return "TKT-2" })
	cmd := selectCmd(t, m)
	m = loadBoard(m)
	m, _ = update(m, cmd())
	if m.selectKey != "" {
		t.Fatalf("derived select key not consumed: %q", m.selectKey)
	}
	m = step(m, key("h")) // move away from the selection
	m.status, m.statusKind = "", ""
	m = loadBoard(m) // auto-refresh
	card, ok := m.selectedCard()
	if !ok || card.Key != "TKT-1" {
		t.Fatalf("auto-refresh re-applied the derived select: selected %v", card.Key)
	}
	if m.status != "" {
		t.Fatalf("auto-refresh re-announced the derived select: %q", m.status)
	}
}
