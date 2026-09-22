//go:build !windows

package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNotifyLockDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	lock := filepath.Join(dir, NotifyLockName)
	if err := os.Symlink(outside, lock); err != nil {
		t.Fatal(err)
	}
	if unlock, err := lockNotify(context.Background(), lock); err == nil {
		unlock()
		t.Fatal("locked through a symlink")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("lock followed the symlink and created %s (err=%v)", outside, err)
	}
	// And a claim over that state dir refuses rather than toasting unguarded.
	if _, err := claimNotify(context.Background(), dir, "p", 1, StatusBlocked, time.Now()); err == nil {
		t.Fatal("claim succeeded without the lock")
	}
}

func TestNotifyLockTimeoutRespectsCtx(t *testing.T) {
	t.Parallel() // waits on its own deadline; nothing shared
	path := filepath.Join(t.TempDir(), NotifyLockName)
	unlock, err := lockNotify(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		u, err := lockNotify(ctx, path)
		if err == nil {
			u()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("held lock: err = %v, want deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lock wait outlived its context")
	}

	unlock()
	again, err := lockNotify(context.Background(), path)
	if err != nil {
		t.Fatalf("lock not free after release: %v", err)
	}
	again()
}
