package ui

import (
	"testing"

	"github.com/olddognewflex/tktban/internal/model"
)

// vimModel builds a board with one multi-card column ("todo", 5 cards) and a
// second column, so motions, counts and gg/G can be exercised.
func vimModel() Model {
	th, _ := themeByName("textual-dark")
	todo := []model.Card{
		{Key: "T-0"}, {Key: "T-1"}, {Key: "T-2"}, {Key: "T-3"}, {Key: "T-4"},
	}
	cols := []model.Column{
		{Lane: "To Do", Role: "todo", Cards: todo},
		{Lane: "Done", Role: "done", Cards: []model.Card{{Key: "D-0"}}},
	}
	return Model{
		styles:  newStyles(th),
		loaded:  true,
		columns: cols,
		sel:     map[string]int{},
		width:   120, height: 30,
	}
}

func selIdx(m Model) int { return m.sel["todo"] }

func TestVimMotionJK(t *testing.T) {
	m := vimModel()
	m = step(m, key("j"))
	if selIdx(m) != 1 {
		t.Fatalf("j: sel = %d, want 1", selIdx(m))
	}
	m = step(m, key("j"))
	m = step(m, key("k"))
	if selIdx(m) != 1 {
		t.Fatalf("jjk: sel = %d, want 1", selIdx(m))
	}
	// k clamps at the top.
	m = step(m, key("k"))
	m = step(m, key("k"))
	if selIdx(m) != 0 {
		t.Fatalf("k clamp: sel = %d, want 0", selIdx(m))
	}
}

func TestVimColumnHL(t *testing.T) {
	m := vimModel()
	m = step(m, key("l"))
	if m.focusCol != 1 {
		t.Fatalf("l: focusCol = %d, want 1", m.focusCol)
	}
	m = step(m, key("l")) // clamp at last column
	if m.focusCol != 1 {
		t.Fatalf("l clamp: focusCol = %d, want 1", m.focusCol)
	}
	m = step(m, key("h"))
	if m.focusCol != 0 {
		t.Fatalf("h: focusCol = %d, want 0", m.focusCol)
	}
}

func TestVimCountPrefix(t *testing.T) {
	m := vimModel()
	// A lone count does not move yet.
	m = step(m, key("3"))
	if selIdx(m) != 0 {
		t.Fatalf("count alone moved selection: %d", selIdx(m))
	}
	m = step(m, key("j")) // 3j
	if selIdx(m) != 3 {
		t.Fatalf("3j: sel = %d, want 3", selIdx(m))
	}
	// Count is consumed: the next j moves only one.
	m = step(m, key("j"))
	if selIdx(m) != 4 {
		t.Fatalf("count not reset: sel = %d, want 4", selIdx(m))
	}
	// Multi-digit count, clamped to the last card.
	m = vimModel()
	m = step(m, key("1"))
	m = step(m, key("2"))
	m = step(m, key("j")) // 12j on a 5-card column
	if selIdx(m) != 4 {
		t.Fatalf("12j clamp: sel = %d, want 4", selIdx(m))
	}
}

func TestVimGGAndG(t *testing.T) {
	m := vimModel()
	m = step(m, key("3"))
	m = step(m, key("j")) // at index 3
	m = step(m, key("G"))
	if selIdx(m) != 4 {
		t.Fatalf("G: sel = %d, want 4 (last)", selIdx(m))
	}
	m = step(m, key("g"))
	m = step(m, key("g")) // gg
	if selIdx(m) != 0 {
		t.Fatalf("gg: sel = %d, want 0 (first)", selIdx(m))
	}
	// A single g followed by a motion cancels the gg and applies the motion.
	m = step(m, key("g"))
	m = step(m, key("j"))
	if selIdx(m) != 1 {
		t.Fatalf("g then j: sel = %d, want 1", selIdx(m))
	}
}

func TestVimSlashOpensFilter(t *testing.T) {
	m := vimModel()
	m = step(m, key("/"))
	if _, ok := m.modal.(filterModal); !ok {
		t.Fatalf("/ should open the filter modal, got %T", m.modal)
	}
}

// A pending count is discarded (not leaked) when a non-motion key runs, including
// opening a modal, so it can't surprise the next motion.
func TestVimCountResetByNonMotion(t *testing.T) {
	m := vimModel()
	m = step(m, key("3"))
	if m.pendingCount != 3 {
		t.Fatalf("count not accumulated: %d", m.pendingCount)
	}
	m = step(m, key("/")) // opening a modal is a non-motion key
	if m.pendingCount != 0 {
		t.Fatalf("count leaked past modal open: %d", m.pendingCount)
	}
	if _, ok := m.modal.(filterModal); !ok {
		t.Fatalf("expected filter modal, got %T", m.modal)
	}
}

// G discards any pending count (kanban columns don't take an NG line jump), and
// the count does not carry into the following motion.
func TestVimGDiscardsCount(t *testing.T) {
	m := vimModel()
	m = step(m, key("3"))
	m = step(m, key("G")) // jump to last regardless of the 3
	if selIdx(m) != 4 {
		t.Fatalf("3G: sel = %d, want 4 (last)", selIdx(m))
	}
	m = step(m, key("k")) // should move exactly one, count was reset
	if selIdx(m) != 3 {
		t.Fatalf("count leaked past G: sel = %d, want 3", selIdx(m))
	}
}

// j/k drive the create modal's type picker (a select list).
func TestVimCreateTypePickerJK(t *testing.T) {
	th, _ := themeByName("textual-dark")
	cm := newCreateModal([]string{"Story", "Bug", "Task"}, nil, th.surface)
	// focus 0 is the type picker by default; typeIdx starts at -1 (none chosen).
	nm, _ := cm.Update(key("j"))
	cm = nm.(createModal)
	if cm.typeIdx != 0 {
		t.Fatalf("first j on type picker: typeIdx = %d, want 0", cm.typeIdx)
	}
	nm, _ = cm.Update(key("j"))
	cm = nm.(createModal)
	if cm.typeIdx != 1 {
		t.Fatalf("second j on type picker: typeIdx = %d, want 1", cm.typeIdx)
	}
	nm, _ = cm.Update(key("k"))
	cm = nm.(createModal)
	if cm.typeIdx != 0 {
		t.Fatalf("k on type picker: typeIdx = %d, want 0", cm.typeIdx)
	}
}
