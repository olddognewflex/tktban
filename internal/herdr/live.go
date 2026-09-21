package herdr

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Status is herdr's agent_status for a pane.
type Status string

const (
	StatusIdle    Status = "idle"
	StatusWorking Status = "working"
	StatusBlocked Status = "blocked" // herdr saw an approval prompt or question
	StatusDone    Status = "done"    // finished, not yet viewed; same as idle for the board
	StatusUnknown Status = "unknown"
)

// rank orders statuses for a ticket with several agent panes: the one that
// needs a human wins, then the one still busy. idle and done tie on purpose
// (see docs/herdr-events.md).
func rank(s Status) int {
	switch s {
	case StatusBlocked:
		return 3
	case StatusWorking:
		return 2
	case StatusIdle, StatusDone:
		return 1
	default:
		return 0
	}
}

// PaneRef locates one agent pane working a ticket. Kept so a later feature can
// jump from a card to its pane.
type PaneRef struct {
	PaneID      string
	WorkspaceID string
	TabID       string
	Status      Status
	Focused     bool
}

// Live is the herdr view of one ticket: the most urgent status across its
// agent panes, and the panes themselves.
type Live struct {
	Status Status
	Panes  []PaneRef
}

// Resolve joins herdr's agent panes to ticket keys. A pane counts when it has
// an agent and its directory's branch names a ticket; the foreground cwd is
// preferred because that is where the agent actually runs. A branch naming
// several keys puts the pane under each of them. Keys come back as keysForDir
// returns them (uppercase for KeysForDir).
func Resolve(agents []Agent, keysForDir func(string) []string) map[string]Live {
	out := map[string]Live{}
	cache := map[string][]string{} // dir -> keys, so each dir is read once per call
	for _, a := range agents {
		if a.Agent == nil || *a.Agent == "" {
			continue
		}
		dir := a.ForegroundCwd
		if dir == "" {
			dir = a.Cwd
		}
		if dir == "" {
			continue
		}
		keys, seen := cache[dir]
		if !seen {
			keys = keysForDir(dir)
			cache[dir] = keys
		}
		ref := PaneRef{
			PaneID:      a.PaneID,
			WorkspaceID: a.WorkspaceID,
			TabID:       a.TabID,
			Status:      a.Status,
			Focused:     a.Focused,
		}
		for _, key := range keys {
			l := out[key]
			if len(l.Panes) == 0 || rank(a.Status) > rank(l.Status) {
				l.Status = a.Status
			}
			l.Panes = append(l.Panes, ref)
			out[key] = l
		}
	}
	return out
}

// PickPane chooses the pane to jump to for one ticket. A pane herdr already
// has focused wins outright — it is where the human last was — otherwise the
// most urgent one does (blocked > working > idle/done > unknown), and equal
// panes keep agent.list order, which Resolve preserves. False means the ticket
// has no agent pane at all.
func PickPane(l Live) (PaneRef, bool) {
	var best PaneRef
	found := false
	for _, p := range l.Panes {
		switch {
		case !found:
			best, found = p, true
		case best.Focused:
			// Nothing outranks the pane the human is already in.
		case p.Focused || rank(p.Status) > rank(best.Status):
			best = p
		}
	}
	return best, found
}

// ErrProtocol means herdr speaks a socket protocol tktban was not built for.
var ErrProtocol = errors.New("unsupported herdr protocol")

// SocketSource polls the herdr socket for live ticket status.
//
// Polls are expected to be serialised — the board never has two herdr calls in
// flight — and published is only ever touched from inside Poll, so the type
// carries no lock of its own.
type SocketSource struct {
	Client *Client
	// KeysForDir maps a pane directory to ticket keys; nil means KeysForDir.
	KeysForDir func(context.Context, string) []string
	// Tokens publishes each resolved pane's ticket key back to herdr as a
	// `ticket` pane-metadata token, so a herdr sidebar row configured with
	// $ticket can show it. Off leaves herdr's metadata untouched.
	Tokens bool

	// published is what herdr was last told for each pane: pane id -> the
	// token value it holds. Only successful calls land here, so a failed one
	// is retried on the next poll rather than assumed done.
	published map[string]string
}

// NewSocketSource returns a source reading the herdr socket at path.
func NewSocketSource(path string) *SocketSource {
	return &SocketSource{Client: NewClient(path)}
}

// Probe checks herdr is reachable and speaks SupportedProtocol. A mismatch
// wraps ErrProtocol.
func (s *SocketSource) Probe(ctx context.Context) error {
	p, err := s.Client.Ping(ctx)
	if err != nil {
		return err
	}
	if p.Protocol != SupportedProtocol {
		return fmt.Errorf("%w %d from herdr %s (want %d)", ErrProtocol, p.Protocol, p.Version, SupportedProtocol)
	}
	return nil
}

// Poll lists herdr's agent panes and resolves them to tickets. Branches are
// re-read every call, so a checkout inside a pane shows on the next poll. A
// resolve that outlives ctx (a hung filesystem) counts as a failed poll.
//
// Poll is also a writer: with Tokens on, a successful poll reports each pane's
// ticket key back to herdr as pane metadata (see publishTokens). That work is
// best-effort and bounded by ctx — it can never fail a poll or change the
// statuses returned — but it does happen before Poll returns, so a poll that
// changes many panes at once costs those round trips.
func (s *SocketSource) Poll(ctx context.Context) (map[string]Live, error) {
	agents, err := s.Client.AgentList(ctx)
	if err != nil {
		return nil, err
	}
	keysFor := s.KeysForDir
	if keysFor == nil {
		keysFor = KeysForDir
	}
	// Once ctx ends, stop touching the disk; the result is discarded anyway.
	byKey := Resolve(agents, func(dir string) []string {
		if ctx.Err() != nil {
			return nil
		}
		return keysFor(ctx, dir)
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.publishTokens(ctx, byKey)
	return byKey, nil
}

// publishTokens tells herdr which ticket each agent pane is on, as a `ticket`
// pane-metadata token, and clears the token for panes that have dropped out of
// the mapping (closed, released, or moved to a branch naming no ticket). Only
// changes are sent, so a board sitting idle makes no calls at all.
//
// A pane on a branch naming several tickets gets them sorted and joined with
// "," so the same set always produces the same token.
//
// Errors are swallowed on purpose: the sidebar is a nicety and the badges are
// the job. A pane whose call failed is simply left out of published, so the
// next poll tries it again.
func (s *SocketSource) publishTokens(ctx context.Context, byKey map[string]Live) {
	if !s.Tokens {
		return
	}
	want := paneTokens(byKey)
	for _, pane := range slices.Sorted(maps.Keys(want)) {
		if s.published[pane] == want[pane] {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		value := want[pane]
		if err := s.Client.ReportPaneTokens(ctx, pane, map[string]*string{TicketToken: &value}); err != nil {
			continue
		}
		if s.published == nil {
			s.published = map[string]string{}
		}
		s.published[pane] = value
	}
	for _, pane := range slices.Sorted(maps.Keys(s.published)) {
		if _, ok := want[pane]; ok {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if err := s.Client.ReportPaneTokens(ctx, pane, map[string]*string{TicketToken: nil}); err != nil {
			continue
		}
		delete(s.published, pane)
	}
}

// paneTokens inverts Resolve's key -> panes mapping into the token value each
// pane should carry.
func paneTokens(byKey map[string]Live) map[string]string {
	keys := map[string][]string{}
	for key, l := range byKey {
		for _, p := range l.Panes {
			keys[p.PaneID] = append(keys[p.PaneID], key)
		}
	}
	out := make(map[string]string, len(keys))
	for pane, k := range keys {
		slices.Sort(k)
		out[pane] = strings.Join(slices.Compact(k), ",")
	}
	return out
}

// FocusPane focuses a herdr pane, so the board can jump from a card to the
// agent working it. One call is enough across workspaces and tabs.
func (s *SocketSource) FocusPane(ctx context.Context, paneID string) error {
	return s.Client.FocusPane(ctx, paneID)
}
