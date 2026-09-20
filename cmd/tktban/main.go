// Command tktban is a terminal kanban board for tkt. `tktban` (or `tktban board`)
// launches the TUI; `tktban doctor` validates setup. It speaks only the tkt CLI
// verb contract — see the internal/tkt package. `--herdr` adapts it to run as a
// herdr plugin pane (see herdr-plugin.toml and the internal/herdr package).
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/settings"
	"github.com/olddognewflex/tktban/internal/tkt"
	"github.com/olddognewflex/tktban/internal/ui"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	fs := flag.NewFlagSet("tktban", flag.ContinueOnError)
	config := fs.String("config", "", "path to .sdlc/config.toml (else tkt auto-discovers)")
	interval := fs.Float64("refresh-interval", 10.0, "auto-refresh cadence in seconds (must be > 0)")
	noAuto := fs.Bool("no-auto-refresh", false, "start with auto-refresh off (toggle at runtime with 'a')")
	inHerdr := fs.Bool("herdr", false, "running as a herdr plugin pane: pick config from the herdr context, keep settings in the plugin state dir (no-op outside herdr)")
	noLive := fs.Bool("no-herdr-live", false, "inside herdr, don't read live agent status from the herdr socket for card badges")
	selectKey := fs.String("select", "", "select this ticket key when the board opens, e.g. TKB-23")
	selectFromCwd := fs.Bool("select-from-cwd", false, "select the ticket named by the branch checked out where the board was opened (herdr context, else this directory); --select wins")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: tktban [flags] [board|doctor]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if *interval <= 0 {
		fmt.Fprintln(os.Stderr, "tktban: --refresh-interval must be > 0")
		return 2
	}

	command := "board"
	if rest := fs.Args(); len(rest) > 0 {
		command = rest[0]
	}

	cwd, _ := os.Getwd()
	cfg, settingsPath, stateDir := *config, "", ""
	if *inHerdr {
		cfg, settingsPath, stateDir = herdrSetup(cfg, os.Getenv, cwd, os.Stat, os.Lstat)
	}
	selected := selectTarget(*selectKey, *selectFromCwd, os.Getenv, cwd)

	tk := tkt.New(cfg, "")

	switch command {
	case "doctor":
		return doctor(tk)
	case "board":
		// Seed only when the board runs, so `--herdr doctor` never writes. A
		// failed seed just means default settings, which Load already handles.
		_ = herdr.SeedSettings(settingsPath, settings.DefaultPath())
		// Held for the board's lifetime so the launcher can tell our popup
		// from another plugin's before closing it.
		release := herdr.HoldBoardLock(stateDir)
		defer release()
		// --herdr is only ever passed by the manifest's popup pane, so it is
		// also the board's "you are the popup" signal for a jump.
		return board(tk, *interval, !*noAuto, settingsPath, liveSource(os.Getenv, *noLive), selected, *inHerdr)
	default:
		fmt.Fprintf(os.Stderr, "tktban: unknown command %q (want board or doctor)\n", command)
		return 2
	}
}

func doctor(tk *tkt.Tkt) int {
	okAll := true
	for _, c := range tk.Doctor() {
		mark := "ok  "
		if !c.OK {
			mark = "FAIL"
		}
		line := fmt.Sprintf("[%s] %s", mark, c.Name)
		if c.Detail != "" {
			line += " — " + c.Detail
		}
		fmt.Println(line)
		okAll = okAll && c.OK
	}
	if !okAll {
		fmt.Println("\ntktban needs every check above to pass. " +
			"The board reads all tickets via a `[queries].all` query.")
		return 1
	}
	return 0
}

// herdrSetup applies herdr plugin mode and returns the tkt config path, the
// settings path ("" means the standalone default for each) and the plugin state
// dir ("" outside herdr). An explicit --config always wins; else the config is
// found from the herdr context (focused pane, then workspace, then cwd).
// Settings move to the plugin state dir unless that path is a symlink. Outside
// herdr (HERDR_ENV unset) nothing changes.
func herdrSetup(config string, getenv func(string) string, cwd string, stat, lstat func(string) (fs.FileInfo, error)) (string, string, string) {
	e := herdr.FromEnv(getenv)
	if !e.InHerdr {
		return config, "", ""
	}
	if config == "" {
		config = herdr.ResolveConfig(e, cwd, stat)
	}
	return config, herdr.SafeSettingsPath(herdr.SettingsPath(e), lstat), e.StateDir
}

// liveSource returns the herdr socket as the board's live agent status source
// when running inside herdr, whether or not --herdr was given (a board in a
// plain herdr pane benefits too). Outside herdr, with no socket, or with
// --no-herdr-live it returns nil and badges come from ticket frontmatter only.
func liveSource(getenv func(string) string, disabled bool) ui.LiveSource {
	if disabled {
		return nil
	}
	e := herdr.FromEnv(getenv)
	if !e.InHerdr || e.SocketPath == "" {
		return nil
	}
	return herdr.NewSocketSource(e.SocketPath)
}

// selectKeyTimeout bounds reading the branch of the directories --select-from-cwd
// looks at. Only a reftable repo runs git at all, but the board must open
// either way.
const selectKeyTimeout = 2 * time.Second

// selectTarget is the ticket key the board should open on. An explicit
// --select always wins; --select-from-cwd otherwise reads the branch checked
// out where the board was opened — inside herdr that is the invoking pane's
// directory (the action runs with its context), which is how prefix+t from an
// agent pane opens the board on that pane's ticket. "" means "no preselection".
func selectTarget(explicit string, fromCwd bool, getenv func(string) string, cwd string) string {
	if explicit != "" {
		return explicit
	}
	if !fromCwd {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), selectKeyTimeout)
	defer cancel()
	return herdr.SelectKey(herdr.FromEnv(getenv), cwd, func(dir string) []string {
		return herdr.KeysForDir(ctx, dir)
	})
}

func board(tk *tkt.Tkt, interval float64, auto bool, settingsPath string, live ui.LiveSource, selectKey string, popup bool) int {
	m := ui.New(tk, interval, auto, settingsPath).WithLive(live).WithSelect(selectKey).WithPopup(popup)
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tktban:", err)
		return 1
	}
	return 0
}
