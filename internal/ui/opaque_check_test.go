package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/olddognewflex/tktban/internal/model"
)

// surfaceBGSeq is the truecolor background escape for textual-dark's surface
// (#1E1E2E). A field row carrying this sequence is opaque; one without it shows
// the terminal background through — the transparency bug from TKB-14.
const surfaceBGSeq = "48;2;30;30;46"

// fieldRowsAreOpaque asserts that every input-field row in a rendered dialog
// carries the surface background, i.e. the box has no transparent holes.
func fieldRowsAreOpaque(t *testing.T, out string, labels ...string) {
	t.Helper()
	if !strings.Contains(out, surfaceBGSeq) {
		t.Fatalf("dialog output has no surface background %q", surfaceBGSeq)
	}
	for ln := range strings.SplitSeq(out, "\n") {
		for _, label := range labels {
			if strings.Contains(ln, label) && !strings.Contains(ln, surfaceBGSeq) {
				t.Fatalf("field row %q lacks surface background (transparent)", label)
			}
		}
	}
}

func TestCreateModalIsOpaque(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	th, _ := themeByName("textual-dark")
	cm := newCreateModal([]string{"Story", "Bug"}, nil, th.surface)
	out := cm.View(newStyles(th), 80, 30)
	fieldRowsAreOpaque(t, out, "summary", "priority", "labels")
}

func TestEditModalIsOpaque(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	th, _ := themeByName("textual-dark")
	em := newEditModal(model.Ticket{"key": "TKB-1", "summary": "x"}, nil, th.surface)
	out := em.View(newStyles(th), 80, 30)
	fieldRowsAreOpaque(t, out, "summary", "priority", "labels")
}
