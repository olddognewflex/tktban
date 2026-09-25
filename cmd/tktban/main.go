// Command tktban is a terminal kanban board for tkt. `tktban` (or `tktban board`)
// launches the TUI; `tktban doctor` validates setup; `tktban herdr-hook` is the
// herdr event hook that toasts when a ticket's agent needs a human. It speaks only the tkt CLI
// verb contract — see the internal/tkt package. `--herdr` adapts it to run as a
// herdr plugin pane (see herdr-plugin.toml and the internal/herdr package).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
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
	noTokens := fs.Bool("no-herdr-tokens", false, "inside herdr, don't publish the ticket key as herdr pane metadata")
	noDispatch := fs.Bool("no-herdr-dispatch", false, "inside herdr, turn the D dispatch key off whatever the dispatch setting says")
	selectKey := fs.String("select", "", "select this ticket key when the board opens, e.g. TKB-23")
	selectFromCwd := fs.Bool("select-from-cwd", false, "select the ticket named by the branch checked out where the board was opened (the herdr context inside herdr, else this directory); --select wins")
	popup := fs.Bool("popup", false, "(internal) the board is herdr's popup pane: a successful jump to an agent pane exits, which is what closes the popup")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: tktban [flags] [board|doctor|herdr-hook]")
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

	// The hook is its own world: herdr runs it once per status event, so it
	// touches no tkt, no board settings seed and no board lock, and it never
	// fails (a non-zero hook exit only clutters herdr's plugin log).
	if command == "herdr-hook" {
		return herdrHook(os.Getenv, hookOut)
	}

	cwd, _ := os.Getwd()
	cfg, settingsPath, stateDir := *config, "", ""
	if *inHerdr {
		cfg, settingsPath, stateDir = herdrSetup(cfg, os.Getenv, cwd, os.Stat, os.Lstat)
	}

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
		// Only the board selects a ticket, so `doctor` never reads a branch.
		selected, derive := selectTarget(*selectKey, *selectFromCwd, os.Getenv, cwd)
		opts := herdrOpts{off: *noLive, noTokens: *noTokens, noDispatch: *noDispatch}
		live := liveSource(os.Getenv, opts)
		return board(tk, *interval, !*noAuto, settingsPath, live,
			dispatchDir(os.Getenv, cwd, settingsPath, opts), selected, derive, *popup)
	default:
		fmt.Fprintf(os.Stderr, "tktban: unknown command %q (want board, doctor or herdr-hook)\n", command)
		return 2
	}
}

// doctor is a variable for the same reason board is: a test can check how a
// command line is routed without shelling out to tkt.
var doctor = func(tk *tkt.Tkt) int {
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

// herdrOpts carries the herdr opt-outs. They are a struct, not three bools:
// all are negative, they sit next to each other, and swapping them would
// compile and silently invert the pair.
type herdrOpts struct {
	off        bool // --no-herdr-live: no socket source at all
	noTokens   bool // --no-herdr-tokens: poll, but report nothing back
	noDispatch bool // --no-herdr-dispatch: no D key, whatever the setting says
}

// liveSource returns the herdr socket as the board's live agent status source
// when running inside herdr, whether or not --herdr was given (a board in a
// plain herdr pane benefits too). Outside herdr, with no socket, or with
// --no-herdr-live it returns nil and badges come from ticket frontmatter only.
//
// Reporting the pane's ticket key back to herdr as pane metadata rides on the
// same source, so --no-herdr-live turns that off too: without polls there is
// nothing to report.
func liveSource(getenv func(string) string, opt herdrOpts) ui.LiveSource {
	if opt.off {
		return nil
	}
	e := herdr.FromEnv(getenv)
	if !e.InHerdr || e.SocketPath == "" {
		return nil
	}
	src := herdr.NewSocketSource(e.SocketPath)
	src.Tokens = !opt.noTokens
	return src
}

// dispatchDir says which checkout the board's D key dispatches in, or "" to
// leave the key off.
//
// Three things have to agree before a board may cut a branch and start an
// agent: the opt-out flag is absent, the dispatch setting was turned on by
// hand (it defaults to false and the board never writes it, exactly like
// notify), and a directory resolved that is certainly the repo the person
// means. herdr.DispatchDir refuses rather than guess from the process working
// directory inside herdr, because a plugin pane started without --cwd runs in
// tktban's own install checkout — and two repos here share one board, so a
// guess could branch the wrong repository for a real ticket.
//
// settingsPath is "" outside --herdr, meaning the standalone file.
func dispatchDir(getenv func(string) string, cwd, settingsPath string, opt herdrOpts) string {
	if opt.noDispatch {
		return ""
	}
	if settingsPath == "" {
		settingsPath = settings.DefaultPath()
	}
	if on, _ := settings.Load(settingsPath)["dispatch"].(bool); !on {
		return ""
	}
	dir, ok := herdr.DispatchDir(herdr.FromEnv(getenv), cwd)
	if !ok {
		return ""
	}
	return dir
}

// selectKeyTimeout bounds reading the branch of the directories --select-from-cwd
// looks at. Only a reftable repo runs git at all, but the board must open
// either way.
const selectKeyTimeout = 2 * time.Second

// selectTarget says which ticket the board should open on: a key to use
// straight away, and a function to work one out when it has to be derived.
// An explicit --select wins and needs no derivation. --select-from-cwd reads
// the branch checked out where the board was opened, which inside herdr is
// the invoking pane's directory — that is how the board key pressed in an
// agent pane opens the board on that pane's ticket. The reading is handed
// back as a function because it touches the filesystem, and only a git
// subprocess honours a deadline: the board runs it as a command so a hung
// mount delays the selection rather than the first paint.
func selectTarget(explicit string, fromCwd bool, getenv func(string) string, cwd string) (string, func() string) {
	if explicit != "" || !fromCwd {
		return explicit, nil
	}
	return "", func() string {
		ctx, cancel := context.WithTimeout(context.Background(), selectKeyTimeout)
		defer cancel()
		return herdr.SelectKey(herdr.FromEnv(getenv), cwd, func(dir string) []string {
			return herdr.KeysForDir(ctx, dir)
		})
	}
}

// board runs the TUI. A variable so a test can check the wiring from argv
// without a terminal.
var board = func(tk *tkt.Tkt, interval float64, auto bool, settingsPath string, live ui.LiveSource, dispatchDir string, selectKey string, derive func() string, popup bool) int {
	m := ui.New(tk, interval, auto, settingsPath).
		// A non-empty settingsPath is herdr's plugin settings file and nothing
		// else: run() fills it in only under --herdr, and only when
		// SafeSettingsPath accepted it. That is exactly the file the
		// notification hook reads, which is what the board needs to know.
		WithPluginSettings(settingsPath != "").
		WithLive(live).
		// "" leaves the D key refusing: see dispatchDir.
		WithDispatch(dispatchDir).
		WithSelect(selectKey).
		WithSelectFunc(derive).
		WithPopup(popup)
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tktban:", err)
		return 1
	}
	return 0
}

// hookTimeout bounds the hook's own work. herdr sets no timeout of its own
// and drops runs past 32 in flight, so a hung socket or filesystem must not
// pin a slot. The slowest honest run is the 1 s settle delay plus two 1.1 s
// rate-limit retries. Two state-file cleanups run after it on up to 1 s each
// (giving a claim back, clearing the settle marker), so a run ends within
// about 7 s.
const hookTimeout = 5 * time.Second

// hookOut is where the hook's decision line goes: stdout, which herdr keeps
// in its plugin log. A variable so a test can read it.
var hookOut io.Writer = os.Stdout

// hookSleep is the hook's wait; a variable so a test of the routing does not
// spend herdr's 1 s settle delay on a real clock.
var hookSleep = herdr.SleepCtx

// herdrHook runs the herdr event hook (herdr-plugin.toml's [[events]]). It is
// a no-op, printing nothing, unless it is inside herdr with a socket, a
// plugin state dir and a status event to handle; otherwise it prints the
// one-line decision herdr keeps in its plugin log. It always exits 0.
//
// This is the only path in tktban that can show a herdr notification: the
// board never toasts, standalone or not.
var herdrHook = func(getenv func(string) string, out io.Writer) int {
	e := herdr.FromEnv(getenv)
	event, payload := getenv("HERDR_PLUGIN_EVENT"), getenv("HERDR_PLUGIN_EVENT_JSON")
	if !e.InHerdr || e.SocketPath == "" || e.StateDir == "" || event != herdr.StatusEventName || payload == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	fmt.Fprintln(out, herdr.RunHook(ctx, herdr.HookDeps{
		Event:     event,
		EventJSON: payload,
		StateDir:  e.StateDir,
		Client:    herdr.NewClient(e.SocketPath),
		Sleep:     hookSleep,
	}))
	return 0
}
