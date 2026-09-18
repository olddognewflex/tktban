package herdr

import "path/filepath"

// LockName is the board lock file inside HERDR_PLUGIN_STATE_DIR. A running
// board holds an exclusive flock on it; scripts/open-board.sh tests the lock to
// tell the tktban popup apart from another plugin's popup before closing it.
const LockName = "board.lock"

// LockPath is the board lock path for a plugin state dir, "" without one.
func LockPath(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, LockName)
}
