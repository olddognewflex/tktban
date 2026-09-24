package herdr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var stateNow = time.Unix(1_800_000_000, 0)

func msAgo(d time.Duration) int64 { return stateNow.Add(-d).UnixMilli() }

func TestNotifyStateCorruptFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, NotifyStateName)
	for _, body := range []string{"{not json", "null", `["a"]`, ""} {
		write(t, path, body)
		if got := loadNotifyState(path, stateNow); got == nil || len(got) != 0 {
			t.Errorf("%q: state = %v, want empty", body, got)
		}
	}
	if got := loadNotifyState(filepath.Join(dir, "missing.json"), stateNow); got == nil || len(got) != 0 {
		t.Errorf("missing file: state = %v, want empty", got)
	}
}

func TestNotifyStateAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, NotifyStateName)
	want := notifyEntry{Seq: 722, SeenAt: stateNow.UnixMilli(), At: map[Status]int64{StatusBlocked: stateNow.UnixMilli()}}
	if err := saveNotifyState(path, map[string]notifyEntry{"wD:p1": want}, stateNow); err != nil {
		t.Fatal(err)
	}
	got := loadNotifyState(path, stateNow)["wD:p1"]
	if got.Seq != want.Seq || got.SeenAt != want.SeenAt || got.At[StatusBlocked] != want.At[StatusBlocked] {
		t.Fatalf("round trip = %+v", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != NotifyStateName {
		t.Fatalf("dir holds %v, want only the state file (no temp left behind)", entries)
	}

	// A symlink at the path is replaced, never written through.
	outside := filepath.Join(t.TempDir(), "outside.json")
	write(t, outside, "keep")
	link := filepath.Join(t.TempDir(), NotifyStateName)
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := saveNotifyState(link, map[string]notifyEntry{"p": want}, stateNow); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "keep" {
		t.Fatalf("write followed the symlink: outside now %q", data)
	}
	if info, err := os.Lstat(link); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("state path is not a regular file after save: %v, %v", info, err)
	}
}

func TestNotifyStatePrunesOld(t *testing.T) {
	path := filepath.Join(t.TempDir(), NotifyStateName)
	state := map[string]notifyEntry{
		"old":   {Seq: 1, SeenAt: msAgo(25 * time.Hour), At: map[Status]int64{StatusDone: msAgo(25 * time.Hour)}},
		"fresh": {Seq: 2, SeenAt: msAgo(25 * time.Hour), At: map[Status]int64{StatusDone: msAgo(time.Hour)}},
	}
	if err := saveNotifyState(path, state, stateNow); err != nil {
		t.Fatal(err)
	}
	// Read the file itself: load prunes too and would hide a save that did not.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]notifyEntry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["old"]; ok || len(got) != 1 {
		t.Fatalf("file after prune = %s, want only fresh", raw)
	}
}

// The TTL applies on load too: an entry that went stale while nothing saved
// (the only pane that ever toasts, say) must not steer a decision.
func TestNotifyStatePrunesOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), NotifyStateName)
	if err := saveNotifyState(path, map[string]notifyEntry{
		"p": {Seq: 9, SeenAt: stateNow.UnixMilli()},
	}, stateNow); err != nil {
		t.Fatal(err)
	}
	if got := loadNotifyState(path, stateNow.Add(25*time.Hour)); len(got) != 0 {
		t.Fatalf("a day later the load still holds %v", got)
	}
}

// A crashed save's temp file is swept once it is old; a fresh one is left.
func TestNotifyStateSweepsStaleTemps(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, NotifyStateName+".123.tmp")
	fresh := filepath.Join(dir, NotifyStateName+".456.tmp")
	write(t, stale, "x")
	write(t, fresh, "x")
	old := stateNow.Add(-2 * time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, stateNow, stateNow); err != nil {
		t.Fatal(err)
	}
	if err := saveNotifyState(filepath.Join(dir, NotifyStateName), map[string]notifyEntry{}, stateNow); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temp survived (err=%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh temp removed: %v", err)
	}
}

func TestDecideClaim(t *testing.T) {
	at := func(s Status, ago time.Duration) map[Status]int64 { return map[Status]int64{s: msAgo(ago)} }
	cases := []struct {
		name   string
		prev   notifyEntry
		had    bool
		seq    uint64
		status Status
		want   claim
	}{
		{"first toast for the pane", notifyEntry{}, false, 722, StatusBlocked, claimed},
		{"same seq, just handled", notifyEntry{Seq: 722, SeenAt: msAgo(2 * time.Second), At: at(StatusBlocked, 2*time.Second)}, true, 722, StatusBlocked, claimSeen},
		{"lower seq, just handled", notifyEntry{Seq: 730, SeenAt: msAgo(time.Second), At: at(StatusDone, time.Hour)}, true, 722, StatusBlocked, claimSeen},
		// herdr restarted: its per-pane counter began again below the stored one.
		{"lower seq an hour later is a reset", notifyEntry{Seq: 730, SeenAt: msAgo(time.Hour), At: at(StatusBlocked, time.Hour)}, true, 5, StatusBlocked, claimed},
		// herdr can re-send a status event with no new transition; an equal
		// seq is handled however old.
		{"same seq an hour later", notifyEntry{Seq: 722, SeenAt: msAgo(time.Hour), At: at(StatusBlocked, time.Hour)}, true, 722, StatusBlocked, claimSeen},
		{"lower seq just inside the window", notifyEntry{Seq: 730, SeenAt: msAgo(59 * time.Second), At: at(StatusBlocked, 59*time.Second)}, true, 722, StatusBlocked, claimSeen},
		{"lower seq past the window", notifyEntry{Seq: 730, SeenAt: msAgo(61 * time.Second), At: at(StatusBlocked, 61*time.Second)}, true, 722, StatusBlocked, claimed},
		{"seen time in the future is a reset", notifyEntry{Seq: 730, SeenAt: msAgo(-time.Hour)}, true, 5, StatusBlocked, claimed},
		{"same status inside cooldown", notifyEntry{Seq: 722, SeenAt: msAgo(29 * time.Second), At: at(StatusBlocked, 29*time.Second)}, true, 730, StatusBlocked, claimFlap},
		{"same status after cooldown", notifyEntry{Seq: 722, SeenAt: msAgo(30 * time.Second), At: at(StatusBlocked, 30*time.Second)}, true, 730, StatusBlocked, claimed},
		{"other status inside cooldown", notifyEntry{Seq: 722, SeenAt: msAgo(time.Second), At: at(StatusBlocked, time.Second)}, true, 730, StatusDone, claimed},
		{"toast time in the future (clock stepped back)", notifyEntry{Seq: 722, SeenAt: msAgo(time.Hour), At: at(StatusBlocked, -time.Minute)}, true, 730, StatusBlocked, claimed},
		{"no seq from herdr: flap guard only", notifyEntry{SeenAt: msAgo(time.Second), At: at(StatusDone, time.Hour)}, true, 0, StatusBlocked, claimed},
		{"no seq, same status inside cooldown", notifyEntry{SeenAt: msAgo(time.Second), At: at(StatusBlocked, time.Second)}, true, 0, StatusBlocked, claimFlap},
	}
	for _, c := range cases {
		got, next := decideClaim(c.prev, c.had, c.seq, c.status, stateNow)
		if got != c.want {
			t.Errorf("%s: claim = %v, want %v", c.name, got, c.want)
			continue
		}
		switch got {
		case claimed:
			if next.Seq != c.seq || next.SeenAt != stateNow.UnixMilli() || next.At[c.status] != stateNow.UnixMilli() {
				t.Errorf("%s: recorded %+v", c.name, next)
			}
		case claimFlap:
			if next.Seq != c.seq || next.SeenAt != stateNow.UnixMilli() || next.At[c.status] != c.prev.At[c.status] {
				t.Errorf("%s: flap recorded %+v, want seq and seen time set, toast time unchanged", c.name, next)
			}
		}
	}
}

// Releasing a claim puts the entry back as it was, but never undoes a newer
// decision another hook wrote in between.
func TestReleaseClaimKeepsNewerDecision(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	c, tk, err := claimNotify(ctx, dir, "p", 722, StatusBlocked, stateNow)
	if err != nil || c != claimed {
		t.Fatalf("claim = %v, %v", c, err)
	}
	// Another hook, a moment later, handles a newer transition.
	later := stateNow.Add(40 * time.Second)
	if c, _, err := claimNotify(ctx, dir, "p", 730, StatusBlocked, later); err != nil || c != claimed {
		t.Fatalf("second claim = %v, %v", c, err)
	}
	if err := releaseClaim(ctx, dir, tk, later); err != nil {
		t.Fatal(err)
	}
	got := loadNotifyState(filepath.Join(dir, NotifyStateName), later)["p"]
	if got.Seq != 730 {
		t.Fatalf("release clobbered the newer claim: %+v", got)
	}
}

// Releasing a claim reverts only the claim's own fields. Settle markers
// belong to other runs: one written after the claim survives the release,
// and one that ended after the claim is not brought back by it.
func TestReleaseClaimLeavesSettleMarkers(t *testing.T) {
	ctx := context.Background()
	path := func(dir string) string { return filepath.Join(dir, NotifyStateName) }

	dir := t.TempDir()
	_, tk, err := claimNotify(ctx, dir, "p", 722, StatusBlocked, stateNow)
	if err != nil {
		t.Fatal(err)
	}
	free, _, err := startSettle(ctx, dir, "p", StatusDone, stateNow)
	if err != nil || !free {
		t.Fatalf("settle = %v, %v", free, err)
	}
	if err := releaseClaim(ctx, dir, tk, stateNow); err != nil {
		t.Fatal(err)
	}
	got := loadNotifyState(path(dir), stateNow)["p"]
	if _, ok := got.Settle[StatusDone]; !ok {
		t.Fatalf("release wiped a newer settle marker: %+v", got)
	}
	if _, ok := got.At[StatusBlocked]; ok || got.Seq != 0 || got.SeenAt != 0 {
		t.Fatalf("claim fields not reverted: %+v", got)
	}

	dir = t.TempDir()
	_, stamp, err := startSettle(ctx, dir, "p", StatusDone, stateNow)
	if err != nil {
		t.Fatal(err)
	}
	_, tk, err = claimNotify(ctx, dir, "p", 722, StatusBlocked, stateNow)
	if err != nil {
		t.Fatal(err)
	}
	endSettle(ctx, dir, "p", StatusDone, stamp, stateNow)
	if err := releaseClaim(ctx, dir, tk, stateNow); err != nil {
		t.Fatal(err)
	}
	if got := loadNotifyState(path(dir), stateNow)["p"]; len(got.Settle) != 0 {
		t.Fatalf("release resurrected an ended settle marker: %+v", got)
	}
}
