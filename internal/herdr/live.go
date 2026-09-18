package herdr

import (
	"context"
	"errors"
	"fmt"
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

// ErrProtocol means herdr speaks a socket protocol tktban was not built for.
var ErrProtocol = errors.New("unsupported herdr protocol")

// SocketSource polls the herdr socket for live ticket status.
type SocketSource struct {
	Client *Client
	// KeysForDir maps a pane directory to ticket keys; nil means KeysForDir.
	KeysForDir func(context.Context, string) []string
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
	return byKey, nil
}
