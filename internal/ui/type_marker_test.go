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
		"Story": "S", "Bug": "B", "Sub-task": "U", "Task": "T", "Chore": "C",
		"Spike": "K", "Epic": "E", "sub-task": "U", "SUBTASK": "U", "sub task": "U",
		"Sub_Task": "U", "bug": "B",
	}
	for in, want := range cases {
		if got, _ := typeMarker(in); got != want {
			t.Errorf("typeMarker(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTypeMarkerUnknownFallback(t *testing.T) {
	for _, in := range []string{"", "Improvement", "🐛", "  "} {
		if got, _ := typeMarker(in); got != "?" {
			t.Errorf("typeMarker(%q) = %q, want ?", in, got)
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
	return strings.TrimSpace(strings.Trim(ln, "│"))
}

func TestCardShowsTypeMarkerWithoutColour(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(prev)
	m := markerModel()
	seen := map[string]string{}
	for typ, glyph := range map[string]string{"Bug": "B", "Story": "S", "Task": "T", "Spike": "K"} {
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

func TestUnknownTypeCardRendersCleanly(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(prev)
	m := markerModel()
	for _, typ := range []string{"Improvement", ""} {
		for _, w := range []int{24, 14} {
			out := m.renderCard(model.Card{Key: "TKB-1", Summary: "a long summary here", Type: typ, Priority: "High", BlockerCount: 2}, w, false)
			lines := strings.Split(out, "\n")
			if !strings.HasPrefix(lines[0], "╭") || !strings.HasPrefix(lines[len(lines)-1], "╰") {
				t.Errorf("type %q width %d: outline broken:\n%s", typ, w, out)
			}
			for _, ln := range lines {
				if got := lipgloss.Width(ln); got != w-2 {
					t.Errorf("type %q width %d: line width %d, want %d: %q", typ, w, got, w-2, ln)
				}
			}
			if !strings.Contains(out, "? ") {
				t.Errorf("type %q width %d: missing ? marker:\n%s", typ, w, out)
			}
		}
	}
}

func TestBugMarkerUsesErrorColour(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)
	m := markerModel()
	out := m.renderTypeMarker(model.Card{Type: "Bug", TypeClass: "deliverable"})
	if !strings.Contains(out, fgSeq(m.styles.t.errc)) {
		t.Fatalf("bug marker lacks errc colour: %q", out)
	}
	out = m.renderTypeMarker(model.Card{Type: "Task"})
	if strings.Contains(out, fgSeq(m.styles.t.errc)) {
		t.Fatalf("task marker should not be red: %q", out)
	}
}

func TestTypeMarkerColourByClass(t *testing.T) {
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
		out := m.renderTypeMarker(tc.card)
		if !strings.Contains(out, seqs[tc.want]) {
			t.Errorf("%s: want %s colour %q in %q", tc.name, tc.want, seqs[tc.want], out)
		}
		for name, sq := range seqs {
			if name != tc.want && strings.Contains(out, sq) {
				t.Errorf("%s: want %s but found %s colour %q in %q", tc.name, tc.want, name, sq, out)
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
		if want := tm.Glyph + "  " + tm.Type; !strings.Contains(v, want) {
			t.Errorf("legend missing pairing %q:\n%s", want, v)
		}
	}
	if want := "?  other / none"; !strings.Contains(v, want) {
		t.Errorf("legend missing %q:\n%s", want, v)
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
