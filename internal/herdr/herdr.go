// Package herdr adapts tktban to running as a herdr plugin pane.
//
// Plugin setup reads only the environment herdr injects (HERDR_ENV,
// HERDR_PLUGIN_*, HERDR_SOCKET_PATH) and is pure over an injected getenv/stat so
// it tests without a running herdr. Live agent status (client.go, live.go) is
// the one part that talks to the herdr socket. Outside herdr every function
// degrades to the standalone behaviour: no config override, the normal
// settings path, no live status.
package herdr

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Env var names herdr sets for plugin processes.
const (
	envHerdr    = "HERDR_ENV"                 // "1" inside any herdr-managed pane
	envStateDir = "HERDR_PLUGIN_STATE_DIR"    // per-plugin writable state dir
	envContext  = "HERDR_PLUGIN_CONTEXT_JSON" // invocation context (actions, panes)
	envSocket   = "HERDR_SOCKET_PATH"         // unix socket of the herdr server API
)

// configRel is the tkt config path tkt itself auto-discovers.
var configRel = filepath.Join(".sdlc", "config.toml")

// Context is the subset of herdr's plugin invocation context tktban uses. The
// JSON is flat; unknown fields are ignored.
type Context struct {
	WorkspaceID    string `json:"workspace_id"`
	WorkspaceCwd   string `json:"workspace_cwd"`
	FocusedPaneCwd string `json:"focused_pane_cwd"`
}

// Env is what tktban learns from the process environment when started by herdr.
type Env struct {
	InHerdr    bool    // HERDR_ENV == "1"
	StateDir   string  // HERDR_PLUGIN_STATE_DIR, "" when unset
	SocketPath string  // HERDR_SOCKET_PATH, "" when unset
	Context    Context // zero when HERDR_PLUGIN_CONTEXT_JSON is unset or invalid
}

// FromEnv reads the herdr environment. A missing or malformed context is not an
// error: the pane still works, it just falls back to its working directory.
func FromEnv(getenv func(string) string) Env {
	e := Env{
		InHerdr:    getenv(envHerdr) == "1",
		StateDir:   getenv(envStateDir),
		SocketPath: getenv(envSocket),
	}
	if raw := getenv(envContext); raw != "" {
		var c Context
		if json.Unmarshal([]byte(raw), &c) == nil {
			e.Context = c
		}
	}
	return e
}

// statFunc matches os.Stat so tests can fake the filesystem.
type statFunc func(string) (fs.FileInfo, error)

// FindConfig walks up from start looking for .sdlc/config.toml, the same file
// tkt would auto-discover from that directory. Returns "" when none is found.
func FindConfig(start string, stat statFunc) string {
	if start == "" {
		return ""
	}
	dir := filepath.Clean(start)
	for {
		candidate := filepath.Join(dir, configRel)
		if info, err := stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ResolveConfig picks the tkt config for a board opened inside herdr. The
// focused pane's directory wins over the workspace root because it is the more
// specific signal (a workspace can hold several repos); the process working
// directory is the last resort. Returns "" to leave tkt's own discovery in charge.
func ResolveConfig(e Env, cwd string, stat statFunc) string {
	for _, start := range []string{e.Context.FocusedPaneCwd, e.Context.WorkspaceCwd, cwd} {
		if found := FindConfig(start, stat); found != "" {
			return found
		}
	}
	return ""
}

// SettingsPath returns where tktban keeps its UI settings in herdr mode: the
// plugin's own state dir, so the plugin never writes outside what herdr gave it.
// Returns "" when there is no state dir, meaning "use the standalone default".
func SettingsPath(e Env) string {
	if !e.InHerdr || e.StateDir == "" {
		return ""
	}
	return filepath.Join(e.StateDir, "settings.toml")
}

// SafeSettingsPath rejects a plugin settings path that is a symlink, returning
// "" so the caller falls back to the standalone settings file. tktban only ever
// creates a regular file there, and settings saves follow symlinks, so a link
// planted in the state dir could otherwise redirect writes outside it. A path
// that does not exist yet is fine: the seed or first save creates it.
func SafeSettingsPath(path string, lstat statFunc) string {
	if path == "" {
		return ""
	}
	if info, err := lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return ""
	}
	return path
}

// SeedSettings copies the standalone settings file to the plugin path the first
// time the plugin runs, so a theme or hidden-column choice carries over. It never
// overwrites an existing plugin file (the create is exclusive, which also
// refuses to follow a symlink left at that path), and a missing source is not
// an error.
func SeedSettings(pluginPath, standalonePath string) error {
	if pluginPath == "" || standalonePath == "" || pluginPath == standalonePath {
		return nil
	}
	if _, err := os.Lstat(pluginPath); err == nil {
		return nil
	}
	data, err := os.ReadFile(standalonePath)
	if err != nil {
		return nil // nothing to seed from; defaults apply
	}
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(pluginPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil // another pane seeded it first
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(pluginPath) // don't leave a truncated file behind
		return err
	}
	return f.Close()
}
