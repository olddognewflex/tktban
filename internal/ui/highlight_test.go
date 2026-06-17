package ui

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/olddognewflex/tktban/internal/model"
)

func withTrueColor(t *testing.T) {
	t.Helper()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })
}

// blurred preview must occupy the exact same block as the editable textarea, or
// tabbing between fields would shift the modal layout (acceptance: no regression
// in layout). Compare height and width against the plain textarea.View().
func TestHighlightAreaMatchesTextareaFootprint(t *testing.T) {
	withTrueColor(t)
	th, _ := themeByName("textual-dark")
	cm := newCreateModal([]string{"Story"}, th.surface)
	ta := cm.desc
	ta.SetValue("# Heading\n- item one\n`code` and **bold**")

	plain := ta.View() // editable footprint
	preview, ok := highlightArea(ta.Value(), ta.Width(), ta.Height(), th)
	if !ok {
		t.Fatal("highlightArea returned ok=false for valid markdown")
	}

	if got, want := lipgloss.Height(preview), lipgloss.Height(plain); got != want {
		t.Fatalf("preview height %d != textarea height %d", got, want)
	}
	if got, want := lipgloss.Width(preview), lipgloss.Width(plain); got != want {
		t.Fatalf("preview width %d != textarea width %d", got, want)
	}
}

// A blurred editor with markdown content must actually colour the text. The
// heading carries the heading token style (primary, bold), distinct from plain
// body text.
func TestHighlightAreaColorsMarkdown(t *testing.T) {
	withTrueColor(t)
	th, _ := themeByName("textual-dark")
	out, ok := highlightArea("# Heading", 40, 3, th)
	if !ok {
		t.Fatal("highlightArea returned ok=false")
	}
	headingSeg := tokenStyle(chroma.GenericHeading, th).Render("# Heading")
	if !strings.Contains(out, headingSeg) {
		t.Fatalf("heading not rendered with heading style:\n got %q\nwant substring %q", out, headingSeg)
	}
	plainSeg := tokenStyle(chroma.Text, th).Render("# Heading")
	if headingSeg == plainSeg {
		t.Fatal("heading style is indistinguishable from plain text")
	}
}

// Highlighting is display-only: rendering the preview must never alter the
// editor's value, so the submitted body stays byte-identical (acceptance:
// round-trips byte-identical).
func TestAreaViewLeavesValueByteIdentical(t *testing.T) {
	withTrueColor(t)
	th, _ := themeByName("textual-dark")
	st := newStyles(th)
	cm := newCreateModal([]string{"Story"}, th.surface)
	const body = "# Title\n\n- a\n- b\n\n`x` **y** _z_"
	cm.desc.SetValue(body)
	cm.desc.Blur()

	_ = areaView(cm.desc, st) // render the highlighted preview
	if got := cm.desc.Value(); got != body {
		t.Fatalf("value mutated by preview render:\n got %q\nwant %q", got, body)
	}
}

// Focused editor stays the plain editable textarea (no highlight substitution),
// so editing UX is unchanged.
func TestAreaViewFocusedIsPlain(t *testing.T) {
	withTrueColor(t)
	th, _ := themeByName("textual-dark")
	st := newStyles(th)
	cm := newCreateModal([]string{"Story"}, th.surface)
	cm.desc.SetValue("# Heading")
	cm.desc.Focus()
	if got, want := areaView(cm.desc, st), cm.desc.View(); got != want {
		t.Fatalf("focused areaView differs from plain textarea view")
	}
}

// Empty/blurred and degenerate sizes fall back to the plain view without panic
// (acceptance: graceful fallback).
func TestAreaViewFallbacks(t *testing.T) {
	withTrueColor(t)
	th, _ := themeByName("textual-dark")
	st := newStyles(th)
	cm := newCreateModal([]string{"Story"}, th.surface)
	cm.desc.Blur() // empty + blurred
	if got, want := areaView(cm.desc, st), cm.desc.View(); got != want {
		t.Fatal("empty blurred editor should render plain textarea view")
	}
	if _, ok := highlightArea("# x", 0, 3, th); ok {
		t.Fatal("highlightArea should fail for non-positive width")
	}
	if _, ok := highlightArea("# x", 40, 0, th); ok {
		t.Fatal("highlightArea should fail for non-positive height")
	}
}

// A logical line wider than innerW must wrap onto extra rows (like the editable
// textarea) rather than truncate — no visible text may vanish on blur.
func TestHighlightLinesWrapsLongLine(t *testing.T) {
	withTrueColor(t)
	th, _ := themeByName("textual-dark")
	const innerW = 10
	line := "abcdefghijklmnopqrstuvwxyz" // 26 cols → 3 rows at width 10
	lines, ok := highlightLines(line, innerW, 8, th)
	if !ok {
		t.Fatal("highlightLines ok=false")
	}
	var plain strings.Builder
	for _, l := range lines {
		if w := lipgloss.Width(l); w > innerW {
			t.Fatalf("wrapped row exceeds innerW: width %d > %d (%q)", w, innerW, l)
		}
		plain.WriteString(stripANSI(l))
	}
	if !strings.HasPrefix(plain.String(), line) {
		t.Fatalf("wrapping lost content: joined rows %q do not contain %q", plain.String(), line)
	}
}

// The edit modal's description editor highlights when blurred and stays plain
// when focused (AC#1 names both modals).
func TestEditModalDescriptionHighlights(t *testing.T) {
	withTrueColor(t)
	th, _ := themeByName("textual-dark")
	st := newStyles(th)
	em := newEditModal(model.Ticket{"key": "TKB-1", "summary": "x", "description": "# Heading\n- item"}, th.surface)
	em.focus = 0 // summary focused → description blurred
	em.refocus()
	out := em.View(st, 80, 30)
	headingSeg := tokenStyle(chroma.GenericHeading, th).Render("# Heading")
	if !strings.Contains(out, headingSeg) {
		t.Fatal("blurred edit-modal description should be highlighted")
	}
	// Round-trip: highlighting must not alter the description value.
	if got := em.current().Description; got != "# Heading\n- item" {
		t.Fatalf("edit value mutated: %q", got)
	}
}

// truncateToWidth never exceeds the requested column budget and never splits a
// multibyte rune.
func TestTruncateToWidth(t *testing.T) {
	if got := truncateToWidth("hello world", 5); got != "hello" {
		t.Fatalf("truncateToWidth = %q, want %q", got, "hello")
	}
	if got := truncateToWidth("hi", 10); got != "hi" {
		t.Fatalf("short string should pass through, got %q", got)
	}
	if got := truncateToWidth("anything", 0); got != "" {
		t.Fatalf("zero budget should yield empty, got %q", got)
	}
	// Wide (2-col) runes: budget 5 fits two 日本 (4 cols) but not the third.
	if got := truncateToWidth("日本語x", 5); got != "日本" {
		t.Fatalf("multibyte truncate = %q, want %q", got, "日本")
	}
	if w := lipgloss.Width(truncateToWidth("日本語", 3)); w > 3 {
		t.Fatalf("multibyte truncate exceeded budget: width %d", w)
	}
}

// stripANSI removes SGR escape sequences so wrapped row text can be compared.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEsc = true
		case inEsc && r == 'm':
			inEsc = false
		case inEsc:
			// skip
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
