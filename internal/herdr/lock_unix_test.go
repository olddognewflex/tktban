//go:build !windows

package herdr

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// held reports whether some other open file description holds the lock.
func held(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	return err == syscall.EWOULDBLOCK
}

func TestHoldBoardLockHoldsUntilRelease(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	release := HoldBoardLock(dir)
	path := filepath.Join(dir, LockName)
	if !held(t, path) {
		t.Fatal("lock should be held while the board runs")
	}
	release()
	if held(t, path) {
		t.Fatal("lock should be free after release")
	}
}

func TestHoldBoardLockSecondHolderIsNoop(t *testing.T) {
	dir := t.TempDir()
	first := HoldBoardLock(dir)
	defer first()
	second := HoldBoardLock(dir) // must not block or panic
	second()
	if !held(t, filepath.Join(dir, LockName)) {
		t.Fatal("releasing the no-op second holder must not free the first lock")
	}
}

func TestHoldBoardLockNoStateDir(t *testing.T) {
	HoldBoardLock("")() // no-op, no panic
}

func TestHoldBoardLockDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	if err := os.Symlink(outside, filepath.Join(dir, LockName)); err != nil {
		t.Fatal(err)
	}
	HoldBoardLock(dir)()
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("lock followed the symlink and created %s (err=%v)", outside, err)
	}
}
