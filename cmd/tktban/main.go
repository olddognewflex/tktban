// Command tktban is a terminal kanban board for tkt. `tktban` (or `tktban board`)
// launches the TUI; `tktban doctor` validates setup. It speaks only the tkt CLI
// verb contract — see the internal/tkt package.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

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

	tk := tkt.New(*config, "")

	switch command {
	case "doctor":
		return doctor(tk)
	case "board":
		return board(tk, *interval, !*noAuto)
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

func board(tk *tkt.Tkt, interval float64, auto bool) int {
	m := ui.New(tk, interval, auto, "")
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tktban:", err)
		return 1
	}
	return 0
}
