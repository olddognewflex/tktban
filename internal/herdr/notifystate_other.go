//go:build windows

package herdr

import (
	"context"
	"errors"
)

// notifyDedupe is off where flock is unavailable: every hook that gets this
// far toasts. The plugin manifest only targets macOS and Linux.
const notifyDedupe = false

func lockNotify(context.Context, string) (func(), error) {
	return nil, errors.New("herdr: notify lock unsupported on this platform")
}
