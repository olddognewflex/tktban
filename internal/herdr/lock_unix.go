//go:build !windows

package herdr

import (
	"os"
	"path/filepath"
	"syscall"
)

// HoldBoardLock takes a non-blocking exclusive flock on the board lock for the
// life of the process and returns a release func. It never fails the board: if
// the lock cannot be taken (no state dir, another board already holds it, an
// I/O error) it returns a no-op release, and the launcher simply treats the
// popup as tktban's only when some board holds the lock. The file is opened
// O_NOFOLLOW so a symlink planted at the lock path is not followed.
func HoldBoardLock(stateDir string) func() {
	path := LockPath(stateDir)
	if path == "" {
		return func() {}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return func() {}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return func() {}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return func() {}
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}
