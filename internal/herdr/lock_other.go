//go:build windows

package herdr

// HoldBoardLock is a no-op where flock is unavailable; the plugin manifest
// only targets macOS and Linux.
func HoldBoardLock(stateDir string) func() { return func() {} }
