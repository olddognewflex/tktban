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
                           # and hints when there is no archived role for auto-archive
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
| `D` | Dispatch this card to a herdr agent: branch, worktree, agent, prompt ([Dispatch a ticket to an agent](#dispatch-a-ticket-to-an-agent)) |
| `b` | Silence herdr's ticket toasts, or turn them back on ([Notifications](#notifications)) |
| `q` | Quit |
| `tab` / arrows | Move focus between columns and cards |

Columns come from `[board.roles]` (in config order); cards are grouped by their
canonical `status_role` and sorted by priority then key. A `⛔N` badge shows
unresolved blocker count. Tickets in an unconfigured lane appear in a trailing
`(unmapped)` column rather than being dropped.

### Auto-archive

Tickets that have sat in Done for seven days move to an Archive lane on their
own. Give the board somewhere to put them — an `archived` role:

```toml
[board.roles]
# ...your other roles...
done     = "Done"
archived = "Archived"
```

Without that role the board never archives anything, and `tktban doctor`
prints a `[hint]` saying how to turn it on (a hint, not a failure: the exit
status is unaffected).

With it, after every refresh the board looks at every ticket from
`tkt list --query all` — the whole board, whatever the filter is showing (lane
time is read for the filtered cards plus every Done ticket) — and takes each one whose `status_role` is `done` and whose time in that lane is at
least `archive_after_days`. For each, it re-reads the ticket (`tkt view`) and
skips it if it has already left Done, so two boards open on one tkt board do
not both archive it; then runs `tkt transition KEY archived` and comments
`Auto-archived after 7 days in Done.` (with your threshold in place of 7). The
status line reports `archived 3 tickets`, and the board refreshes once more.
Each tkt call a sweep makes is killed after 15 s, so a hung tkt cannot stall
auto-archive for the session.

- **The threshold** is `archive_after_days` in the settings file (default `7`;
  `0` turns auto-archive off). It is hand-edited, like the dispatch keys; the
  board never writes it. It is **per settings file**: herdr's own board reads
  its plugin settings file, not `~/.config/tktban/settings.toml`, and the
  one-time seed into it does not copy this key. So `archive_after_days = 0` in
  the standalone file does not stop herdr's board archiving — set it in both
  (paths below).
- **Time in lane** is tkt's read-only `lane-time`: time since the ticket last
  entered Done. A backend that reports no lane time (no history, or `0`
  seconds) never archives anything.
- **A failure never blocks the board.** A transition that fails is a warning on
  the status line (`archived 2 tickets; 1 failed: TKB-9: …`) and gets no
  comment. A ticket whose transition was tried is not tried again this board
  session, whether it moved or failed: a failed one waits for a restart. A
  ticket that was skipped — it had already left Done, or re-reading it failed
  (also a warning) — was not touched, so a later refresh may take it again.
- **The Archive column is hidden by default** (`hidden_roles = "archived"`).
  `X` shows it, like any hidden column; a `[ui.board] hidden_roles` config
  default is widened to include `archived`. On a board without an `archived`
  role, `X` shows every column the board has and leaves `archived` in the
  hidden set, so the lane is hidden once you add it.
- **Upgrading: add `archived` to `hidden_roles` yourself.** That default only
  reaches a board with no settings file yet. A settings file a board has
  already saved carries its own `hidden_roles` (often `hidden_roles = ""`),
  which wins, so the Archive column shows until you add it — in
  `~/.config/tktban/settings.toml` and, if you use herdr's board, in
  `~/.local/state/herdr/plugins/odnf.tktban/settings.toml` too:

  ```toml
  hidden_roles = "archived"   # or e.g. "archived,blocked" to keep others hidden
  ```

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
  (`~/.local/state/herdr/plugins/odnf.tktban/settings.toml`), seeded once from
  your standalone settings so the theme and hidden columns carry over —
  `notify` does not, being what the hook reads rather than display state
  ([Notifications](#notifications)). If that path is a symlink it is ignored
  and the standalone settings file is used instead;
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
| ⚙ | agent working | herdr `working`, a `tkt agents` run `running`/`stalled`, or frontmatter `processing` |
| 🙋 | agent waiting on you: a permission prompt or question | herdr `blocked` |
| ⏳ | waiting | a `tkt agents` run `halted`, or frontmatter `waiting` |
| 🚫 | blocked | a `tkt agents` run `blocked`, or frontmatter `blocked` |
| ✓ | done | frontmatter `agent_status` |

Each card merges three signals, most live first, and the first that has
something to say wins:

1. **herdr live status**: `working` → ⚙, `blocked` → 🙋.
2. **`tkt agents` run state**, read once per refresh: `running`/`stalled` →
   ⚙, `blocked` → 🚫, `halted` → ⏳. An idle herdr pane does not beat a live
   run. tkt keeps run dirs forever, so a `halted` or `blocked` run yields to a
   frontmatter status written strictly after it (`agent_status_at` newer than
   the run's `updated`): a ticket finished by hand after its run stopped at a
   gate shows the newer status, not ⏳ forever.
3. **frontmatter `agent_status`**, except `processing` is hidden when a live
   source says nothing is working: herdr is on and reports anything other than
   `working`/`blocked` for the ticket (no pane, idle, done, unknown), or the
   ticket's run is `dead`. A killed pane clears its badge on the next poll even
   though the file still says `processing`. `idle` and empty show no badge.

A stored badge also shows its age on the card's meta line, next to the lane
time (`⏱ 6h 10m  agent 3h`): from `agent_status_at` for a frontmatter badge,
from the run's `updated` for a `tkt agents` badge, so a stale status reads as
stale either way. A herdr badge is current and carries no age, and neither
does a timestamp in the future.

herdr `idle` and `done` show no badge. Outside herdr, with `--no-herdr-live`,
or while herdr is not answering, the herdr step is skipped; with a tkt that
has no `agents` verb the run step is skipped too, silently, and badges come
from frontmatter alone, exactly as before. If herdr speaks an unsupported
socket protocol, live status stays off for the run. Any other herdr failure
(not answering at startup, or 3 failed polls later) shows one warning,
retries every 2 s, and goes live again on its own once herdr is back.

Panes are matched to tickets by branch: the board reads the git branch checked
out in each agent pane's directory (linked worktrees included) and takes the
ticket key from it, following the `.sdlc` branch convention
`feature/{key-lower}-{slug}` or `hotfix/{key-lower}-{slug}`, e.g.
`feature/tkb-22-live-herdr-status` → `TKB-22`. A branch naming several keys
(`revert-45-feature/tkb-22-x`) badges each of them. Agents on a branch with
no key are ignored. When several agent panes work one ticket, 🙋 beats ⚙.
The branch comes from the `.git/HEAD` file; only repos using reftable ref
storage, where that file is a stub, run `git symbolic-ref` instead.

### Write `agent_status` back (opt-in)

The badges above fix what the *board* shows; the ticket file can still say
`processing` long after its agent pane was closed, and every other reader
(`tkt view`, a skill picking the next ticket) believes it. With write-back on,
the board corrects the frontmatter from what herdr sees:

| herdr sees | Ticket says | Board writes |
|------------|-------------|--------------|
| the ticket's agent pane is gone (closed, or the agent released) | `processing` | `idle` |
| the agent is blocked on a prompt or question | `processing` | `waiting` |
| the agent is working again | a `waiting` this board wrote | `processing` |
| the pane is gone | a `waiting` this board wrote | `idle` |

It is off by default, because it writes to your ticket provider. It is a hand
edit, like `dispatch`, in the settings file the board reads:

```toml
# ~/.local/state/herdr/plugins/odnf.tktban/settings.toml  (herdr's own board)
# ~/.config/tktban/settings.toml                          (everywhere else)
reconcile_agent_status = true
```

The one-time seed into herdr's plugin file does not copy this key, so a
board herdr launches stays off until you set it there.

It is deliberately narrow:

- **Only from a good live poll, inside herdr.** Outside herdr, with
  `--no-herdr-live`, after a protocol mismatch, and while polls are failing,
  nothing is written: a poll that did not answer is no evidence a pane closed.
- **Gone means the pane, not the branch.** A ticket's pane counts as gone
  only once herdr stops listing it with an agent (the pane closed, or herdr
  released the agent) for two good polls in a row *and* at least three
  seconds since a poll last saw it on the ticket. An agent whose branch merely
  stops naming the ticket — a detached HEAD mid-rebase, a checkout inside the
  worktree, a `cd` out of it — is still alive, so nothing is written for as
  long as it stays that way, even if another pane on the same ticket closes.
- **Only over a `processing` older than the pane.** `idle` is written only if
  the ticket's `agent_status_at` is before the start of the last poll that saw
  the pane, or is the very `processing` this board restored itself. A
  `processing` written later (another machine, an agent outside herdr) is
  left alone, as is one with a missing or unreadable `agent_status_at`.
- **Only tickets this board has seen a pane for.** A ticket that never had an
  agent pane while the board was open is never written.
- **Never `done`.** A closed pane looks the same whether the agent finished or
  was killed.
- **Never over someone else's value.** Each ticket is re-read (`tkt view`)
  right before the write and skipped unless it still says `processing`, or
  still holds the very `waiting` this board wrote (same `agent_status_at`). A
  `waiting` a skill wrote is never cleared.
- **Not while a headless run owns it.** No `idle` while `tkt agents` reports a
  `running` or `stalled` run for the ticket.
- **Only in this board's repo.** Ticket keys are unique per board, not
  globally, so the board writes only when its `[vcs] repo` is set and every
  agent pane on the ticket is a checkout of that repo (the `origin` remote,
  compared case-insensitively). A pane whose repo cannot be read blocks the
  write.
- **Once per episode.** A write that fails warns once and is not retried until
  herdr moves on (the pane comes back, leaves `blocked`, or goes `blocked`
  again, which re-arms the restore to `processing`). One batch of writes runs
  at a time, and the board refreshes after any write.

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

### Dispatch a ticket to an agent

`D` on a card hands the ticket to a herdr agent: cut the ticket's branch, open
a worktree for it, start an agent there, give it a first prompt, move the
ticket's lane and record what happened on the ticket.

**`D` always asks first.** The key makes one read-only `worktree.list` — "is
this a git checkout, which repo is it, does this branch already have a
worktree" — and then opens a confirm dialog showing every decision before any
of it happens: the lane the ticket will move to, the branch, the base it is
cut from, the repository, where the worktree goes, the agent kind and name,
and the prompt. `esc` closes it and creates nothing, saying nothing. `enter`
goes ahead.

#### What `enter` creates, in order

| Stage | Call | What it does |
|-------|------|--------------|
| 1 | `worktree.create` (or `worktree.open`) | cuts the branch from the base and opens a worktree for it, in a new herdr workspace with a tab and a pane at a shell. **herdr chooses the path** — tktban sends none, so a dispatched worktree lands under `[worktrees] directory` (default `~/.herdr/worktrees`) beside the ones you make by hand. If the preflight already saw the branch checked out, this is `worktree.open` instead and the existing worktree is rejoined rather than a second one cut. |
| 2 | `agent.start` | starts the agent in that pane, named after the ticket (`TKB-25` → `tkb-25`), which is what makes it recognisable in herdr's own agent list. No herdr method opens a pane *at* a command, so this is necessarily a second step — and the pane exists before its login shell reaches a prompt, so herdr answers `agent_pane_busy` until it does. tktban retries six times at 100/200/400/800/1600/3200 ms (6.3 s in all); the first real dispatch needed five attempts and 1.5 s on a shell with a prompt framework and a version manager in its rc files. A name already held by a live agent (`agent_name_taken`) gets one retry as `tkb-25-2`, out of the same budget. |
| 3 | `agent.prompt` | types the first prompt in. No `wait` is sent: a board must never block on an agent's status. **This retries too.** A successful `agent.start` means herdr detected the agent, not that it will accept a prompt - the active-named-agent registry lags, and herdr answers `agent_not_ready` until it catches up. Same schedule as the start. `agent_blocked` is never retried (herdr refuses before sending anything, so a retry would type into whatever dialog is up), and nor is a submission that failed part-way, which may have left half the prompt in the pane. |
| 4 | `tkt transition` | moves the ticket to the target lane — only when the agent actually started, and only when the ticket is still in the lane the plan was built for. |
| 5 | `tkt comment` | records the whole thing on the ticket: branch, worktree path, workspace, tab and pane ids, agent name and the argv herdr ran, whether the prompt landed, and what happened to the lane. |

The dialog stays open the whole time and says which stage it is on, and every
keystroke is swallowed while it runs — so a second `enter` cannot cut a second
worktree. The board does not quit afterwards, even as herdr's popup: there is
an agent to go and look at, and `o` is how you get to it.

**The badge appears when the agent starts *working*, not when the pane
appears.** A freshly started agent is `idle`, and ⚙ means busy — so expect a
second or two of nothing on the card after a successful dispatch, until the
agent picks the prompt up. `o` works immediately either way.

#### Nothing is ever removed

**A dispatch cannot undo its own work, by construction.** The herdr interface
it is given has no `worktree.remove`, no `pane.close` and no `workspace.close`
in it at all, so no failure path can reach one. A worktree may hold a
checkout, a stash or an edited file, and a board does not get to decide those
are disposable. When a dispatch stops half-way the worktree stays exactly
where it is, the comment on the ticket says where, and removing it is yours to
do (`herdr worktree remove`, or `git worktree remove` in the repo).

The other half of the same rule: **the worktree step is never retried.** A
`worktree.create` that failed with a herdr error code did not happen; one that
timed out or hit a closed socket *might* have. Asking again after "we don't
know" is how a ticket ends up with two worktrees, so tktban stops and says the
dispatch may be incomplete.

#### Every way it can go wrong

| What failed | What happens |
|-------------|--------------|
| `worktree.create` / `worktree.open` refused by herdr | Nothing was created. No transition. A comment carrying herdr's code and message, and a status line that names them. |
| `worktree.create` / `worktree.open` never answered (closed socket, deadline) | Nothing claims nothing was created — herdr may have cut the branch and opened a workspace before the socket dropped. The status says `TKB-25 may be half dispatched` and to check `herdr worktree list`, and the comment carries a worktree line saying the same. That comment is the **only** record that an orphan may exist, so it is written even though there is no path to name. |
| the worktree was made but herdr's reply carried no pane | The worktree **does** exist, so nothing says otherwise: the status names the path it is still at and the comment records it, while the agent bullet says the dispatch stopped before `agent.start`. No transition, nothing removed. |
| `agent.start` refused by herdr, after the worktree exists | No rollback: the worktree, workspace and pane stay. The lane does not move. The comment records the branch, the worktree path, the workspace id, the pane id, the name it tried and the error; the status says the worktree is ready but the agent did not start. |
| `agent.start` never answered (closed socket, deadline) | The same, except that nothing claims the agent did not start — we cannot know. The status and the comment both say it **may or may not** be running and to check the pane with `o` before dispatching again. |
| `agent.prompt` refused for the whole budget | The agent **is** dispatched, so the transition and the comment still run. The status says the prompt did not land, how many times it was tried, and that `o` focuses the pane so you can paste it yourself; `agent_not_ready` and `agent_blocked` are reported by name. |
| `agent.prompt` never answered | The same, except that nothing claims the prompt did not land - the text may have gone in. The status and the comment both say it **may or may not** have arrived and to check the pane before pasting it again, because pasting a prompt that did arrive gives the agent the same instruction twice. |
| `tkt transition` | Still comments. The status says dispatched but the lane did not move. |
| `tkt comment` | A warning on the status line only. It never blocks and never undoes anything. |
| herdr unreachable, or a deadline mid-sequence | The comment says the dispatch may be incomplete. The worktree step is **not** retried. |
| the ticket left the dispatch source lane meanwhile | It is dispatched, and the lane is left alone — moving it from wherever it is now to the plan's target is not the move you agreed to. The status and the comment both say which lane it is in now. A card that has merely been *filtered* out of sight is not treated as having moved; one that has vanished from an unfiltered board is. |

Each stage has its own budget: 15 s for the worktree, 35 s for `agent.start`,
15 s for the prompt and 5 s for each `tkt` call. Both agent budgets cover a whole
retry loop rather than one call: the start's is herdr's own 20 s startup timeout
*plus* the 6.3 s of backoff the loop may spend getting to its last attempt, with
margin - herdr's timer has to be the one that fires, because a herdr timeout is a
typed error naming what broke, while ours is a closed socket that cannot even say
whether the agent started.

**Only one dispatch runs at a time.** `D` and the confirm both refuse while one
is in flight (`Dispatching TKB-25 — wait for it to finish`), and anything that
was asked for before the dispatch started declines to land on top of it: a
ticket fetch for `v`, an issue-type fetch for `n`, an `$EDITOR` launch for `N`
or `E`, and a stale `esc` from the confirm dialog itself. The progress dialog
swallowing keys is not relied on as the only lock — it can be displaced, and a
second dispatch of one ticket would `worktree.open` into the same pane and start
a second agent on one branch.

**When the retries took a while, the comment says so.** A dispatch that spent a
second and a half being told the shell was not ready records
`agent start: 5 attempts, 4 busy, 0 renamed, waited 1.5s`, and one whose prompt
had to wait for the agent registry records
`prompt attempts: 3 attempts, 2 not ready, 0 busy, waited 300ms`. Both lines
appear on the failure path as well as the successful one - one attempt and seven
are very different stories, and the first live failure was not diagnosable from
the ticket without them.

**It is off by default.** `dispatch` in tktban's settings file defaults to
`false` and the board never writes it — like `notify`, it is a hand edit:

```toml
# ~/.local/state/herdr/plugins/odnf.tktban/settings.toml  (herdr's own board)
# ~/.config/tktban/settings.toml                          (everywhere else)
dispatch = true
dispatch_agent = "claude"   # the herdr agent kind to start
dispatch_args = ""          # extra argv for it, split on spaces
dispatch_prompt = ""        # empty = the built-in prompt
```

`dispatch_prompt` may use `{key}`, `{summary}`, `{branch}`, `{base}`, `{role}`
and `{lane}`. `--no-herdr-dispatch` turns the key off for one run whatever the
setting says.

**There are two of those files, and a board reads exactly one.** Setting
`dispatch = true` in the standalone file will not turn the key on for a board
herdr launched, which reads the plugin one — the same split that makes `notify`
land in only one place ([Notifications](#notifications)). So `D` names the file
*that board* is reading when it refuses, and that is the file to edit:

```
Dispatch is off — set dispatch = true in ~/.local/state/herdr/plugins/odnf.tktban/settings.toml
```

Every way `D` can refuse says which one it was, and opens nothing:

| Refusal | Why |
|---------|-----|
| `Dispatch is off — set dispatch = true in <file>` | the opt-in setting is not set in the file this board reads |
| `Dispatch is off for this board (--no-herdr-dispatch)` | turned off for this run on the command line |
| `Live agent status is off` / `herdr live status unavailable` | outside herdr, or herdr is not answering |
| `Select a card first` / `That card has no ticket key` | nothing to dispatch |
| `TKB-25 already has an agent pane (o focuses it)` | an agent is already on it; two agents racing one branch is not an improvement |
| `Don't know which repo to dispatch TKB-25 in — open the board from a pane in the repo` | see below |
| `No [vcs] branch_fmt in <config>` | no branch convention to follow |
| `branch_fmt … in <config> doesn't name TKB-25` | the branch would not name the ticket, so the board could never badge or jump to its agent |
| `No agent-owned transition out of To Do ([board] ownership in <config>)` | `[board] ownership` gives no agent-owned move out of that lane |
| `Not a git work tree: …` | herdr says the directory is not a checkout |
| `This board is open in a worktree …` | there is nothing to branch from |
| `Timed out reading the tkt config …` | the two `tkt cfg` reads did not finish inside the 2 s budget |
| `TKB-25 picked up an agent while the dialog was open` | an agent appeared between `D` and `enter`; live status keeps polling behind the dialog, and this is what stops two agents racing one branch |

Two details worth knowing:

- **The lane comes from your config, not from tktban.** The target is the
  first agent-owned transition out of the card's own role in
  `[board] ownership` (`"todo->in_progress" = "agent"`), ranked by board
  order. No lane name is hard-coded.
- **The repo is resolved, never guessed.** A herdr *plugin* process that was
  given no herdr context refuses rather than fall back to its working
  directory — a plugin pane started without `--cwd` runs in tktban's own
  install checkout, and two repositories here share one board, so a guess
  could branch the wrong repository for a real ticket. `tktban` typed in an
  ordinary herdr terminal pane is not a plugin process and does use its
  working directory, which is exactly the repo you meant; `--select-from-cwd`
  refuses more broadly, because mis-selecting a card costs a keystroke and
  mis-resolving a repository costs a worktree in the wrong repo.
- **The lane has to be one the board has.** A target role that is not in
  `[board.roles]` — a typo like `"todo->in_progres" = "agent"` — is refused
  rather than transitioned into, since moving a ticket there would take the
  card off the board.

Because the branch names the ticket, everything above works on a dispatched
agent with no further wiring: the card badges it and `o` jumps to its pane,
found by reading the pane's branch back.

tktban does not write `agent_status` into the ticket's frontmatter when it
dispatches. The card's badge comes from herdr while live status is on, and the
badge merge deliberately hides a frontmatter `processing` in that case — so a
written-back status would be invisible on the board that wrote it. Correcting
a stale status after the agent stops or closes is a separate, opt-in feature:
[Write `agent_status` back](#write-agent_status-back-opt-in).

### Notifications

The manifest also hooks herdr's `pane.agent_status_changed` event, so herdr
runs `tktban herdr-hook` whenever an agent pane changes state, board open or
not. When an agent on a ticket branch needs you, herdr pops a toast:

| Agent goes | Toast | Sound |
|------------|-------|-------|
| blocked (a permission prompt or a question; herdr cannot tell them apart) | `TKB-24 needs you` | `request` |
| done | `TKB-24 finished` | `done` |

Nothing toasts for `working`, `idle` or `unknown`. Those runs exit before
touching the socket or the disk, so most hook runs cost only a process start.

The hook waits a second, then re-reads the pane from herdr, and stays quiet
when:

- the pane has moved on since the event (the prompt was already answered);
- the pane is focused, because you are already looking at it;
- its branch names no ticket, or no `.sdlc/config.toml` covers its directory
  (panes are matched to tickets by branch, as for the badges);
- another hook run is already waiting out the second for that pane and
  status, or has just handled the same transition (herdr can run several at
  once; they agree through `notify-state.json` in the plugin state dir, keyed
  on herdr's `state_change_seq`: the same seq is handled for good, a lower one
  only for a minute, because herdr restarts the counter. So a herdr restart
  within a minute of a toast can drop that pane's first prompt after it);
- the same pane toasted the same status less than 30 s ago, so a prompt that
  flickers does not toast twice. Each status keeps its own 30 s: `done` in
  between does not reopen `blocked`.

Things to know:

- **You have to turn herdr's toasts on.** Set `ui.toast.delivery` in
  `~/.config/herdr/config.toml` to `"herdr"` (in-app), `"terminal"` or
  `"system"`. herdr's default config lists `"off"`, and with that herdr
  answers every toast `disabled` and nothing shows (the log says
  `not shown: disabled`).
- **You may get two.** herdr toasts background agents by itself (`ui.toast`,
  `ui.sound`); this hook adds one that names the ticket. Both can appear for
  the same prompt.
- **No click-to-focus.** herdr's `notification.show` takes no pane and no
  action, so a toast only tells you; `prefix+t` then `o` gets you there.
- **Toasts share one rate limit.** herdr allows one API notification per
  second across every caller. A hook turned away as rate-limited or busy tries
  twice more, 1.1 s apart, then gives up. A run that gives up (or hits a
  socket error or runs out of time; a run ends within about 7 s) hands its
  claim back, so the pane's next change to the same status is not swallowed
  by the 30 s flap guard as if this toast had shown. One told `disabled` or
  `no_foreground_client` keeps it, since trying again cannot help.
- **Turn them off** with `b` on the board. It flips `notify` in the settings
  file that board is using, says which way it went — naming that file, when it
  is not the one the hook reads — and —
  on herdr's own board, while live status is on — the subtitle reads
  `herdr live (muted)` until you press `b` again. Editing the file by hand
  still works: `notify = false`, as the TOML boolean (`"false"` or `0` count
  as on).
- **Press it on herdr's own board.** The hook reads one file and one only: the
  plugin settings file in herdr's plugin state dir (`HERDR_PLUGIN_STATE_DIR`)
  — on macOS `~/.local/state/herdr/plugins/odnf.tktban/settings.toml` — which
  is what a board started as `tktban --herdr` writes. Every other board writes
  the standalone `~/.config/tktban/settings.toml`: a board run outside herdr,
  and also one run in a plain herdr pane, which gets live badges but keeps its
  settings standalone. `notify` there reaches nothing — the hook never reads
  that file, and the one-time seed that carries your standalone settings into
  the plugin file copies only `theme` and `hidden_roles`. Those boards say so
  when you press `b`, and never claim `(muted)`. A plugin path that is a
  symlink is refused by both: the board falls back to the standalone file, and
  the hook, left with no file it will read, keeps toasting — put a regular file
  back there to get the toggle working again. Either way a board saves only
  the keys it owns (`theme`, `hidden_roles`, and `notify` when you press `b`),
  re-reading the file first, so an edit made while a board is open survives the
  board's next save — and `b` flips what the file says at that moment, not what
  the board read when it opened. If the file is not valid TOML, though, a board
  save rewrites it from defaults, erasing the bad line.
- **Outside herdr nothing toasts.** The standalone board never notifies, and
  `tktban herdr-hook` does nothing unless herdr started it.

Every run prints one line saying what it decided (`shown TKB-24 blocked`,
`skip: pane focused`, ...), which `herdr plugin log list --plugin odnf.tktban`
shows.

**After pulling this, rebuild and re-link**, or herdr keeps running the old
binary, which does not know `herdr-hook` and fails every hook with exit 2.
Re-linking is what makes herdr read the manifest's new `[[events]]` entry:

```sh
go build -o bin/tktban ./cmd/tktban
herdr plugin link .
```

## How it talks to tkt

`internal/tkt/tkt.go` is the entire coupling surface — a thin subprocess wrapper:

| tktban call | tkt verb |
|-------------|----------|
| read columns | `tkt cfg board.roles --json` |
| read tickets | `tkt list --query all --json` |
| move | `tkt transition KEY ROLE` |
| dispatch: branch convention | `tkt cfg vcs --json` |
| dispatch: which lane an agent owns | `tkt cfg board.ownership --json` |
| comment | `tkt comment KEY BODY` |
| auto-archive | `tkt view KEY --json`, `tkt transition KEY archived`, `tkt comment KEY BODY` |
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
