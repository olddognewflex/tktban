package ui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/model"
)

// LiveSource is where live agent status comes from (herdr.SocketSource inside
// herdr). Probe runs once at startup; Poll returns the herdr view of each
// ticket, keyed by uppercase ticket key.
type LiveSource interface {
	Probe(ctx context.Context) error
	Poll(ctx context.Context) (map[string]herdr.Live, error)
}

const (
	livePollInterval = 500 * time.Millisecond
	liveBackoff      = 2 * time.Second // retry cadence once live status is lost
	liveMaxFails     = 3               // consecutive poll failures before falling back
	liveCallTimeout  = time.Second
)

// liveState is the board's live agent status. It runs on its own serial loop
// (poll → result → tick → poll), apart from the tkt auto-refresh, and keeps
// going while a modal is open. src is nil outside herdr, and is dropped for
// good when the startup probe fails.
type liveState struct {
	src      LiveSource
	on       bool                  // badges follow herdr; false means frontmatter only
	byKey    map[string]herdr.Live // last good poll
	fails    int                   // consecutive failed polls
	nextPoll time.Duration         // delay of the tick last scheduled, 0 before any
}

// liveProbeMsg is the startup probe result.
type liveProbeMsg struct{ err error }

// liveMsg is one poll result.
type liveMsg struct {
	byKey map[string]herdr.Live
	err   error
}

// liveTickMsg fires when the next poll is due.
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
	src := m.live.src
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

// scheduleLive arms the next poll. Only a poll result calls it, so at most one
// poll is ever in flight.
func (m *Model) scheduleLive(d time.Duration) tea.Cmd {
	m.live.nextPoll = d
	return tea.Tick(d, func(time.Time) tea.Msg { return liveTickMsg{} })
}

// onLiveProbe starts polling, or turns live status off for this run with one
// warning. The board works as before either way.
func (m Model) onLiveProbe(msg liveProbeMsg) (tea.Model, tea.Cmd) {
	if m.live.src == nil {
		return m, nil
	}
	if msg.err != nil {
		m.live = liveState{}
		return m, m.setStatus("herdr live status off: "+msg.err.Error(), "warn")
	}
	m.live.on = true
	return m, livePollCmd(m.live.src)
}

// onLive stores a poll result. A failure keeps the last map for a couple of
// polls (a blip should not flicker badges); after liveMaxFails the board falls
// back to frontmatter badges, warns once, and keeps retrying slowly so it
// recovers when herdr returns.
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
	if m.live.on {
		m.live.on = false
		m.live.byKey = nil
		warn = m.setStatus("herdr live status lost: "+msg.err.Error(), "warn")
	}
	return m, tea.Batch(warn, m.scheduleLive(liveBackoff))
}

func (m Model) onLiveTick() (tea.Model, tea.Cmd) {
	if m.live.src == nil {
		return m, nil
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
