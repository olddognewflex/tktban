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

// Regression: a card's full outline must fit within the column's inner width.
// If it overflows, the column wraps the card and its top/bottom borders split
// across two lines (the bug that fragmented the board). Assert each card's top
// and bottom border render as a complete ╭…╮ / ╰…╯ run on a single line.
func TestCardOutlineFitsInColumn(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	th, _ := themeByName("textual-dark")
	m := Model{styles: newStyles(th)}
	cards := []model.Card{
		{Key: "TKB-1", Summary: "a long summary that will need to be truncated to fit", Priority: "High", Assignee: "raymond", LaneHuman: "10h 5m"},
		{Key: "TKB-2", Summary: "another card"},
	}
	col := m.renderColumn(model.Column{Lane: "To Do", Role: "todo", Cards: cards}, 0, 30, 22)

	var topRuns, botRuns int
	for ln := range strings.SplitSeq(col, "\n") {
		// A card border line contains its left+right corner on the same line.
		if strings.Contains(ln, "╭") && strings.Contains(ln, "╮") && strings.Count(ln, "╭") == 1 {
			topRuns++
		}
		if strings.Contains(ln, "╰") && strings.Contains(ln, "╯") && strings.Count(ln, "╰") == 1 {
			botRuns++
		}
	}
	// The column itself contributes one ╭…╮ and one ╰…╯; each of the 2 cards adds
	// one of each. Anything less means a card outline wrapped/fragmented.
	if topRuns < 3 || botRuns < 3 {
		t.Fatalf("card outlines fragmented in column (intact top runs=%d bottom=%d, want >=3 each):\n%s", topRuns, botRuns, col)
	}
}

// Regression: the board fills the full terminal width with no dead space on the
// right, for any width (the leftover from width/n is distributed across columns).
func TestBoardFillsWidth(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(termenv.TrueColor)

	th, _ := themeByName("textual-dark")
	var cols []model.Column
	for _, name := range []string{"Backlog", "To Do", "In Progress", "In Review", "Done", "Blocked"} {
		cols = append(cols, model.Column{Lane: name, Role: name})
	}
	for _, w := range []int{120, 160, 200, 203, 257} {
		m := Model{styles: newStyles(th), loaded: true, columns: cols, width: w}
		board := m.renderBoard(20)
		for ln := range strings.SplitSeq(board, "\n") {
			if got := lipgloss.Width(ln); got != w {
				t.Fatalf("width %d: board line is %d cols wide (dead space):\n%q", w, got, ln)
			}
		}
	}
}

// Regression: the column title bar must fit on a single line. It has a
// background, so if it's sized wider than the column's text area it wraps,
// pushing every card down by a row (the stray block under the title).
func TestColumnTitleDoesNotWrap(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(termenv.TrueColor)

	th, _ := themeByName("textual-dark")
	m := Model{styles: newStyles(th)}
	col := m.renderColumn(model.Column{Lane: "In Progress", Role: "wip", Cards: []model.Card{{Key: "TKB-1", Summary: "x"}}}, 0, 30, 12)

	lines := strings.Split(col, "\n")
	// line 0 = column top border, line 1 = title, line 2 = first card's top border.
	if len(lines) < 3 || !strings.Contains(lines[2], "╭") {
		t.Fatalf("title appears to wrap (first card not on line 3):\n%s", col)
	}
}
