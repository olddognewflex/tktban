package ui

import (
	"strings"

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
	{"Story", "S", "full_sdlc"},
	{"Bug", "B", "full_sdlc"},
	{"Sub-task", "U", "full_sdlc"},
	{"Task", "T", "deliverable"},
	{"Chore", "C", "deliverable"},
	{"Spike", "K", "deliverable"},
	{"Epic", "E", "container"},
}

const unknownTypeGlyph = "?"

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

// renderTypeMarker draws the card's one-letter type tag. Bug is always red;
// otherwise the card's own type_class wins over the table's.
func (m Model) renderTypeMarker(c model.Card) string {
	glyph, class := typeMarker(c.Type)
	if c.TypeClass != "" {
		class = c.TypeClass
	}
	st := m.styles.typeUnknown
	switch {
	case glyph == "B":
		st = m.styles.typeBug
	case class == "full_sdlc":
		st = m.styles.typeSDLC
	case class == "deliverable" || class == "container":
		st = m.styles.typeDeliverable
	}
	return st.Render(glyph)
}
