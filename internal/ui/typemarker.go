package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/olddognewflex/tktban/internal/model"
)

// typeMarkerRow is one row of the card type legend. Class is the tkt type_class
// that picks the colour when the card does not carry its own.
type typeMarkerRow struct {
	Type  string
	Glyph string
	Class string
}

// typeMarkers is the single source for the card tags and the legend dialog.
var typeMarkers = []typeMarkerRow{
	{"Story", "📖", "full_sdlc"},
	{"Bug", bugGlyph, "full_sdlc"},
	{"Sub-task", "🔗", "full_sdlc"},
	{"Task", "✅", "deliverable"},
	{"Chore", "🔧", "deliverable"},
	{"Spike", "🔬", "deliverable"},
	{"Epic", "📦", "container"},
}

// The glyphs are all Unicode 6.0 single-codepoint, Emoji_Presentation=Yes
// emoji, so each is two cells wide with no variation selector (U+FE0F) to
// disagree about width between terminals. Keep new entries that way.
const bugGlyph = "🐛"

const unknownTypeGlyph = "❔"

// normType lowercases and strips spaces, '-' and '_' so "Sub-task",
// "Subtask" and "sub task" all match.
func normType(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '_':
			return -1
		}
		return r
	}, strings.ToLower(s))
}

// typeMarker returns the glyph and colour class for an issue type. Unknown or
// empty types give unknownTypeGlyph and an empty class.
func typeMarker(typ string) (glyph, class string) {
	n := normType(typ)
	if n != "" {
		for _, tm := range typeMarkers {
			if normType(tm.Type) == n {
				return tm.Glyph, tm.Class
			}
		}
	}
	return unknownTypeGlyph, ""
}

// renderTypeMarker draws the card's emoji type tag. Emoji ignore the
// foreground colour, so it is rendered plain.
func (m Model) renderTypeMarker(c model.Card) string {
	glyph, _ := typeMarker(c.Type)
	return glyph
}

// typeColor is the colour for an issue type: the border of its cards and the
// swatch in the legend. Bug is always errc; otherwise typeClass (the card's own
// type_class) wins over the table's. Anything else is muted.
func typeColor(t theme, typ, typeClass string) lipgloss.TerminalColor {
	glyph, class := typeMarker(typ)
	if typeClass != "" {
		class = typeClass
	}
	switch {
	case glyph == bugGlyph:
		return t.errc
	case class == "full_sdlc":
		return t.primary
	case class == "deliverable" || class == "container":
		return t.accent
	}
	return t.muted
}

// typeBorderColor is the card's border colour; see typeColor.
func (m Model) typeBorderColor(c model.Card) lipgloss.TerminalColor {
	return typeColor(m.styles.t, c.Type, c.TypeClass)
}
