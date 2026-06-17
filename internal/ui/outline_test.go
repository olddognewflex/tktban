package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/olddognewflex/tktban/internal/model"
)

// fgSeq returns the bare SGR parameters lipgloss emits for a foreground colour
// (e.g. "38;2;46;196;182"), so tests can assert a border carries that colour
// without hardcoding termenv's exact RGB rounding.
func fgSeq(c lipgloss.Color) string {
	s := lipgloss.NewStyle().Foreground(c).Render("x")
	i, j := strings.Index(s, "["), strings.Index(s, "m")
	if i < 0 || j < 0 || j < i {
		return ""
	}
	return s[i+1 : j]
}

// TKB-17: cards have a faint full outline that brightens when selected. A
// meta-less card isolates the border colour (accent is otherwise only used by
// the meta line), so an unselected card must not carry the accent sequence
// while a selected one must.
func TestCardOutlineBrightensWhenSelected(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	th, _ := themeByName("textual-dark")
	m := Model{styles: newStyles(th)}
	card := model.Card{Key: "TKB-1", Summary: "outline me"} // no priority/meta/blockers

	unselected := m.renderCard(card, 24, false)
	selected := m.renderCard(card, 24, true)

	// Full outline present (rounded box corners), both states.
	for name, out := range map[string]string{"unselected": unselected, "selected": selected} {
		if !strings.Contains(out, "╭") || !strings.Contains(out, "╰") {
			t.Fatalf("%s card lacks a full rounded outline:\n%q", name, out)
		}
	}

	accent := fgSeq(th.accent)
	muted := fgSeq(th.muted)
	if strings.Contains(unselected, accent) {
		t.Fatalf("unselected card border should be faint (muted), not accent %q:\n%q", accent, unselected)
	}
	if !strings.Contains(selected, accent) {
		t.Fatalf("selected card border should brighten to accent %q:\n%q", accent, selected)
	}
	if !strings.Contains(unselected, muted) {
		t.Fatalf("unselected card border should be muted %q:\n%q", muted, unselected)
	}
}
