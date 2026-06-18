package ui

import (
	"testing"

	"github.com/olddognewflex/tktban/internal/model"
	"github.com/olddognewflex/tktban/internal/ticket"
)

var prios = []string{"Highest", "High", "Medium", "Low", "Lowest"}

// selectField pre-selects a configured value and cycles through options plus a
// blank slot.
func TestSelectFieldBasics(t *testing.T) {
	s := newSelectField(prios, "High")
	if s.value() != "High" {
		t.Fatalf("pre-select: value = %q, want High", s.value())
	}
	// Blank when nothing is selected.
	blank := newSelectField(prios, "")
	if blank.value() != "" || blank.display() != "(default)" {
		t.Fatalf("blank: value=%q display=%q", blank.value(), blank.display())
	}
	// next from blank -> first option; prev from blank -> last.
	b2 := newSelectField(prios, "")
	b2.next()
	if b2.value() != "Highest" {
		t.Fatalf("next from blank = %q, want Highest", b2.value())
	}
	b3 := newSelectField(prios, "")
	b3.prev()
	if b3.value() != "Lowest" {
		t.Fatalf("prev from blank = %q, want Lowest", b3.value())
	}
	// Cycling forward off the end returns to blank.
	s2 := newSelectField(prios, "Lowest")
	s2.next()
	if s2.value() != "" {
		t.Fatalf("next off end = %q, want blank", s2.value())
	}
}

// Criterion 5: a stored priority not in the configured list still displays and
// is selectable.
func TestSelectFieldUnknownStoredValue(t *testing.T) {
	s := newSelectField([]string{"High", "Low"}, "Frobnicate")
	if s.value() != "Frobnicate" {
		t.Fatalf("unknown value not preserved: %q", s.value())
	}
	if s.display() != "Frobnicate" {
		t.Fatalf("unknown value not displayed: %q", s.display())
	}
	s.prev() // still navigable without panic
	if s.value() != "Low" {
		t.Fatalf("prev from unknown = %q, want Low", s.value())
	}
}

// Criterion 1+2+3: in the create modal priority is a picker fed from config, the
// cycle keys change it, and an untouched picker submits a blank priority.
func TestCreateModalPriorityPicker(t *testing.T) {
	th, _ := themeByName("textual-dark")
	cm := newCreateModal([]string{"Story"}, prios, th.surface, 120, 40)
	cm.typeIdx = 0 // choose a type so validate() passes
	cm.summary.SetValue("s")
	cm.focus = 4 // priority picker

	// Blank by default -> no --priority.
	if p, err := cm.validate(); err != "" || p.priority != "" {
		t.Fatalf("default priority should be blank: %q err=%q", p.priority, err)
	}
	// Cycle to the first configured option.
	nm, _ := cm.Update(key("l"))
	cm = nm.(createModal)
	if cm.prio.value() != "Highest" {
		t.Fatalf("picker did not advance: %q", cm.prio.value())
	}
	if p, _ := cm.validate(); p.priority != "Highest" {
		t.Fatalf("validate priority = %q, want Highest", p.priority)
	}
	// Typing a digit must not corrupt the picker (it's not free text).
	nm, _ = cm.Update(key("3"))
	cm = nm.(createModal)
	if cm.prio.value() != "Highest" {
		t.Fatalf("digit changed picker: %q", cm.prio.value())
	}
}

// When the priorities lookup fails (nil), the picker degrades to a blank,
// inert, non-crashing field — the modal still opens and submits a blank
// priority.
func TestPriorityPickerNilOptions(t *testing.T) {
	s := newSelectField(nil, "")
	s.next()
	s.prev()
	if s.value() != "" || s.display() != "(default)" {
		t.Fatalf("nil options not inert: value=%q display=%q", s.value(), s.display())
	}
	th, _ := themeByName("textual-dark")
	cm := newCreateModal([]string{"Story"}, nil, th.surface, 120, 40)
	cm.typeIdx = 0
	cm.summary.SetValue("s")
	if p, err := cm.validate(); err != "" || p.priority != "" {
		t.Fatalf("nil priorities should validate blank: %q err=%q", p.priority, err)
	}
}

// tab reaches the priority picker, and at that focus no text field is focused
// (the picker has no Focus state) — guards the focus-index wiring.
func TestTabReachesPriorityPicker(t *testing.T) {
	th, _ := themeByName("textual-dark")
	cm := newCreateModal([]string{"Story"}, prios, th.surface, 120, 40)
	for i := 0; i < 4; i++ { // 0(type)->1(summary)->2(desc)->3(accept)->4(priority)
		nm, _ := cm.Update(key("tab"))
		cm = nm.(createModal)
	}
	if cm.focus != 4 {
		t.Fatalf("tab did not reach priority picker: focus=%d", cm.focus)
	}
	if cm.summary.Focused() {
		t.Fatal("a text field is still focused at the priority picker")
	}
	nm, _ := cm.Update(key("l")) // picker is live here
	cm = nm.(createModal)
	if cm.prio.value() != "Highest" {
		t.Fatalf("picker not active after tabbing to it: %q", cm.prio.value())
	}
}

// Criterion 4: the editor pre-selects the ticket's priority, and saving an
// unchanged priority produces no diff.
func TestEditModalPriorityPreselectAndNoDiff(t *testing.T) {
	th, _ := themeByName("textual-dark")
	tk := model.Ticket{"key": "TKB-1", "summary": "s", "priority": "High"}
	em := newEditModal(tk, prios, th.surface, 120, 40)
	if em.prio.value() != "High" {
		t.Fatalf("priority not pre-selected: %q", em.prio.value())
	}
	opts, changed := ticket.ComputeEdit(em.orig, em.current())
	if changed || opts.Priority != nil {
		t.Fatalf("unchanged priority produced a diff: changed=%v opts=%+v", changed, opts)
	}
	// Changing it produces a diff.
	em.focus = 2
	nm, _ := em.Update(key("l")) // High -> Medium
	em = nm.(editModal)
	if em.prio.value() != "Medium" {
		t.Fatalf("picker did not advance: %q", em.prio.value())
	}
	opts, changed = ticket.ComputeEdit(em.orig, em.current())
	if !changed || opts.Priority == nil || *opts.Priority != "Medium" {
		t.Fatalf("changed priority not diffed: changed=%v opts=%+v", changed, opts)
	}
}
