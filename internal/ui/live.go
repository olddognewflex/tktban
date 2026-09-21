package ui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/model"
)

// LiveSource is where live agent status comes from (herdr.SocketSource inside
// herdr). Probe runs at startup, and again while herdr is unreachable; Poll
// returns the herdr view of each ticket, keyed by uppercase ticket key.
type LiveSource interface {
	Probe(ctx context.Context) error
	Poll(ctx context.Context) (map[string]herdr.Live, error)
}

const (
	livePollInterval = 500 * time.Millisecond
	liveBackoff      = 2 * time.Second // retry cadence while herdr is unreachable
	liveMaxFails     = 3               // consecutive poll failures before falling back
	liveCallTimeout  = time.Second
)

// liveState is the board's live agent status. It runs on its own serial loop
// (probe → poll → result → tick → poll), apart from the tkt auto-refresh, and
// keeps going while a modal is open. src is nil outside herdr, and is dropped
// for good when herdr speaks an unsupported protocol.
type liveState struct {
	src      LiveSource
	probed   bool                  // the startup probe passed; ticks poll rather than re-probe
	on       bool                  // a poll succeeded lately: badges follow herdr
	byKey    map[string]herdr.Live // last good poll
	fails    int                   // consecutive failed probes, then polls
	nextPoll time.Duration         // delay of the tick last scheduled, 0 before any
}

// liveProbeMsg is the startup probe result.
type liveProbeMsg struct{ err error }

// liveMsg is one poll result.
type liveMsg struct {
	byKey map[string]herdr.Live
	err   error
}

// liveTickMsg fires when the next scheduled call (probe or poll) is due.
type liveTickMsg struct{}

// WithLive attaches a live status source. A nil source leaves the board
// exactly as it is without herdr.
func (m Model) WithLive(src LiveSource) Model {
	m.live = liveState{src: src}
	return m
}

// liveInit is the startup probe, or nil when there is no source.
func (m Model) liveInit() tea.Cmd {
	if m.live.src == nil {
		return nil
	}
	return liveProbeCmd(m.live.src)
}

func liveProbeCmd(src LiveSource) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), liveCallTimeout)
		defer cancel()
		return liveProbeMsg{err: src.Probe(ctx)}
	}
}

func livePollCmd(src LiveSource) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), liveCallTimeout)
		defer cancel()
		byKey, err := src.Poll(ctx)
		return liveMsg{byKey: byKey, err: err}
	}
}

// scheduleLive arms the next tick. Only a probe or poll result calls it, so at
// most one call to herdr is ever in flight.
func (m *Model) scheduleLive(d time.Duration) tea.Cmd {
	m.live.nextPoll = d
	return tea.Tick(d, func(time.Time) tea.Msg { return liveTickMsg{} })
}

// onLiveProbe starts polling once herdr answers. An unsupported protocol turns
// live status off for the run; anything else (herdr not up yet, a timeout)
// warns once and re-probes at the backoff. Badges stay frontmatter-only until
// the first good poll, and the board works as before either way. Warnings
// carry a short reason, never the socket path.
func (m Model) onLiveProbe(msg liveProbeMsg) (tea.Model, tea.Cmd) {
	if m.live.src == nil {
		return m, nil
	}
	if msg.err == nil {
		m.live.probed = true
		m.live.fails = 0
		return m, livePollCmd(m.live.src)
	}
	if errors.Is(msg.err, herdr.ErrProtocol) {
		m.live = liveState{}
		return m, m.setStatus("herdr live status off: protocol mismatch", "warn")
	}
	m.live.fails++
	var warn tea.Cmd
	if m.live.fails == 1 {
		warn = m.setStatus("herdr live status off: unavailable", "warn")
	}
	return m, tea.Batch(warn, m.scheduleLive(liveBackoff))
}

// onLive stores a poll result; a good one turns badges live. A failure keeps
// the last map for a couple of polls (a blip should not flicker badges); after
// liveMaxFails the board falls back to frontmatter badges, warns once per
// outage, and keeps retrying slowly so it recovers when herdr returns.
func (m Model) onLive(msg liveMsg) (tea.Model, tea.Cmd) {
	if m.live.src == nil {
		return m, nil
	}
	if msg.err == nil {
		m.live.byKey = msg.byKey
		m.live.fails = 0
		m.live.on = true
		return m, m.scheduleLive(livePollInterval)
	}
	m.live.fails++
	if m.live.fails < liveMaxFails {
		return m, m.scheduleLive(livePollInterval)
	}
	var warn tea.Cmd
	if m.live.fails == liveMaxFails {
		msg := "herdr live status unavailable; retrying" // never went live
		if m.live.on {
			msg = "herdr live status lost; retrying"
		}
		warn = m.setStatus(msg, "warn")
	}
	m.live.on = false
	m.live.byKey = nil
	return m, tea.Batch(warn, m.scheduleLive(liveBackoff))
}

// onLiveTick runs the next scheduled call: a poll, or another probe while
// herdr has not answered one yet.
func (m Model) onLiveTick() (tea.Model, tea.Cmd) {
	if m.live.src == nil {
		return m, nil
	}
	if !m.live.probed {
		return m, liveProbeCmd(m.live.src)
	}
	return m, livePollCmd(m.live.src)
}

// liveBadge maps herdr's agent_status to a card glyph. Only an agent that is
// busy (⚙) or waiting on a human (🙋, a permission prompt or question) gets a
// badge; idle, done and unknown are silent.
func liveBadge(s herdr.Status) string {
	switch s {
	case herdr.StatusWorking:
		return "⚙"
	case herdr.StatusBlocked:
		return "🙋"
	default:
		return ""
	}
}

// badgeFor picks a card's agent badge. With live status off it is exactly the
// frontmatter badge. With it on, herdr's working/blocked win; otherwise the
// frontmatter badge shows, except "processing", because herdr is the truth for
// that and has no agent working the ticket.
func badgeFor(front string, live herdr.Status, liveOn bool) string {
	if !liveOn {
		return agentBadge(front)
	}
	if b := liveBadge(live); b != "" {
		return b
	}
	if front == "processing" {
		return ""
	}
	return agentBadge(front)
}

// cardBadge is the agent badge for a card on this board.
func (m Model) cardBadge(c model.Card) string {
	var st herdr.Status
	if m.live.on {
		st = m.live.byKey[strings.ToUpper(c.Key)].Status
	}
	return badgeFor(c.AgentStatus, st, m.live.on)
}

// PaneFocuser is the optional half of a live source: it can also focus one of
// herdr's panes, which is how the board jumps from a card to the agent working
// it (herdr.SocketSource implements it). It is a separate interface from
// LiveSource on purpose, so a source that only reads status still drives the
// badges; such a source simply has no jump.
type PaneFocuser interface {
	FocusPane(ctx context.Context, paneID string) error
}

// jumpMsg is the result of one pane.focus for key's agent pane.
type jumpMsg struct {
	key    string
	paneID string
	err    error
}

// jumpCmd focuses one pane, bounded by the same 1s budget as every other
// herdr call so a wedged socket cannot freeze the board.
func jumpCmd(f PaneFocuser, key, paneID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), liveCallTimeout)
		defer cancel()
		return jumpMsg{key: key, paneID: paneID, err: f.FocusPane(ctx, paneID)}
	}
}

// jumpToAgentPane focuses the herdr pane running the selected card's agent.
// Every way this cannot work says which one it was, because the key does
// nothing visible otherwise.
func (m Model) jumpToAgentPane() (tea.Model, tea.Cmd) {
	if m.live.src == nil {
		return m, m.setStatus("Live agent status is off", "warn")
	}
	focuser, canFocus := m.live.src.(PaneFocuser)
	if !canFocus {
		return m, m.setStatus("This board can't focus herdr panes", "warn")
	}
	if !m.live.on {
		return m, m.setStatus("herdr live status unavailable", "warn")
	}
	card, ok := m.selectedCard()
	if !ok {
		return m, m.setStatus("Select a card first", "warn")
	}
	key := strings.ToUpper(card.Key)
	pane, ok := herdr.PickPane(m.live.byKey[key])
	if !ok {
		return m, m.setStatus("No agent pane for "+key, "warn")
	}
	return m, jumpCmd(focuser, key, pane.PaneID)
}

// onJump reports the jump. As a herdr popup the board has no pane id of its
// own and popup.close would SIGHUP it, so the focus round trip is already
// done by the time this runs and quitting is what closes the popup — leaving
// the newly focused agent pane in front. Outside a popup the board stays open
// and just says where focus went. A failure never quits: the board is the
// only thing left to look at.
func (m Model) onJump(msg jumpMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		text := "Could not focus " + msg.key + " agent pane"
		var apiErr *herdr.APIError
		if errors.As(msg.err, &apiErr) && apiErr.Code == "pane_not_found" {
			text = "Agent pane for " + msg.key + " is gone"
		}
		return m, m.setStatus(text, "warn")
	}
	if m.popup {
		return m, tea.Quit
	}
	return m, m.setStatus("Focused "+msg.key+" agent pane", "")
}
