package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/olddognewflex/tktban/internal/model"
)

func TestTypeMarkerKnownTypes(t *testing.T) {
	cases := map[string]string{
		"Story": "📖", "Bug": "🐛", "Sub-task": "🔗", "Task": "✅", "Chore": "🔧",
		"Spike": "🔬", "Epic": "📦", "sub-task": "🔗", "SUBTASK": "🔗", "sub task": "🔗",
		"Sub_Task": "🔗", "bug": "🐛",
	}
	for in, want := range cases {
		if got, _ := typeMarker(in); got != want {
			t.Errorf("typeMarker(%q) = %q, want %q", in, got, want)
		}
	}
	for _, tm := range typeMarkers {
		if strings.Contains(tm.Glyph, "\uFE0F") {
			t.Errorf("%s glyph carries a variation selector", tm.Type)
		}
		if w := lipgloss.Width(tm.Glyph); w != 2 {
			t.Errorf("%s glyph width = %d, want 2", tm.Type, w)
		}
	}
}

func TestTypeMarkerUnknownFallback(t *testing.T) {
	for _, in := range []string{"", "Improvement", "🐛", "  "} {
		if got, _ := typeMarker(in); got != "❔" {
			t.Errorf("typeMarker(%q) = %q, want ❔", in, got)
		}
	}
}

func markerModel() Model {
	th, _ := themeByName("textual-dark")
	return Model{styles: newStyles(th)}
}

// headLine returns the plain first text line of a rendered card, minus the
// border and padding.
func headLine(out string) string {
	ln := strings.Split(ansi.Strip(out), "\n")[1]
	return strings.TrimSpace(strings.Trim(ln, "│┃"))
}

func TestCardShowsTypeMarkerWithoutColour(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(prev)
	m := markerModel()
	seen := map[string]string{}
	for typ, glyph := range map[string]string{"Bug": "🐛", "Story": "📖", "Task": "✅", "Spike": "🔬"} {
		out := m.renderCard(model.Card{Key: "TKB-1", Summary: "x", Type: typ}, 24, false)
		if h := headLine(out); !strings.HasPrefix(h, glyph+" ") {
			t.Errorf("%s head = %q, want prefix %q", typ, h, glyph+" ")
		}
		for other, o := range seen {
			if o == out {
				t.Errorf("%s and %s render identically", typ, other)
			}
		}
		seen[typ] = out
	}
}

// Emoji are two cells wide, so the key risk is a card whose lines are not all
// width-2 or whose outline breaks. Checked for every type, both selection
// states, at a roomy and a cramped width.
func TestCardWidthAndOutlineForEveryType(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(prev)
	m := markerModel()
	types := []string{"", "Improvement"}
	for _, tm := range typeMarkers {
		types = append(types, tm.Type)
	}
	for _, typ := range types {
		for _, w := range []int{24, 14} {
			for _, sel := range []bool{false, true} {
				out := m.renderCard(model.Card{Key: "TKB-1", Summary: "a long summary here", Type: typ, Priority: "High", BlockerCount: 2}, w, sel)
				lines := strings.Split(out, "\n")
				top, bot := "╭", "╰"
				if sel {
					top, bot = "┏", "┗"
				}
				if !strings.HasPrefix(lines[0], top) || !strings.HasPrefix(lines[len(lines)-1], bot) {
					t.Errorf("type %q width %d selected=%v: outline broken:\n%s", typ, w, sel, out)
				}
				for _, ln := range lines {
					if got := lipgloss.Width(ln); got != w-2 {
						t.Errorf("type %q width %d selected=%v: line width %d, want %d: %q", typ, w, sel, got, w-2, ln)
					}
				}
				g, _ := typeMarker(typ)
				if !strings.Contains(out, g) {
					t.Errorf("type %q width %d: missing marker %q:\n%s", typ, w, g, out)
				}
			}
		}
	}
}

func TestSelectedCardIsThickUnselectedRounded(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(prev)
	m := markerModel()
	c := model.Card{Key: "TKB-1", Summary: "x", Type: "Task"}
	un := ansi.Strip(m.renderCard(c, 24, false))
	se := ansi.Strip(m.renderCard(c, 24, true))
	for _, r := range []string{"╭", "╮", "╰", "╯"} {
		if !strings.Contains(un, r) || strings.Contains(se, r) {
			t.Errorf("rounded corner %s: unselected must have it, selected must not", r)
		}
	}
	for _, r := range []string{"┏", "┓", "┗", "┛", "┃", "━"} {
		if !strings.Contains(se, r) || strings.Contains(un, r) {
			t.Errorf("thick rune %s: selected must have it, unselected must not", r)
		}
	}
}

func TestCardBorderColourByType(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)
	m := markerModel()
	th := m.styles.t
	seqs := map[string]string{
		"primary": fgSeq(th.primary), "accent": fgSeq(th.accent),
		"muted": fgSeq(th.muted), "errc": fgSeq(th.errc),
	}
	// A shared colour would make "expected present, others absent" meaningless.
	seen := map[string]string{}
	for name, sq := range seqs {
		if sq == "" {
			t.Fatalf("slot %s produced no colour sequence", name)
		}
		if o, dup := seen[sq]; dup {
			t.Fatalf("theme slots %s and %s share colour %q; pick a theme where they differ", name, o, sq)
		}
		seen[sq] = name
	}
	cases := []struct {
		name string
		card model.Card
		want string
	}{
		{"Story table class is full_sdlc", model.Card{Type: "Story"}, "primary"},
		{"Sub-task table class is full_sdlc", model.Card{Type: "Sub-task"}, "primary"},
		{"Task table class is deliverable", model.Card{Type: "Task"}, "accent"},
		{"Epic table class is container", model.Card{Type: "Epic"}, "accent"},
		{"unknown type no class", model.Card{Type: "Improvement"}, "muted"},
		{"empty type no class", model.Card{}, "muted"},
		{"card full_sdlc overrides Task", model.Card{Type: "Task", TypeClass: "full_sdlc"}, "primary"},
		{"card deliverable on unknown type", model.Card{Type: "Improvement", TypeClass: "deliverable"}, "accent"},
		{"card container on unknown type", model.Card{Type: "Improvement", TypeClass: "container"}, "accent"},
		{"card unknown overrides Story", model.Card{Type: "Story", TypeClass: "unknown"}, "muted"},
		{"Bug stays red despite deliverable", model.Card{Type: "Bug", TypeClass: "deliverable"}, "errc"},
		{"Bug stays red with no class", model.Card{Type: "Bug"}, "errc"},
	}
	for _, tc := range cases {
		for _, sel := range []bool{false, true} {
			tc.card.Key, tc.card.Summary = "TKB-1", "x"
			lines := strings.Split(m.renderCard(tc.card, 24, sel), "\n")
			// Only the top and bottom border lines: card text uses other colours.
			for _, ln := range []string{lines[0], lines[len(lines)-1]} {
				if !strings.Contains(ln, seqs[tc.want]) {
					t.Errorf("%s (selected=%v): want %s colour %q in border %q", tc.name, sel, tc.want, seqs[tc.want], ln)
				}
				for name, sq := range seqs {
					if name != tc.want && strings.Contains(ln, sq) {
						t.Errorf("%s (selected=%v): want %s but border has %s colour %q", tc.name, sel, tc.want, name, sq)
					}
				}
			}
		}
	}
}

func TestTypeLegendModalContent(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(prev)
	m := markerModel()
	v := ansi.Strip(typeLegendModal{}.View(m.styles, 80, 30))
	if !strings.Contains(v, "Card type markers") {
		t.Errorf("legend missing title:\n%s", v)
	}
	if len(typeMarkers) == 0 {
		t.Fatal("typeMarkers empty")
	}
	for _, tm := range typeMarkers {
		if want := "━━ " + tm.Glyph + "  " + tm.Type; !strings.Contains(v, want) {
			t.Errorf("legend missing row %q:\n%s", want, v)
		}
	}
	for _, want := range []string{"━━ ❔  other / none", "Thick border = selected card", "any key closes"} {
		if !strings.Contains(v, want) {
			t.Errorf("legend missing %q:\n%s", want, v)
		}
	}
}

func TestTypeLegendFitsShortPanes(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(prev)
	m := markerModel()
	for _, w := range []int{80, 40} {
		for _, h := range []int{18, 14, 10, 8, 6} {
			v := typeLegendModal{}.View(m.styles, w, h)
			if got := lipgloss.Height(v); got > h {
				t.Errorf("%dx%d: height %d exceeds %d:\n%s", w, h, got, h, ansi.Strip(v))
			}
			for _, ln := range strings.Split(v, "\n") {
				if lw := lipgloss.Width(ln); lw > w {
					t.Errorf("%dx%d: line width %d exceeds %d: %q", w, h, lw, w, ansi.Strip(ln))
				}
			}
			plain := ansi.Strip(v)
			missing := 0
			for _, tm := range typeMarkers {
				if !strings.Contains(plain, tm.Type) {
					missing++
				}
			}
			if !strings.Contains(plain, "other / none") {
				missing++
			}
			if (h >= 18 || (w == 80 && h >= 10)) && missing > 0 {
				t.Errorf("%dx%d: %d rows clipped:\n%s", w, h, missing, plain)
			}
			if missing > 0 {
				t.Logf("%dx%d clips %d rows", w, h, missing)
			}
		}
	}
}

func TestTypeLegendSwatchColours(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)
	m := markerModel()
	th := m.styles.t
	v := typeLegendModal{}.View(m.styles, 80, 30)
	want := map[string]lipgloss.Color{
		"Bug": th.errc, "Story": th.primary, "Task": th.accent, "other / none": th.muted,
	}
	for _, ln := range strings.Split(v, "\n") {
		plain := ansi.Strip(ln)
		for name, c := range want {
			if strings.HasSuffix(strings.TrimSpace(strings.Trim(strings.TrimSpace(plain), "┃")), "  "+name) {
				if !strings.Contains(ln, fgSeq(c)) {
					t.Errorf("%s row lacks its colour %q: %q", name, fgSeq(c), ln)
				}
				delete(want, name)
			}
		}
	}
	for name := range want {
		t.Errorf("no legend row found for %s", name)
	}
}

func TestTypeLegendAppShortTerminal(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(prev)
	m, _ := testModel(t)
	m = loadBoard(m)
	m = step(m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	v := m.View()
	// At 80 columns the key hints wrap onto several rows; View must budget
	// for them so the whole screen still fits the terminal.
	if got := lipgloss.Height(v); got > 20 {
		t.Errorf("view height %d exceeds 20:\n%s", got, ansi.Strip(v))
	}
	plain := ansi.Strip(v)
	for _, tm := range typeMarkers {
		if !strings.Contains(plain, tm.Type) {
			t.Errorf("type %s missing at 80x20:\n%s", tm.Type, plain)
		}
	}
}

func TestTypeLegendModal(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if _, ok := m.modal.(typeLegendModal); !ok {
		t.Fatalf("? did not open legend, modal = %T", m.modal)
	}
	if v := m.View(); !strings.Contains(v, "Card type markers") {
		t.Errorf("open legend not rendered in view")
	}
	nm, cmd := m.Update(key("x"))
	m = nm.(Model)
	if cmd == nil {
		t.Fatal("key gave no close cmd")
	}
	m = step(m, cmd())
	if m.modal != nil {
		t.Fatalf("modal still open: %T", m.modal)
	}
}
