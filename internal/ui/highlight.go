package ui

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
)

// areaPrompt mirrors the bubbles textarea default left gutter ("┃ "). The
// description/acceptance editors run with line numbers off (see mkArea /
// newEditModal), so this two-cell prompt is the whole gutter — the blurred
// preview replicates it exactly to keep the box footprint identical to the
// focused, editable textarea.
const areaPrompt = "┃ "

// areaView renders a description/acceptance editor. While focused it is the
// plain editable bubbles textarea; when blurred it shows the same text with
// markdown syntax highlighting. The highlight is display-only — Value() is never
// touched, so the submitted body is byte-identical to what a plain textarea
// produced. Any failure (no markdown grammar, lex error, empty body) falls back
// to the plain textarea view.
func areaView(ta textarea.Model, st styles) string {
	if ta.Focused() {
		return ta.View()
	}
	if strings.TrimSpace(ta.Value()) == "" {
		// Empty + blurred: let the textarea draw its placeholder/empty box.
		return ta.View()
	}
	out, ok := highlightArea(ta.Value(), ta.Width(), ta.Height(), st.t)
	if !ok {
		return ta.View()
	}
	return out
}

// highlightArea builds a rows×(prompt+innerW) block of syntax-highlighted
// markdown sized to match the textarea's footprint. Long source lines wrap onto
// extra rows; rows beyond the box height are clipped (the box does not grow),
// matching the textarea's fixed viewport.
func highlightArea(src string, innerW, rows int, t theme) (string, bool) {
	if innerW <= 0 || rows <= 0 {
		return "", false
	}
	lines, ok := highlightLines(src, innerW, rows, t)
	if !ok {
		return "", false
	}
	bg := lipgloss.NewStyle().Background(t.surface)
	prompt := lipgloss.NewStyle().Foreground(t.muted).Background(t.surface).Render(areaPrompt)
	var b strings.Builder
	for i := range rows {
		var content string
		if i < len(lines) {
			content = lines[i]
		}
		if pad := innerW - lipgloss.Width(content); pad > 0 {
			content += bg.Render(strings.Repeat(" ", pad))
		}
		b.WriteString(prompt + content)
		if i < rows-1 {
			b.WriteString("\n")
		}
	}
	return bg.Render(b.String()), true
}

// highlightLines tokenizes src with chroma's markdown lexer and returns styled
// display rows. Source lines wider than innerW are soft-wrapped onto extra rows
// (mirroring the textarea, which wraps rather than truncates) so no text
// silently disappears on blur. It stops once maxRows rows are produced, since
// the caller never shows more. Returns ok=false when the grammar is unavailable
// or lexing fails, so the caller can fall back to plain text.
func highlightLines(src string, innerW, maxRows int, t theme) ([]string, bool) {
	lexer := lexers.Get("markdown")
	if lexer == nil {
		return nil, false
	}
	// chroma's markdown block rules (heading, list, blockquote) only match a
	// line that ends in "\n"; without this the final line never highlights.
	// The extra newline yields one trailing empty logical line, which the
	// caller harmlessly pads/clips to the box height.
	it, err := lexer.Tokenise(nil, src+"\n")
	if err != nil {
		return nil, false
	}
	var (
		lines []string
		cur   strings.Builder
		curW  int
	)
	flush := func() {
		lines = append(lines, cur.String())
		cur.Reset()
		curW = 0
	}
	for _, tok := range it.Tokens() {
		if len(lines) >= maxRows {
			break
		}
		style := tokenStyle(tok.Type, t)
		// A token value may span multiple source lines; "\n" delimits rows.
		for i, part := range strings.Split(tok.Value, "\n") {
			if i > 0 {
				flush()
				if len(lines) >= maxRows {
					break
				}
			}
			// Emit the line's runes, wrapping onto a new row each time the
			// current row fills up.
			for part != "" {
				if curW >= innerW {
					flush()
					if len(lines) >= maxRows {
						break
					}
				}
				seg := truncateToWidth(part, innerW-curW)
				if seg == "" {
					// A single rune wider than the remaining space: if the row
					// already has content, wrap; otherwise force one rune so we
					// never loop forever.
					if curW > 0 {
						flush()
						continue
					}
					seg = firstRune(part)
				}
				cur.WriteString(style.Render(seg))
				curW += lipgloss.Width(seg)
				part = part[len(seg):]
			}
		}
	}
	flush()
	return lines, true
}

// tokenStyle maps a chroma markdown token type to a theme-derived lipgloss
// style. Every style carries the dialog surface background so the rendered cells
// stay opaque inside the modal.
func tokenStyle(tt chroma.TokenType, t theme) lipgloss.Style {
	s := lipgloss.NewStyle().Background(t.surface).Foreground(t.fg)
	switch {
	case tt == chroma.GenericHeading || tt == chroma.GenericSubheading:
		return s.Foreground(t.primary).Bold(true)
	case tt == chroma.GenericStrong:
		return s.Bold(true)
	case tt == chroma.GenericEmph:
		return s.Foreground(t.accent).Italic(true)
	case tt == chroma.GenericDeleted:
		return s.Foreground(t.muted).Strikethrough(true)
	case tt == chroma.Keyword || tt.InCategory(chroma.Keyword):
		// List bullets, numbers and blockquote markers.
		return s.Foreground(t.primary)
	case tt.InCategory(chroma.Literal):
		// Inline code, fenced code blocks and other literals.
		return s.Foreground(t.accent)
	case tt.InCategory(chroma.Name):
		// Links / mentions.
		return s.Foreground(t.primary).Underline(true)
	default:
		return s
	}
}

// firstRune returns the first UTF-8 rune of s as a string (empty if s is empty).
func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

// truncateToWidth returns the longest prefix of s whose display width does not
// exceed max columns. It is ANSI-free input (raw token text), so plain rune
// accumulation with lipgloss width accounting is sufficient.
func truncateToWidth(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= max {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > max {
			break
		}
		b.WriteString(string(r))
		w += rw
	}
	return b.String()
}
