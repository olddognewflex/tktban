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
| `o` / `ga` | Focus the herdr pane running this card's agent (inside herdr) |
| `q` | Quit |
| `tab` / arrows | Move focus between columns and cards |

Columns come from `[board.roles]` (in config order); cards are grouped by their
canonical `status_role` and sorted by priority then key. A `⛔N` badge shows
unresolved blocker count. Tickets in an unconfigured lane appear in a trailing
`(unmapped)` column rather than being dropped.

## Run inside herdr

tktban ships a [herdr](https://herdr.dev) plugin manifest (`herdr-plugin.toml`),
so the board opens as a full-screen popup over any herdr workspace.

```sh
herdr plugin install olddognewflex/tktban      # clones and builds bin/tktban (needs Go)

# local development: link the checkout; link does not run the build step
go build -o bin/tktban ./cmd/tktban
herdr plugin link .
```

**A linked checkout is the live plugin.** `herdr plugin link .` registers the
working tree itself, and herdr runs `bin/tktban` from it — the binary you
built, not the code you have since edited. So **rebuild after every change**:

```sh
go build -o bin/tktban ./cmd/tktban
```

The giveaway that you forgot is a popup that opens and vanishes at once,
exiting with status 2: the manifest passes a flag the stale binary does not
know, so it fails to parse its own command line. `herdr plugin log list` shows
the usage text.

Bind the action to a key in `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+t"   # prefix+b is herdr's sidebar toggle
type = "plugin_action"
command = "odnf.tktban.open-board"
```

The key opens the board and, pressed again, closes it (so does `q`). herdr
allows one popup at a time; if another plugin's popup is open, the key leaves it
alone. Toggling needs `python3` on herdr's PATH; without it the key only opens. The board reads the tkt config found from the focused pane's
directory, falling back to the workspace root, so each project shows its own
board.

Under herdr the pane runs `tktban --herdr`, which:

- finds `.sdlc/config.toml` from the herdr context when `--config` is not given;
- keeps UI settings in the plugin state dir
  (`~/.local/state/herdr/plugins/odnf.tktban/settings.toml`), copied once from
  your standalone settings so the theme carries over. If that path is a
  symlink it is ignored and the standalone settings file is used instead;
- holds a lock on `board.lock` in the same dir while running, which is how the
  launcher knows the open popup is the board.

Outside herdr the flag does nothing. For publishing, tag the GitHub repo with
the `herdr-plugin` topic so it shows in the herdr plugin marketplace.
[docs/herdr-events.md](docs/herdr-events.md) records which herdr events the
live status below and future hooks can use.

### Live agent badges

Inside herdr (any pane, with or without `--herdr`) the board polls herdr's
socket every 500 ms and badges each card whose ticket has an agent running,
with no refresh key needed. The subtitle shows `herdr live` while this is on.

| Badge | Meaning | Source |
|-------|---------|--------|
| ⚙ | agent working | herdr (or frontmatter `processing` when live is off) |
| 🙋 | agent waiting on you: a permission prompt or question | herdr |
| ⏳ | waiting | frontmatter `agent_status` |
| 🚫 | blocked | frontmatter `agent_status` |
| ✓ | done | frontmatter `agent_status` |

herdr `idle` and `done` show no badge. While live is on, herdr's 🙋 / ⚙ win
over the ticket's frontmatter; otherwise the frontmatter badge shows, except
`processing`, which is hidden because herdr says no agent is working (a killed
pane clears its badge on the next poll even though the file still says
`processing`). Outside herdr, with `--no-herdr-live`, or while herdr is not
answering, badges come from frontmatter alone, exactly as without herdr. If
herdr speaks an unsupported socket protocol, live status stays off for the
run. Any other failure (herdr not answering at startup, or 3 failed polls
later) shows one warning, retries every 2 s, and goes live again on its own
once herdr is back.

Panes are matched to tickets by branch: the board reads the git branch checked
out in each agent pane's directory (linked worktrees included) and takes the
ticket key from it, following the `.sdlc` branch convention
`feature/{key-lower}-{slug}` or `hotfix/{key-lower}-{slug}`, e.g.
`feature/tkb-22-live-herdr-status` → `TKB-22`. A branch naming several keys
(`revert-45-feature/tkb-22-x`) badges each of them. Agents on a branch with
no key are ignored. When several agent panes work one ticket, 🙋 beats ⚙.
The branch comes from the `.git/HEAD` file; only repos using reftable ref
storage, where that file is a stub, run `git symbolic-ref` instead.

### Jump between a card and its agent pane

Two-way navigation, so the board and the agent working a ticket are one key
apart.

**Card → pane.** With the board open, `o` (or `ga`, for hands that reach for a
vim `g` prefix) focuses the herdr pane running the selected card's agent — its
workspace, tab and pane, in one `pane.focus`, across workspaces. When the
board is herdr's popup it closes on the way and leaves you in the agent pane,
with the focus intact: the popup's teardown does not send you back where you
were. A board running in a plain herdr pane stays open and just moves focus.
When a ticket has several agent panes, the one herdr already has focused wins,
otherwise the most urgent (🙋 over ⚙ over quiet).

The pane you are jumping to has to be on a branch that names the ticket — the
same rule as the badges, since both come from the branch (see *Live agent
badges* above). An agent working on `main`, or on any branch without a key,
belongs to no card, so it shows no badge and nothing can jump to it.

Nothing is silent. The key says `No agent pane for TKB-23` when no agent is on
that ticket, `Live agent status is off` outside herdr (or with
`--no-herdr-live`), `herdr live status unavailable` while herdr is not
answering, and `Agent pane for TKB-23 is gone` when the pane closed between the
last poll and the keypress — in which case the board stays open.

**Pane → card.** Pressing the `prefix+t` board key *inside* an agent pane opens
the board with that pane's ticket already selected: the launcher starts the
board in that pane's directory, and the board takes the key from the branch
checked out there. It says where the ticket is if you have hidden its column,
and opens quietly if the branch names nothing this board shows.

Standalone, the same thing is a flag:

```sh
tktban --select TKB-23           # open the board with TKB-23 selected
tktban --select-from-cwd         # ...with whatever this branch's ticket is
```

`--select` wins over `--select-from-cwd`, and a `--select` that finds nothing
says so where a derived one stays quiet. The branch is read in the background,
so the board paints first either way, and the selection is applied once — a
later auto-refresh never drags you back to it. One refusal: a board running
inside herdr that was given no herdr context does not fall back to guessing
from its working directory, because a plugin pane started without one runs in
the plugin's own checkout.

### The ticket key in herdr's sidebar

While the board is open it also tells herdr which ticket each agent pane is
on, as a pane-metadata token called `ticket` (`pane.report_metadata`, source
`odnf.tktban`). herdr renders custom tokens in its agent sidebar rows as
`$ticket`, so the key shows up next to the agent — the other direction from
the badges, which bring herdr's status onto the board.

herdr shows nothing until you ask for it — the sidebar rows are yours to
configure. Add the token in `~/.config/herdr/config.toml`:

```toml
[ui.sidebar.agents]
rows = [["state_icon", "machine", "workspace", "tab"], ["$ticket"], ["agent"]]
```

A pane on a branch that names no ticket carries no token, and a pane on a
branch naming several gets them comma-separated (`REVERT-45,TKB-22`). Panes
are matched exactly as the badges are, by branch (see *Live agent badges*).
`--no-herdr-tokens` turns the reporting off and leaves herdr's metadata
untouched; `--no-herdr-live` turns it off too, since it rides on the same
poll.

**The key only changes while a board is running.** The token is published
without an expiry on purpose — the board is usually a popup that closes
seconds later, and an expiring token would blank the sidebar exactly then —
so it survives the board, but nothing updates it once the board is gone.
Check out another branch in that pane afterwards and the sidebar keeps showing
the old key until a board next runs.

That is the *only* way it goes stale, because each poll compares every pane
against the token herdr itself reports, not against what this board has
published. So the first poll of any board — within 500 ms of opening it —
corrects every pane it finds wrong, including keys an earlier board left
behind and panes that have since moved to a branch naming no ticket (their
token is cleared). Nothing is written when herdr already agrees, so a board
left open makes no calls at all. A pane that has closed needs no cleaning up:
its metadata goes with it.

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
