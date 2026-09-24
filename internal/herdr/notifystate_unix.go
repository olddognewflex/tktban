//go:build !windows

package herdr

import (
	"context"
	"os"
	"syscall"
	"time"
)

// notifyDedupe is on wherever flock is available.
const notifyDedupe = true

// lockRetry is how often lockNotify retries a lock another hook holds. The
// critical section is one small file read and write, so waits are short.
const lockRetry = 10 * time.Millisecond

// lockNotify takes an exclusive flock on path, retrying a held lock every
// lockRetry until ctx ends. The file is opened O_NOFOLLOW, like the board
// lock, so a symlink planted at the path is refused rather than followed.
func lockNotify(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EINTR {
			f.Close()
			return nil, err
		}
		t := time.NewTimer(lockRetry)
		select {
		case <-ctx.Done():
			t.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}
