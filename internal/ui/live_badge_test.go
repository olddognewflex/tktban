package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/model"
)

// fakeLive is a LiveSource that serves canned herdr state and counts calls.
type fakeLive struct {
	probeErr error
	byKey    map[string]herdr.Live
	pollErr  error
	probes   int
	polls    int
}

func (f *fakeLive) Probe(context.Context) error {
	f.probes++
	return f.probeErr
}

func (f *fakeLive) Poll(context.Context) (map[string]herdr.Live, error) {
	f.polls++
	return f.byKey, f.pollErr
}

func live(pairs ...string) map[string]herdr.Live {
	out := map[string]herdr.Live{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i]] = herdr.Live{Status: herdr.Status(pairs[i+1]), Panes: []herdr.PaneRef{{PaneID: "w1:p1"}}}
	}
	return out
}

// liveBoard is a loaded board over a fakeLive, with TKT-1's frontmatter
// agent_status set to front.
func liveBoard(t *testing.T, src *fakeLive, front string) (Model, *captureRunner) {
	t.Helper()
	m, cr := testModel(t)
	m = m.WithLive(src)
	m = loadBoard(m)
	m.columns[0].Cards[0].AgentStatus = front
	return m, cr
}

func update(m Model, msg tea.Msg) (Model, tea.Cmd) {
	nm, cmd := m.Update(msg)
	return nm.(Model), cmd
}

// goLive runs the startup probe and the first poll, as the program would.
func goLive(t *testing.T, m Model) Model {
	t.Helper()
	probe := m.liveInit()
	if probe == nil {
		t.Fatal("no probe command with a live source")
	}
	m, poll := update(m, probe())
	if poll == nil {
		t.Fatal("successful probe did not start polling")
	}
	m, _ = update(m, poll())
	return m
}

// poll runs one scheduled poll: the tick fires, then its poll result lands.
func poll(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := update(m, liveTickMsg{})
	if cmd == nil {
		t.Fatal("tick did not poll")
	}
	m, _ = update(m, cmd())
	return m
}

func card(m Model) model.Card { return m.columns[0].Cards[0] }

func TestLiveBadgeMapping(t *testing.T) {
	cases := map[herdr.Status]string{
		herdr.StatusWorking: "⚙",
		herdr.StatusBlocked: "🙋",
		herdr.StatusIdle:    "",
		herdr.StatusDone:    "",
		herdr.StatusUnknown: "",
		"":                  "",
		"surprise":          "",
	}
	for st, want := range cases {
		if got := liveBadge(st); got != want {
			t.Errorf("liveBadge(%q) = %q, want %q", st, got, want)
		}
	}
}

func TestBadgeForPrecedence(t *testing.T) {
	cases := []struct {
		front  string
		live   herdr.Status
		liveOn bool
		want   string
	}{
		// live off: exactly the frontmatter badge, whatever herdr says
		{"processing", herdr.StatusBlocked, false, "⚙"},
		{"waiting", herdr.StatusWorking, false, "⏳"},
		{"", herdr.StatusWorking, false, ""},
		{"done", "", false, "✓"},
		{"blocked", "", false, "🚫"},
		// live on: herdr blocked / working win over any frontmatter
		{"", herdr.StatusBlocked, true, "🙋"},
		{"waiting", herdr.StatusBlocked, true, "🙋"},
		{"done", herdr.StatusWorking, true, "⚙"},
		{"blocked", herdr.StatusWorking, true, "⚙"},
		{"processing", herdr.StatusWorking, true, "⚙"},
		// live on, herdr quiet: frontmatter, except processing is hidden
		{"processing", "", true, ""},
		{"processing", herdr.StatusIdle, true, ""},
		{"processing", herdr.StatusDone, true, ""},
		{"waiting", "", true, "⏳"},
		{"waiting", herdr.StatusIdle, true, "⏳"},
		{"blocked", herdr.StatusDone, true, "🚫"},
		{"done", herdr.StatusUnknown, true, "✓"},
		{"idle", herdr.StatusIdle, true, ""},
		{"", "", true, ""},
	}
	for _, c := range cases {
		if got := badgeFor(c.front, c.live, c.liveOn); got != c.want {
			t.Errorf("badgeFor(%q, %q, on=%v) = %q, want %q", c.front, c.live, c.liveOn, got, c.want)
		}
	}
}

// Acceptance 1: an agent starts working and the card shows ⚙ from the live
// poll alone, with no tkt refresh.
func TestLiveWorkingShowsBadgeWithoutRefresh(t *testing.T) {
	src := &fakeLive{byKey: live("TKT-1", "working")}
	m, cr := liveBoard(t, src, "")
	before := len(cr.calls)
	m = goLive(t, m)
	if got := m.cardBadge(card(m)); got != "⚙" {
		t.Fatalf("badge = %q, want ⚙", got)
	}
	if !strings.Contains(m.View(), "⚙") {
		t.Fatalf("view missing ⚙:\n%s", m.View())
	}
	if len(cr.calls) != before {
		t.Fatalf("live poll ran tkt: %v", cr.calls[before:])
	}
	if !strings.Contains(m.View(), "herdr live") {
		t.Fatalf("subtitle missing herdr live:\n%s", m.View())
	}
}

// Acceptance 2: a permission prompt shows 🙋.
func TestLiveBlockedShowsHand(t *testing.T) {
	src := &fakeLive{byKey: live("TKT-1", "working")}
	m, _ := liveBoard(t, src, "")
	m = goLive(t, m)
	src.byKey = live("TKT-1", "blocked")
	m = poll(t, m)
	if got := m.cardBadge(card(m)); got != "🙋" {
		t.Fatalf("badge = %q, want 🙋", got)
	}
	if !strings.Contains(m.View(), "🙋") {
		t.Fatalf("view missing 🙋:\n%s", m.View())
	}
}

// Acceptance 3: the pane goes away; the badge clears on the next poll even
// though the frontmatter still says processing.
func TestLivePaneGoneClearsProcessing(t *testing.T) {
	src := &fakeLive{byKey: live("TKT-1", "working")}
	m, _ := liveBoard(t, src, "processing")
	m = goLive(t, m)
	if got := m.cardBadge(card(m)); got != "⚙" {
		t.Fatalf("while working: badge = %q, want ⚙", got)
	}
	src.byKey = map[string]herdr.Live{}
	m = poll(t, m)
	if got := m.cardBadge(card(m)); got != "" {
		t.Fatalf("pane gone: badge = %q, want none", got)
	}
	if strings.Contains(m.View(), "⚙") {
		t.Fatalf("view still shows ⚙:\n%s", m.View())
	}
}

func TestLivePaneGoneKeepsWaiting(t *testing.T) {
	src := &fakeLive{byKey: map[string]herdr.Live{}}
	m, _ := liveBoard(t, src, "waiting")
	m = goLive(t, m)
	if got := m.cardBadge(card(m)); got != "⏳" {
		t.Fatalf("badge = %q, want ⏳", got)
	}
}

// Acceptance 4: no live source (outside herdr) is today's behaviour.
func TestNoLiveSourceUnchanged(t *testing.T) {
	m, cr := testModel(t)
	m = loadBoard(m)
	m.columns[0].Cards[0].AgentStatus = "processing"
	if m.liveInit() != nil {
		t.Fatal("probe scheduled without a live source")
	}
	if got := m.cardBadge(card(m)); got != agentBadge("processing") {
		t.Fatalf("badge = %q, want frontmatter %q", got, agentBadge("processing"))
	}
	before := len(cr.calls)
	m, cmd := update(m, liveTickMsg{})
	if cmd != nil {
		t.Fatal("stray live tick polled without a source")
	}
	if len(cr.calls) != before || strings.Contains(m.View(), "herdr live") {
		t.Fatalf("live leaked into a board without herdr:\n%s", m.View())
	}
}

func TestLiveFailuresFallBackAfterThreshold(t *testing.T) {
	src := &fakeLive{byKey: live("TKT-1", "blocked")}
	m, _ := liveBoard(t, src, "processing")
	m = goLive(t, m)

	src.pollErr = errors.New("connection refused")
	for i := 1; i < liveMaxFails; i++ {
		m = poll(t, m)
		if !m.live.on || m.cardBadge(card(m)) != "🙋" {
			t.Fatalf("after %d failures: on=%v badge=%q, want last known 🙋", i, m.live.on, m.cardBadge(card(m)))
		}
		if m.live.nextPoll != livePollInterval {
			t.Fatalf("after %d failures: next poll in %v, want %v", i, m.live.nextPoll, livePollInterval)
		}
	}
	m = poll(t, m)
	if m.live.on {
		t.Fatal("live still on after the failure threshold")
	}
	if got := m.cardBadge(card(m)); got != "⚙" {
		t.Fatalf("fallback badge = %q, want frontmatter ⚙", got)
	}
	if m.statusKind != "warn" || !strings.Contains(m.status, "herdr") {
		t.Fatalf("want one herdr warning, got %q (%s)", m.status, m.statusKind)
	}
	if m.live.nextPoll != liveBackoff {
		t.Fatalf("next poll in %v, want backoff %v", m.live.nextPoll, liveBackoff)
	}

	// Still down: no second warning.
	m.status, m.statusKind = "", ""
	m = poll(t, m)
	if m.status != "" {
		t.Fatalf("warned again while already off: %q", m.status)
	}

	// herdr comes back.
	src.pollErr = nil
	src.byKey = live("TKT-1", "working")
	m = poll(t, m)
	if !m.live.on || m.live.fails != 0 || m.cardBadge(card(m)) != "⚙" || m.live.nextPoll != livePollInterval {
		t.Fatalf("did not recover: %+v badge=%q", m.live, m.cardBadge(card(m)))
	}
}

func TestLiveProbeMismatchDisables(t *testing.T) {
	src := &fakeLive{probeErr: fmt.Errorf("%w 23 from herdr 0.10.0 (want 22)", herdr.ErrProtocol)}
	m, _ := liveBoard(t, src, "processing")
	m, _ = update(m, m.liveInit()())
	if m.live.on {
		t.Fatal("live on after a failed probe")
	}
	if m.statusKind != "warn" || !strings.Contains(m.status, "protocol") {
		t.Fatalf("want protocol warning, got %q (%s)", m.status, m.statusKind)
	}
	if _, cmd := update(m, liveTickMsg{}); cmd != nil || src.polls != 0 {
		t.Fatal("polled after a failed probe")
	}
	if got := m.cardBadge(card(m)); got != "⚙" {
		t.Fatalf("badge = %q, want frontmatter ⚙", got)
	}
}

func TestLiveKeyIsCaseInsensitive(t *testing.T) {
	src := &fakeLive{byKey: live("TKT-1", "working")}
	m, _ := liveBoard(t, src, "")
	m = goLive(t, m)
	c := card(m)
	c.Key = "tkt-1"
	if got := m.cardBadge(c); got != "⚙" {
		t.Fatalf("lowercase key badge = %q, want ⚙", got)
	}
}

// Polling is serial: a result schedules the next tick, and a tick only polls.
func TestLiveMsgSchedulesNextPoll(t *testing.T) {
	src := &fakeLive{byKey: live("TKT-1", "working")}
	m, _ := liveBoard(t, src, "")
	m, poll := update(m, m.liveInit()())
	m, next := update(m, poll())
	if next == nil || m.live.nextPoll != livePollInterval {
		t.Fatalf("result did not schedule a poll: cmd=%v next=%v", next != nil, m.live.nextPoll)
	}
	if src.polls != 1 {
		t.Fatalf("polls = %d, want 1 before the tick fires", src.polls)
	}
	if msg := next(); msg != (liveTickMsg{}) {
		t.Fatalf("scheduled %T, want liveTickMsg", msg)
	}
	// The live loop keeps running under a modal and with auto-refresh off.
	m = step(m, key("a"))
	m = step(m, key("f"))
	if m.modal == nil || m.autoOn {
		t.Fatal("setup: want a modal open and auto-refresh off")
	}
	if _, cmd := update(m, liveTickMsg{}); cmd == nil {
		t.Fatal("live tick paused by modal / auto-refresh off")
	}
}

// 🙋 is two cells wide; the card must still fit its column.
func TestLiveBadgeFitsCard(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(termenv.TrueColor)
	src := &fakeLive{byKey: live("TKB-22", "blocked")}
	m, _ := liveBoard(t, src, "")
	m = goLive(t, m)
	c := model.Card{Key: "TKB-22", Priority: "Highest", BlockerCount: 3, Summary: "live herdr status on cards"}
	for _, width := range []int{16, 24, 40} {
		out := m.renderCard(c, width, false)
		if !strings.Contains(out, "🙋") {
			t.Fatalf("width %d: badge missing:\n%s", width, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if w := lipgloss.Width(line); w > width-2 {
				t.Fatalf("width %d: line %q is %d cells, over %d", width, line, w, width-2)
			}
		}
	}
}
