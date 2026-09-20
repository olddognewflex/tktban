// QA Author — adversarial tests
// Break-It dimensions covered: a second outage after recovery must warn
// again (not go silent forever), a protocol mismatch must permanently no-op
// every future tick (not just the next one), and "no live source" must
// leave board rendering byte-identical to pre-TKB-22 behaviour across every
// frontmatter agent_status value, not just "processing".
package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/herdr"
)

// TestLiveFailuresFallBackAfterThreshold already proves one outage warns
// once and recovery clears it. This proves the warning is per-outage, not
// one-shot for the model's whole life: after recovering, a fresh outage must
// warn again, worded as "lost" (it had gone live in between).
func TestLiveSecondOutageWarnsAgainAfterRecovery(t *testing.T) {
	src := &fakeLive{byKey: live("TKT-1", "working")}
	m, _ := liveBoard(t, src, "")
	m = goLive(t, m)

	src.pollErr = errors.New("connection refused")
	for i := 1; i < liveMaxFails; i++ {
		m = poll(t, m)
	}
	m = poll(t, m) // crosses the threshold
	if m.statusKind != "warn" || !strings.Contains(m.status, "herdr") {
		t.Fatalf("want first outage warning, got %q (%s)", m.status, m.statusKind)
	}

	src.pollErr = nil
	src.byKey = live("TKT-1", "working")
	m = poll(t, m)
	if !m.live.on {
		t.Fatal("did not recover from the first outage")
	}
	m.status, m.statusKind = "", ""

	src.pollErr = errors.New("connection refused again")
	for i := 1; i < liveMaxFails; i++ {
		m = poll(t, m)
		if m.status != "" {
			t.Fatalf("warned before the threshold on the second outage (fail %d): %q", i, m.status)
		}
	}
	m = poll(t, m)
	if m.statusKind != "warn" || !strings.Contains(m.status, "lost") {
		t.Fatalf("want a second 'lost' warning, got %q (%s)", m.status, m.statusKind)
	}
}

// A protocol mismatch drops the source for the whole run: not just the tick
// right after the probe, but every tick for as long as the model lives.
func TestLiveProtocolMismatchTicksAreNoOpsIndefinitely(t *testing.T) {
	src := &fakeLive{probeErr: fmt.Errorf("%w 23 from herdr 0.10.0 (want 22)", herdr.ErrProtocol)}
	m, _ := liveBoard(t, src, "")
	m, _ = update(m, m.liveInit()())
	if m.live.src != nil {
		t.Fatal("live source not dropped after a protocol mismatch")
	}
	for i := 0; i < 5; i++ {
		var cmd tea.Cmd
		m, cmd = update(m, liveTickMsg{})
		if cmd != nil {
			t.Fatalf("tick %d issued a command after a protocol mismatch", i)
		}
	}
	if src.probes != 1 || src.polls != 0 {
		t.Fatalf("probes=%d polls=%d, want exactly the one startup probe and nothing since", src.probes, src.polls)
	}
}

// Acceptance 4, exhaustively: with no live source at all, every frontmatter
// agent_status value must render exactly as it did before TKB-22 — the
// frontmatter badge and nothing else, no matter what value shows up. The
// expected glyphs are hardcoded (not read back from agentBadge) so this
// cannot pass merely because it recomputes the same, possibly-broken,
// function it is checking.
func TestNoLiveSourceRenderMatchesAllFrontmatterStatuses(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"idle":                "",
		"processing":          "⚙",
		"waiting":             "⏳",
		"done":                "✓",
		"blocked":             "🚫",
		"unrecognized-status": "",
	}
	for status, want := range cases {
		m, _ := testModel(t)
		m = loadBoard(m)
		m.columns[0].Cards[0].AgentStatus = status

		if got := m.cardBadge(card(m)); got != want {
			t.Errorf("status %q: cardBadge = %q, want %q", status, got, want)
		}
		view := m.View()
		if want != "" && !strings.Contains(view, want) {
			t.Errorf("status %q: view missing expected badge %q:\n%s", status, want, view)
		}
		if strings.Contains(view, "herdr live") {
			t.Errorf("status %q: view mentions herdr live status without a source", status)
		}
	}
}
