package herdr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The notify hook runs as one process per status event, several can overlap,
// and herdr gives no dedupe key. So the hook keeps the little it needs to
// remember in the plugin state dir: per pane, the last state_change_seq it
// claimed and when it last toasted which status. Every read-modify-write of
// that file happens under an exclusive flock on NotifyLockName.
const (
	NotifyStateName = "notify-state.json"
	NotifyLockName  = "notify.lock"
)

// notifyCooldown is the flap guard: a pane that toasts a status stays quiet
// for that same status this long, however often it flips back and forth.
const notifyCooldown = 30 * time.Second

// notifyStateTTL prunes entries for panes nothing has been heard from in a day,
// so the file cannot grow with every pane ever opened.
const notifyStateTTL = 24 * time.Hour

// notifyEntry is what the hook remembers about one pane.
type notifyEntry struct {
	Seq      uint64 `json:"seq"`        // highest state_change_seq claimed
	Status   Status `json:"status"`     // status of the last toast
	AtUnixMs int64  `json:"at_unix_ms"` // when that toast was claimed
}

// loadNotifyState reads the state file. A missing, unreadable or corrupt file
// is an empty state: the worst that costs is one repeated toast.
func loadNotifyState(path string) map[string]notifyEntry {
	state := map[string]notifyEntry{}
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	if json.Unmarshal(data, &state) != nil || state == nil {
		return map[string]notifyEntry{}
	}
	return state
}

// saveNotifyState writes the state file atomically — a temp file in the same
// directory renamed over the old one — so a reader never sees half a file and
// a symlink at the path is replaced rather than followed. Entries older than
// notifyStateTTL are dropped on the way out.
func saveNotifyState(path string, state map[string]notifyEntry, now time.Time) error {
	cutoff := now.Add(-notifyStateTTL).UnixMilli()
	kept := make(map[string]notifyEntry, len(state))
	for pane, e := range state {
		if e.AtUnixMs >= cutoff {
			kept[pane] = e
		}
	}
	data, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, NotifyStateName+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// claim is the outcome of trying to take the toast for one pane transition.
type claim int

const (
	claimed   claim = iota // this process sends the toast
	claimSeen              // another hook already claimed this seq (or a later one)
	claimFlap              // same status toasted less than notifyCooldown ago
)

// decideClaim is the pure rule behind claimNotify. seq 0 means herdr sent no
// state_change_seq (it defaults to 0), so only the flap guard applies.
func decideClaim(prev notifyEntry, had bool, seq uint64, status Status, now time.Time) (claim, notifyEntry) {
	if had && seq != 0 && prev.Seq >= seq {
		return claimSeen, prev
	}
	if had && prev.Status == status && now.Sub(time.UnixMilli(prev.AtUnixMs)) < notifyCooldown {
		// Keep the original time: continuous flapping must not stretch the
		// quiet period forever. Record the seq so a later hook for this same
		// transition reads as already seen.
		next := prev
		if seq > next.Seq {
			next.Seq = seq
		}
		return claimFlap, next
	}
	return claimed, notifyEntry{Seq: seq, Status: status, AtUnixMs: now.UnixMilli()}
}

// claimNotify decides, under the notify lock, whether this process sends the
// toast for pane's transition, and records the decision before returning —
// claim before send, so exactly one of several overlapping hooks toasts. An
// error (no lock before ctx ends, an unwritable state dir) means no toast.
func claimNotify(ctx context.Context, stateDir, pane string, seq uint64, status Status, now time.Time) (claim, error) {
	if !notifyDedupe {
		return claimed, nil
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return 0, err
	}
	unlock, err := lockNotify(ctx, filepath.Join(stateDir, NotifyLockName))
	if err != nil {
		return 0, err
	}
	defer unlock()

	path := filepath.Join(stateDir, NotifyStateName)
	state := loadNotifyState(path)
	prev, had := state[pane]
	c, next := decideClaim(prev, had, seq, status, now)
	if c == claimSeen {
		return c, nil // nothing new to record
	}
	state[pane] = next
	if err := saveNotifyState(path, state, now); err != nil {
		return 0, err
	}
	return c, nil
}
