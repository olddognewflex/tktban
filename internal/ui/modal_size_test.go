package ui

import (
	"testing"

	"github.com/olddognewflex/tktban/internal/model"
)

// TKB-18: the create/edit forms scale toward the viewer's ~80% footprint, and
// the field width is bounded on tiny and huge terminals.
func TestModalFieldWidthScales(t *testing.T) {
	if got := modalFieldWidth(0); got != 52 {
		t.Fatalf("unsized fallback = %d, want 52", got)
	}
	if got := modalFieldWidth(120); got != 96 { // 80% of 120
		t.Fatalf("modalFieldWidth(120) = %d, want 96", got)
	}
	if modalFieldWidth(200) <= modalFieldWidth(120) {
		t.Fatal("field width should grow with the screen")
	}
	if got := modalFieldWidth(20); got != 40 { // clamped up off a tiny screen
		t.Fatalf("tiny screen = %d, want 40 floor", got)
	}
	if got := modalFieldWidth(44); got > 44 { // never wider than the screen
		t.Fatalf("field width %d overflows screen 44", got)
	}
}

// The create and edit forms size their fields and text areas from the screen, so
// at a large terminal they are much bigger than the legacy fixed 50/52.
func TestModalsScaleWithScreen(t *testing.T) {
	th, _ := themeByName("textual-dark")
	big, small := 160, 80

	cmBig := newCreateModal([]string{"Story"}, prios, th.surface, big, 50)
	cmSmall := newCreateModal([]string{"Story"}, prios, th.surface, small, 50)
	if cmBig.summary.Width <= 50 {
		t.Fatalf("create summary not enlarged: %d", cmBig.summary.Width)
	}
	if cmBig.summary.Width <= cmSmall.summary.Width {
		t.Fatal("create field width should grow with the screen")
	}
	if cmBig.desc.Height() <= 4 || cmBig.desc.Width() <= 52 {
		t.Fatalf("create description area not enlarged: %dx%d", cmBig.desc.Width(), cmBig.desc.Height())
	}

	emBig := newEditModal(model.Ticket{"key": "X", "summary": "s"}, prios, th.surface, big, 50)
	if emBig.summary.Width <= 50 || emBig.desc.Height() <= 4 {
		t.Fatalf("edit form not enlarged: field=%d descH=%d", emBig.summary.Width, emBig.desc.Height())
	}
}
