package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/olddognewflex/tktban/internal/model"
)

// TKB-19: a card surfaces its agent's execution state as a badge. processing
// (and the other active states) render a glyph; empty/idle render nothing.
func TestCardRendersAgentStatusBadge(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(termenv.TrueColor)

	th, _ := themeByName("textual-dark")
	m := Model{styles: newStyles(th)}

	cases := []struct {
		status string
		glyph  string
		want   bool
	}{
		{"processing", agentBadge("processing"), true},
		{"waiting", agentBadge("waiting"), true},
		{"done", agentBadge("done"), true},
		{"blocked", agentBadge("blocked"), true},
		{"", "", false},
		{"idle", "", false},
	}
	for _, c := range cases {
		card := model.Card{Key: "TKB-19", Summary: "badge me", AgentStatus: c.status}
		out := m.renderCard(card, 24, false)
		if c.want {
			if c.glyph == "" || !strings.Contains(out, c.glyph) {
				t.Fatalf("status %q: card missing badge %q:\n%s", c.status, c.glyph, out)
			}
		} else if g := agentBadge(c.status); g != "" {
			t.Fatalf("status %q: expected no badge, got glyph %q", c.status, g)
		}
	}
}

// Empty/idle must not draw the processing glyph (acceptance: no badge for idle).
func TestIdleAgentStatusHasNoBadge(t *testing.T) {
	if got := agentBadge(""); got != "" {
		t.Fatalf("empty agent_status badge = %q, want none", got)
	}
	if got := agentBadge("idle"); got != "" {
		t.Fatalf("idle agent_status badge = %q, want none", got)
	}
	processing := agentBadge("processing")
	if processing == "" {
		t.Fatal("processing must render a badge glyph")
	}

	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(termenv.TrueColor)
	th, _ := themeByName("textual-dark")
	m := Model{styles: newStyles(th)}
	out := m.renderCard(model.Card{Key: "TKB-1", Summary: "idle", AgentStatus: "idle"}, 24, false)
	if strings.Contains(out, processing) {
		t.Fatalf("idle card unexpectedly carries the processing glyph:\n%s", out)
	}
}
