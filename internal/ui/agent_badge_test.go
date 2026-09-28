package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/olddognewflex/tktban/internal/herdr"
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

// TKB-26: a stored badge (frontmatter or run) carries how long ago it was
// written; a herdr or absent badge does not, nor does a bad or future time.
func TestCardShowsAgentStatusAge(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(termenv.TrueColor)
	pinned := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return pinned }
	defer func() { now = time.Now }()

	th, _ := themeByName("textual-dark")
	m := Model{styles: newStyles(th)}
	cases := []struct {
		status, at, run, runAt string
		want                   string // "" = no age on the card
	}{
		{"waiting", "2026-09-28T09:00:00Z", "", "", "agent 3h"},
		{"done", "2026-09-28T11:48:00Z", "", "", "agent 12m"},
		{"blocked", "2026-09-25T11:00:00Z", "", "", "agent 3d"},
		{"processing", "2026-09-28T11:59:30Z", "", "", "agent <1m"},
		{"waiting", "", "", "", ""},                                                    // no timestamp
		{"waiting", "yesterday", "", "", ""},                                           // unparseable
		{"waiting", "2026-09-28T13:00:00Z", "", "", ""},                                // future: clock skew
		{"", "2026-09-28T09:00:00Z", "", "", ""},                                       // no badge
		{"idle", "2026-09-28T09:00:00Z", "", "", ""},                                   // no badge
		{"processing", "2026-09-28T09:00:00Z", "dead", "", ""},                         // suppressed
		{"", "", "halted", "2026-09-26T12:00:00Z", "agent 2d"},                         // run badge: the run's age
		{"waiting", "2026-09-28T09:00:00Z", "running", "", ""},                         // run badge, no run time
		{"done", "2026-09-28T11:00:00Z", "halted", "2026-09-28T09:00:00Z", "agent 1h"}, // stale run yields
	}
	for _, c := range cases {
		card := model.Card{Key: "TKB-26", Summary: "age", LaneHuman: "1h 2m",
			AgentStatus: c.status, AgentStatusAt: c.at, RunState: c.run, RunUpdated: c.runAt}
		out := m.renderCard(card, 40, false)
		if c.want == "" {
			if strings.Contains(out, "agent ") {
				t.Errorf("%+v: unexpected age on card:\n%s", c, out)
			}
			continue
		}
		if !strings.Contains(out, "⏱ 1h 2m  "+c.want) {
			t.Errorf("%+v: want %q beside the lane time:\n%s", c, c.want, out)
		}
	}
}

// A live badge is current, so it carries no age even over a dated frontmatter.
func TestLiveBadgeHasNoAgentAge(t *testing.T) {
	src := &fakeLive{byKey: live("TKT-1", "blocked")}
	m, _ := liveBoard(t, src, "waiting")
	m = goLive(t, m)
	c := card(m)
	c.AgentStatusAt = "2026-09-28T09:00:00Z"
	if out := m.renderCard(c, 40, false); strings.Contains(out, "agent ") {
		t.Fatalf("herdr-sourced badge carries an age:\n%s", out)
	}
}

// TKB-26: a refresh stamps each card with its `tkt agents` run state, and the
// badge follows it.
func TestRefreshStampsRunState(t *testing.T) {
	m, cr := testModel(t)
	cr.agents = `{"agents":[{"key":"tkt-1","state":"running"}]}`
	m = loadBoard(m)
	c := card(m)
	if c.Key != "TKT-1" || c.RunState != "running" {
		t.Fatalf("card = %+v, want TKT-1 running", c)
	}
	if got := m.cardBadge(c); got != "⚙" {
		t.Fatalf("badge = %q, want ⚙ from the run", got)
	}
	if cr.last("agents") == nil {
		t.Fatal("refresh did not read tkt agents")
	}
}

// An older tkt without `agents` exits 64: the board loads with no warning.
func TestRefreshWithoutAgentsVerbIsSilent(t *testing.T) {
	m, cr := testModel(t)
	cr.agents = failReply
	m = loadBoard(m)
	if !m.loaded || len(m.columns) == 0 {
		t.Fatal("board did not load")
	}
	if m.status != "" || m.statusKind != "" {
		t.Fatalf("status = %q (%s), want silence", m.status, m.statusKind)
	}
	if c := card(m); c.RunState != "" {
		t.Fatalf("run state = %q, want none", c.RunState)
	}
}

// TKB-26: a run that halted at a gate, then finished by hand, shows the newer
// frontmatter rather than ⏳ forever.
func TestStaleRunYieldsToNewerFrontmatter(t *testing.T) {
	m, cr := testModel(t)
	cr.list = `[{"key":"TKT-1","summary":"s","status_role":"todo","blocked_by":[],` +
		`"agent_status":"done","agent_status_at":"2026-09-28T11:00:00Z"}]`
	cr.agents = `{"agents":[{"key":"TKT-1","state":"halted","updated":"2026-09-28T09:00:00Z"}]}`
	m = loadBoard(m)
	if c := card(m); c.RunUpdated != "2026-09-28T09:00:00Z" || m.cardBadge(c) != "✓" {
		t.Fatalf("card %+v badge %q, want the newer frontmatter ✓", c, m.cardBadge(c))
	}
	cr.agents = `{"agents":[{"key":"TKT-1","state":"halted","updated":"2026-09-28T12:00:00Z"}]}`
	m = loadBoard(m)
	if got := m.cardBadge(card(m)); got != "⏳" {
		t.Fatalf("badge = %q, want the newer run's ⏳", got)
	}
}

// TKB-26 is read-only: a board that hides a stale frontmatter processing
// never writes agent_status back, across load, refresh and live polls.
func TestBoardNeverWritesAgentStatus(t *testing.T) {
	src := &fakeLive{byKey: map[string]herdr.Live{}} // herdr on, no pane
	m, cr := testModel(t)
	cr.list = `[{"key":"TKT-1","summary":"s","status_role":"todo","blocked_by":[],"agent_status":"processing"}]`
	m = loadBoard(m.WithLive(src))
	m = goLive(t, m)
	if got := m.cardBadge(card(m)); got != "" {
		t.Fatalf("badge = %q, want processing hidden", got)
	}
	m = loadBoard(m)
	m = poll(t, m)
	_ = m.View()
	for _, call := range cr.calls {
		if slices.Contains(call, "--agent-status") {
			t.Fatalf("board wrote agent_status: %v", call)
		}
	}
	if len(cr.calls) == 0 || src.polls < 2 {
		t.Fatalf("setup: calls=%d polls=%d", len(cr.calls), src.polls)
	}
}
