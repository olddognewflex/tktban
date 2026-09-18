# tktban

A terminal **kanban board for tkt**, built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

tktban renders a tkt board as columns and lets you move, comment on, and create
tickets — all through tkt verbs. It speaks **only the tkt CLI verb contract**
(shell-out, `--json` in, `--json` out): it never imports tkt's internals and never
reads a backend's storage directly. The payoff is that the same TUI works against
*any* backend tkt supports — point it at a markdown board or a Jira config and it
behaves identically, because all it knows are roles, lanes, and the ticket shape.

## Install

```sh
go install github.com/olddognewflex/tktban/cmd/tktban@latest   # or: go build -o tktban ./cmd/tktban
```

Requires Go ≥ 1.26 and `tkt` on your `PATH` (or set `TKT_BIN=/path/to/tkt`).

## The one prerequisite: an `all` query

A board shows *every* ticket, but `tkt list` always needs a named query and ships
none that returns everything. tktban standardizes on a query named **`all`**. Add
this to your project's `.sdlc/config.toml`:

```toml
[queries]
all = 'ORDER BY key ASC'   # empty filter -> tkt returns every ticket
```

Run `tktban doctor` to check your setup:

```sh
tktban doctor              # verifies: tkt on PATH, board.roles readable, `all` query present
```

## Usage

```sh
tktban                     # launch the board (auto-discovers .sdlc/config.toml)
tktban --config path/to/.sdlc/config.toml
```

### Keys

| Key | Action |
|-----|--------|
| `r` | Refresh the board |
| `m` / `enter` | Move the selected card to another lane (`tkt transition`) |
| `c` | Comment on the selected card (`tkt comment`) |
| `n` | Create a new ticket (`tkt create`) |
| `q` | Quit |
| `tab` / arrows | Move focus between columns and cards |

Columns come from `[board.roles]` (in config order); cards are grouped by their
canonical `status_role` and sorted by priority then key. A `⛔N` badge shows
unresolved blocker count. Tickets in an unconfigured lane appear in a trailing
`(unmapped)` column rather than being dropped.

## Run inside herdr

tktban ships a [herdr](https://herdr.dev) plugin manifest (`herdr-plugin.toml`),
so the board opens as an overlay over any herdr workspace.

```sh
herdr plugin install olddognewflex/tktban      # clones and builds bin/tktban (needs Go)

# local development: link the checkout; link does not run the build step
go build -o bin/tktban ./cmd/tktban
herdr plugin link .
```

Bind the action to a key in `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+t"   # prefix+b is herdr's sidebar toggle
type = "plugin_action"
command = "odnf.tktban.open-board"
```

The key opens the board, focuses it if it is open but not focused, and closes it
if it is focused. Toggling needs `python3` on herdr's PATH; without it every
press opens another board. The board reads the tkt config found from the focused pane's
directory, falling back to the workspace root, so each project shows its own
board.

Under herdr the pane runs `tktban --herdr`, which:

- finds `.sdlc/config.toml` from the herdr context when `--config` is not given;
- keeps UI settings in the plugin state dir
  (`~/.local/state/herdr/plugins/odnf.tktban/settings.toml`), copied once from
  your standalone settings so the theme carries over.

Outside herdr the flag does nothing. For publishing, tag the GitHub repo with
the `herdr-plugin` topic so it shows in the herdr plugin marketplace.
[docs/herdr-events.md](docs/herdr-events.md) records which herdr events a
future live-status feature can hook.

## How it talks to tkt

`internal/tkt/tkt.go` is the entire coupling surface — a thin subprocess wrapper:

| tktban call | tkt verb |
|-------------|----------|
| read columns | `tkt cfg board.roles --json` |
| read tickets | `tkt list --query all --json` |
| move | `tkt transition KEY ROLE` |
| comment | `tkt comment KEY BODY` |
| create | `tkt create --type T --summary S [...] --json` |

Config is passed via the `TKT_CONFIG` env var (tkt's global `--config` placed
*before* a verb is clobbered by an argparse quirk; the env var is reliable for
every verb).

## Development

```sh
go build ./...
go test ./...
```

Tests cover the wrapper (subprocess faked via an injected runner), the board model
(pure grouping/sort), settings persistence, and the UI model/modals (rendered to
strings and asserted).
