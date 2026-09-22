package herdr

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNotifyStateCorruptFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, NotifyStateName)
	for _, body := range []string{"{not json", "null", `["a"]`, ""} {
		write(t, path, body)
		if got := loadNotifyState(path); got == nil || len(got) != 0 {
			t.Errorf("%q: state = %v, want empty", body, got)
		}
	}
	if got := loadNotifyState(filepath.Join(dir, "missing.json")); got == nil || len(got) != 0 {
		t.Errorf("missing file: state = %v, want empty", got)
	}
}

func TestNotifyStateAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, NotifyStateName)
	now := time.Unix(1_800_000_000, 0)
	want := map[string]notifyEntry{"wD:p1": {Seq: 722, Status: StatusBlocked, AtUnixMs: now.UnixMilli()}}
	if err := saveNotifyState(path, want, now); err != nil {
		t.Fatal(err)
	}
	if got := loadNotifyState(path); len(got) != 1 || got["wD:p1"] != want["wD:p1"] {
		t.Fatalf("round trip = %v", got)
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
	if err := saveNotifyState(link, want, now); err != nil {
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
	now := time.Unix(1_800_000_000, 0)
	state := map[string]notifyEntry{
		"old":   {Seq: 1, Status: StatusDone, AtUnixMs: now.Add(-25 * time.Hour).UnixMilli()},
		"fresh": {Seq: 2, Status: StatusDone, AtUnixMs: now.Add(-time.Hour).UnixMilli()},
	}
	if err := saveNotifyState(path, state, now); err != nil {
		t.Fatal(err)
	}
	got := loadNotifyState(path)
	if _, ok := got["old"]; ok || len(got) != 1 {
		t.Fatalf("state after prune = %v, want only fresh", got)
	}
}

func TestDecideClaim(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(ago time.Duration) int64 { return now.Add(-ago).UnixMilli() }
	cases := []struct {
		name   string
		prev   notifyEntry
		had    bool
		seq    uint64
		status Status
		want   claim
	}{
		{"first toast for the pane", notifyEntry{}, false, 722, StatusBlocked, claimed},
		{"same seq", notifyEntry{Seq: 722, Status: StatusBlocked, AtUnixMs: at(time.Hour)}, true, 722, StatusBlocked, claimSeen},
		{"older seq", notifyEntry{Seq: 730, Status: StatusDone, AtUnixMs: at(time.Hour)}, true, 722, StatusBlocked, claimSeen},
		{"same status inside cooldown", notifyEntry{Seq: 722, Status: StatusBlocked, AtUnixMs: at(29 * time.Second)}, true, 730, StatusBlocked, claimFlap},
		{"same status after cooldown", notifyEntry{Seq: 722, Status: StatusBlocked, AtUnixMs: at(30 * time.Second)}, true, 730, StatusBlocked, claimed},
		{"other status inside cooldown", notifyEntry{Seq: 722, Status: StatusBlocked, AtUnixMs: at(time.Second)}, true, 730, StatusDone, claimed},
		{"no seq from herdr: flap guard only", notifyEntry{Seq: 0, Status: StatusDone, AtUnixMs: at(time.Hour)}, true, 0, StatusBlocked, claimed},
		{"no seq, same status inside cooldown", notifyEntry{Seq: 0, Status: StatusBlocked, AtUnixMs: at(time.Second)}, true, 0, StatusBlocked, claimFlap},
	}
	for _, c := range cases {
		got, next := decideClaim(c.prev, c.had, c.seq, c.status, now)
		if got != c.want {
			t.Errorf("%s: claim = %v, want %v", c.name, got, c.want)
		}
		if got == claimed && (next.Seq != c.seq || next.Status != c.status || next.AtUnixMs != now.UnixMilli()) {
			t.Errorf("%s: recorded %+v", c.name, next)
		}
		if got == claimFlap && (next.AtUnixMs != c.prev.AtUnixMs || next.Seq < c.seq) {
			t.Errorf("%s: flap recorded %+v, want seq kept and time unchanged", c.name, next)
		}
	}
}
