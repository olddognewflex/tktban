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
// returns the herdr view of each ticket, keyed by uppercase ticket key, along
// with every pane that still has an agent (see herdr.Snapshot).
type LiveSource interface {
	Probe(ctx context.Context) error
	Poll(ctx context.Context) (herdr.Snapshot, error)
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
	src    LiveSource
	probed bool                  // the startup probe passed; ticks poll rather than re-probe
	on     bool                  // a poll succeeded lately: badges follow herdr
	byKey  map[string]herdr.Live // last good poll
	// agentPanes is the last good poll's live agent panes, keyed or not; the
	// write-back reads it to tell a closed pane from one whose branch stopped
	// naming its ticket.
	agentPanes map[string]bool
	polledAt   time.Time     // when the last good poll started
	fails      int           // consecutive failed probes, then polls
	nextPoll   time.Duration // delay of the tick last scheduled, 0 before any
}

// liveProbeMsg is the startup probe result.
type liveProbeMsg struct{ err error }

// liveMsg is one poll result. polledAt is when the poll started, read before
// herdr was asked, so anything stamped after it is certainly after the poll.
type liveMsg struct {
	byKey      map[string]herdr.Live
	agentPanes map[string]bool
	polledAt   time.Time
	err        error
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
		at := now()
		snap, err := src.Poll(ctx)
		return liveMsg{byKey: snap.ByKey, agentPanes: snap.AgentPanes, polledAt: at, err: err}
	}
}

// scheduleLive arms the next tick. Only a probe or poll result calls it, so at
// most one probe or poll is ever in flight. A jump (jumpCmd) is not on that
// clock and can overlap one; that is safe because a Client dials per call and
// a SocketSource keeps no mutable state.
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
	next := m.scheduleLive(liveBackoff)
	return m, tea.Batch(warn, next)
}

// onLive stores a poll result; a good one turns badges live. A failure keeps
// the last map for a couple of polls (a blip should not flicker badges); after
// liveMaxFails the board falls back to frontmatter badges, warns once per
// outage, and keeps retrying slowly so it recovers when herdr returns.
//
// Only a good poll feeds the agent_status write-back (reconcile): a failed one
// is no evidence that a pane closed, so it neither counts towards nor resets
// a pane's absence.
func (m Model) onLive(msg liveMsg) (tea.Model, tea.Cmd) {
	if m.live.src == nil {
		return m, nil
	}
	if msg.err == nil {
		m.live.byKey = msg.byKey
		m.live.agentPanes = msg.agentPanes
		m.live.polledAt = msg.polledAt
		m.live.fails = 0
		m.live.on = true
		// reconcile and scheduleLive both change m, so they run before m is
		// returned: Go leaves the order of a return's operands unspecified.
		recon := m.reconcile()
		next := m.scheduleLive(livePollInterval)
		return m, tea.Batch(recon, next)
	}
	m.live.fails++
	if m.live.fails < liveMaxFails {
		next := m.scheduleLive(livePollInterval)
		return m, next
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
	m.live.agentPanes = nil
	next := m.scheduleLive(liveBackoff)
	return m, tea.Batch(warn, next)
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

// liveArg is herdr's side of model.MergeAgent: "" while live status is off,
// model.LiveAbsent when herdr is on but has no pane for the ticket.
func liveArg(st herdr.Status, liveOn bool) string {
	switch {
	case !liveOn:
		return ""
	case st == "":
		return model.LiveAbsent
	default:
		return string(st)
	}
}

// viewBadge is the glyph for a merged agent view: 🙋 for an agent waiting on
// you (herdr saw a permission prompt or question), otherwise the frontmatter
// glyph for its status. herdr idle, done and unknown merge to no badge.
func viewBadge(v model.AgentView) string {
	if v.Status == model.AgentNeedsYou {
		return "🙋"
	}
	return agentBadge(v.Status)
}

// badgeFor picks a card's agent badge from its frontmatter, its `tkt agents`
// run state and herdr's status; the precedence lives in model.MergeAgent.
func badgeFor(front, run string, live herdr.Status, liveOn bool) string {
	return viewBadge(model.MergeAgent(liveArg(live, liveOn), run, front))
}

// liveStatus is herdr's status for c, or "" while live status is off.
func (m Model) liveStatus(c model.Card) herdr.Status {
	if !m.live.on {
		return ""
	}
	return m.live.byKey[strings.ToUpper(c.Key)].Status
}

// agentView is the merged agent state for a card on this board.
func (m Model) agentView(c model.Card) model.AgentView {
	return model.MergeAgent(liveArg(m.liveStatus(c), m.live.on), model.EffectiveRun(c), c.AgentStatus)
}

// cardBadge is the agent badge for a card on this board.
func (m Model) cardBadge(c model.Card) string {
	return badgeFor(c.AgentStatus, model.EffectiveRun(c), m.liveStatus(c), m.live.on)
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
		if errors.As(msg.err, &apiErr) && apiErr.Code == herdr.CodePaneNotFound {
			text = "Agent pane for " + msg.key + " is gone"
		}
		return m, m.setStatus(text, "warn")
	}
	if m.popup {
		return m, tea.Quit
	}
	return m, m.setStatus("Focused "+msg.key+" agent pane", "")
}
