package herdr

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"time"
)

// The notify hook runs as one process per status event, several can overlap,
// and herdr gives no dedupe key. So the hook keeps the little it needs to
// remember in the plugin state dir, per pane: the last state_change_seq it
// handled and when, when it last toasted each status, and which statuses a
// run is currently settling. Every read-modify-write of that file happens
// under an exclusive flock on NotifyLockName.
const (
	NotifyStateName = "notify-state.json"
	NotifyLockName  = "notify.lock"
)

// notifyCooldown is the flap guard: a pane that toasts a status stays quiet
// for that same status this long, however often it flips in between.
const notifyCooldown = 30 * time.Second

// seenWindow is how long a handled state_change_seq silences the same or a
// lower seq for its pane. It only has to cover the hooks herdr runs for one
// transition, which all finish within a few seconds. Past it, a seq at or
// below the stored one is not a straggler but a reset counter: herdr's seq
// is stamped per pane and starts again after a restart, while pane ids like
// wC:p1 survive it (session.json keeps them). Without the window a pane
// would stay silent until its new counter caught up with the old one.
const seenWindow = 60 * time.Second

// settleHold is how long a "settling" marker is honoured. A run clears its
// own marker when it finishes; the hold only matters for a run that died,
// and it covers the hook's own 5 s context in cmd/tktban.
const settleHold = 5 * time.Second

// notifyStateTTL drops panes nothing has been heard from in a day, on load
// and on save, so the file cannot grow with every pane ever opened and a
// stale entry never outlives it.
const notifyStateTTL = 24 * time.Hour

// staleTemp is how old a leftover temp file from a crashed save must be
// before a save removes it.
const staleTemp = time.Minute

// notifyEntry is what the hook remembers about one pane.
type notifyEntry struct {
	Seq    uint64           `json:"seq"`              // last state_change_seq handled
	SeenAt int64            `json:"seen_at"`          // unix ms it was handled
	At     map[Status]int64 `json:"at,omitempty"`     // unix ms of the last toast, per status
	Settle map[Status]int64 `json:"settle,omitempty"` // unix ms a settling run holds until, per status
}

// latest is the newest time anywhere in the entry, for the TTL.
func (e notifyEntry) latest() int64 {
	t := e.SeenAt
	for _, v := range e.At {
		t = max(t, v)
	}
	for _, v := range e.Settle {
		t = max(t, v)
	}
	return t
}

func (e notifyEntry) clone() notifyEntry {
	e.At = maps.Clone(e.At)
	e.Settle = maps.Clone(e.Settle)
	return e
}

func (e notifyEntry) empty() bool {
	return e.Seq == 0 && e.SeenAt == 0 && len(e.At) == 0 && len(e.Settle) == 0
}

// since is now minus a unix-ms time, with ok false for a zero time or one in
// the future (a clock stepped backwards), which count as "long ago".
func since(now time.Time, ms int64) (time.Duration, bool) {
	if ms == 0 {
		return 0, false
	}
	d := now.Sub(time.UnixMilli(ms))
	return d, d >= 0
}

// prune drops entries older than notifyStateTTL.
func prune(state map[string]notifyEntry, now time.Time) {
	cutoff := now.Add(-notifyStateTTL).UnixMilli()
	for pane, e := range state {
		if e.latest() < cutoff {
			delete(state, pane)
		}
	}
}

// loadNotifyState reads the state file, already pruned. A missing, unreadable
// or corrupt file is an empty state: the worst that costs is one repeated
// toast.
func loadNotifyState(path string, now time.Time) map[string]notifyEntry {
	state := map[string]notifyEntry{}
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	if json.Unmarshal(data, &state) != nil || state == nil {
		return map[string]notifyEntry{}
	}
	prune(state, now)
	return state
}

// saveNotifyState writes the state file atomically — a temp file in the same
// directory renamed over the old one — so a reader never sees half a file and
// a symlink at the path is replaced rather than followed. It prunes on the
// way out and sweeps temp files a crashed save left behind.
func saveNotifyState(path string, state map[string]notifyEntry, now time.Time) error {
	kept := maps.Clone(state)
	prune(kept, now)
	data, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	sweepTemps(dir, now)
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

// sweepTemps removes notify-state temp files older than staleTemp. Saves run
// under the lock, so a younger one cannot belong to a save in progress
// either; the age check is only a margin.
func sweepTemps(dir string, now time.Time) {
	matches, _ := filepath.Glob(filepath.Join(dir, NotifyStateName+".*.tmp"))
	for _, m := range matches {
		info, err := os.Lstat(m)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if now.Sub(info.ModTime()) > staleTemp {
			os.Remove(m)
		}
	}
}

// withNotifyState runs fn over the state under the notify lock, and saves
// when fn reports a change. An error (no lock before ctx ends, an unwritable
// state dir) means fn's decision did not stick.
func withNotifyState(ctx context.Context, stateDir string, now time.Time, fn func(map[string]notifyEntry) bool) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	unlock, err := lockNotify(ctx, filepath.Join(stateDir, NotifyLockName))
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(stateDir, NotifyStateName)
	state := loadNotifyState(path, now)
	if !fn(state) {
		return nil
	}
	return saveNotifyState(path, state, now)
}

// claim is the outcome of trying to take the toast for one pane transition.
type claim int

const (
	claimed   claim = iota // this process sends the toast
	claimSeen              // another hook just handled this seq (or a later one)
	claimFlap              // same status toasted less than notifyCooldown ago
)

// decideClaim is the pure rule behind claimNotify; it returns the entry to
// store. seq 0 means herdr sent no state_change_seq (it defaults to 0), so
// only the flap guard applies.
//
// An equal seq is handled however old: herdr can re-send a status event
// without a new transition (a title or label change), and a pane sitting
// blocked must not re-toast for it. Only a lower seq ages out, after
// seenWindow, as a counter reset. The accepted cost: a herdr restart within
// seenWindow of a toast can drop that pane's first prompt after it.
func decideClaim(prev notifyEntry, had bool, seq uint64, status Status, now time.Time) (claim, notifyEntry) {
	if had && seq != 0 {
		if prev.Seq == seq {
			return claimSeen, prev
		}
		if prev.Seq > seq {
			if d, ok := since(now, prev.SeenAt); ok && d < seenWindow {
				return claimSeen, prev
			}
			// Older than any straggler could be: herdr's counter was reset.
		}
	}
	next := prev.clone()
	next.Seq, next.SeenAt = seq, now.UnixMilli()
	if d, ok := since(now, prev.At[status]); had && ok && d < notifyCooldown {
		// Keep the toast time: continuous flapping must not stretch the quiet
		// period forever. The seq is recorded so a straggler for this same
		// transition reads as seen.
		return claimFlap, next
	}
	if next.At == nil {
		next.At = map[Status]int64{}
	}
	next.At[status] = now.UnixMilli()
	return claimed, next
}

// claimTicket is what a successful claim hands back, so a send that fails
// for a reason worth retrying can give the claim back (releaseClaim).
type claimTicket struct {
	pane   string
	status Status
	prev   notifyEntry // the entry before the claim
	had    bool
	mine   notifyEntry // the entry the claim wrote
}

// claimNotify decides, under the notify lock, whether this process sends the
// toast for pane's transition, and records the decision before returning —
// claim before send, so exactly one of several overlapping hooks toasts. It
// also clears this run's settling marker for status. An error means no toast.
func claimNotify(ctx context.Context, stateDir, pane string, seq uint64, status Status, now time.Time) (claim, claimTicket, error) {
	if !notifyDedupe {
		return claimed, claimTicket{}, nil
	}
	var c claim
	var tk claimTicket
	err := withNotifyState(ctx, stateDir, now, func(state map[string]notifyEntry) bool {
		prev, had := state[pane]
		var next notifyEntry
		c, next = decideClaim(prev, had, seq, status, now)
		if c == claimSeen {
			return false // nothing new to record; the marker is cleared on exit
		}
		delete(next.Settle, status)
		state[pane] = next
		tk = claimTicket{pane: pane, status: status, prev: prev.clone(), had: had, mine: next}
		return true
	})
	if err != nil {
		return 0, claimTicket{}, err
	}
	return c, tk, nil
}

// releaseClaim gives a claim back after a send that failed for a reason a
// later hook could get past (rate limited, busy, a socket error, the run
// running out of time), so the pane's next change to the same status is not
// swallowed by the flap guard as if this toast had shown.
//
// Only the claim's own fields go back — Seq, SeenAt and the toast time for
// its status — and only if no other hook has claimed since; the settle
// markers are left as they are now, since other runs own them.
//
// It runs even when the run's own ctx is done, on a short context of its own.
func releaseClaim(ctx context.Context, stateDir string, tk claimTicket, now time.Time) error {
	if !notifyDedupe || tk.pane == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	return withNotifyState(ctx, stateDir, now, func(state map[string]notifyEntry) bool {
		cur, ok := state[tk.pane]
		if !ok || cur.Seq != tk.mine.Seq || cur.SeenAt != tk.mine.SeenAt {
			return false
		}
		e := cur.clone()
		e.Seq, e.SeenAt = tk.prev.Seq, tk.prev.SeenAt
		if v, had := tk.prev.At[tk.status]; had {
			if e.At == nil {
				e.At = map[Status]int64{}
			}
			e.At[tk.status] = v
		} else {
			delete(e.At, tk.status)
		}
		if e.empty() {
			delete(state, tk.pane)
		} else {
			state[tk.pane] = e
		}
		return true
	})
}

// startSettle marks pane+status as being settled by this run, so at most one
// run per pane and status waits out the settle delay: a flapping pane cannot
// fill herdr's 32 plugin-command slots with sleepers. False means another run
// already holds it. The returned stamp identifies this run's marker.
func startSettle(ctx context.Context, stateDir, pane string, status Status, now time.Time) (bool, int64, error) {
	if !notifyDedupe {
		return true, 0, nil
	}
	until := now.Add(settleHold).UnixMilli()
	free := false
	err := withNotifyState(ctx, stateDir, now, func(state map[string]notifyEntry) bool {
		e := state[pane].clone()
		if held, ok := e.Settle[status]; ok {
			// Still held unless it expired, or lies further ahead than any
			// marker could (a clock stepped backwards).
			left := time.UnixMilli(held).Sub(now)
			if left > 0 && left <= settleHold {
				return false
			}
		}
		if e.Settle == nil {
			e.Settle = map[Status]int64{}
		}
		e.Settle[status] = until
		state[pane] = e
		free = true
		return true
	})
	return free, until, err
}

// endSettle clears this run's settling marker if it is still there. Best
// effort, on its own short context: a marker left behind expires anyway.
func endSettle(ctx context.Context, stateDir, pane string, status Status, stamp int64, now time.Time) {
	if !notifyDedupe {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	_ = withNotifyState(ctx, stateDir, now, func(state map[string]notifyEntry) bool {
		e, ok := state[pane]
		if !ok || e.Settle[status] != stamp {
			return false
		}
		e = e.clone()
		delete(e.Settle, status)
		if e.empty() {
			delete(state, pane)
		} else {
			state[pane] = e
		}
		return true
	})
}
