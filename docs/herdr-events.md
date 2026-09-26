# herdr events for tktban (TKB-20 spike)

Which herdr events can a tktban plugin react to, and how should the board get
live agent state? Measured against **herdr 0.9.0, socket protocol 22** on
2026-09-18 with a throwaway linked plugin, a raw socket subscriber, and a Go
poll client. Findings here gate TKB-21 (plugin packaging), TKB-22 (live badge),
TKB-24 (notifications) and TKB-26 (write-back).

## Answer

`pane.agent_status_changed` **is hookable** from a manifest `[[events]]` entry,
and a hook sees **every pane**, including panes created after the plugin
loaded. No pane id is needed.

The socket `events.subscribe` route is the weakest option: its status
subscription requires one `pane_id` per pane, rejects wildcards, and never sees
panes created after it subscribed.

## Recommended architecture

| Need | Mechanism | Why |
|---|---|---|
| Live badges while the board is open (TKB-22) | Poll `agent.list` every 500 ms | ~0.9 ms per call; complete truth each tick; no subscription upkeep; same approach as the herdr.auto-title plugin (its `internal/app/config.go`) |
| Side effects while the board is closed (TKB-24, TKB-26) | Manifest `[[events]]` hooks on status + lifecycle events | Fires for all panes without a long-lived process |
| Low-latency per-pane watch | Not recommended | Per-pane subscriptions, all-or-nothing validation, reconnect on every new pane |

**Hook handlers must treat the event as a signal, not as state.** Each hook is
a separate process, and the payload carries no sequence number, so a handler
must not trust payload order. (`AgentInfo` does carry one, `state_change_seq`,
which is what re-reading gives you; see *Verified in TKB-24*.) Two rules follow:

1. **Status events:** re-read the pane with `agent.get` and act on that.
   `HERDR_SOCKET_PATH` is set in the hook environment, so this works.
2. **Close and exit events:** the pane is already gone, so `agent.get` returns
   `agent_not_found`. `pane.closed` carries only `pane_id, workspace_id`, and
   `workspace.closed` carries no pane ids at all. The plugin must keep its own
   `pane_id → {workspace_id, cwd, ticket key}` map in `HERDR_PLUGIN_STATE_DIR`,
   filled from `pane.created` (which carries `cwd`) and status events. Close
   handlers look the pane up there, and `workspace.closed` resolves every pane
   mapped to that workspace.

## Hookable events

Result of linking a probe manifest with one `[[events]]` entry per candidate.
"Rejected" means `herdr plugin link` warned `unknown event '<name>'` and the
hook never runs. "Fired" means the hook ran during the test.

| `on =` | Link | Observed | Notes |
|---|---|---|---|
| `pane.agent_status_changed` | accepted | fired | all panes; real (screen-detected) and reported agents |
| `pane.agent_detected` | accepted | fired | also fires on release with `released: true, final_status` |
| `pane.created` | accepted | fired | |
| `pane.closed` | accepted | fired | no status event precedes it |
| `pane.exited` | accepted | fired | shell exit; see lifecycle gaps |
| `workspace.created` | accepted | fired | |
| `workspace.closed` | accepted | fired | no per-pane events follow, even with live agents; no pane ids in payload |
| `tab.created` | accepted | fired | |
| `tab.renamed` | accepted | fired | herdr.auto-title renamed tabs on create and on agent detection |
| `workspace.updated`, `workspace.renamed`, `workspace.focused` | accepted | not triggered | |
| `tab.closed`, `tab.focused`, `pane.focused` | accepted | not triggered | tests used `--no-focus` |
| `worktree.created`, `worktree.opened`, `worktree.removed` | accepted | not triggered | persiyanov.reviewr relies on the first two |
| `workspace.metadata_updated` | **rejected** | — | matches herdr docs |
| `pane.updated`, `pane.output_changed`, `layout.updated` | **rejected** | — | socket event kinds, not hook events |
| `pane_agent_status_changed` (underscore) | **rejected** | — | hooks use dotted names |

`[[startup]]` hooks run at server start, not on `plugin link`.

## Hook runtime

- `HERDR_PLUGIN_EVENT` holds the dotted name (`pane.agent_status_changed`).
- `HERDR_PLUGIN_EVENT_JSON` holds the underscore envelope
  (`"event":"pane_agent_status_changed"` with `data.type`). That matches the
  socket's global events, but **not** per-pane socket subscription events,
  which use the dotted name and omit `data.type`.
- `HERDR_SOCKET_PATH` is set, so a hook can call back into herdr.
- `HERDR_PLUGIN_STATE_DIR` is `~/.local/state/herdr/plugins/<plugin-id>/`.
- Config dir is `~/.config/herdr/plugins/config/<plugin-id>/`.
- `herdr plugin log list --plugin <id>` shows every invocation with exit code,
  stdout, stderr and timings. Use it to debug hooks.
- Burst of 7 transitions: all 7 hooks fired in the order reported, each as a
  separate process, 8 to 20 ms apart by the hook's own clock. `herdr plugin log`
  showed each run taking 9 to 15 ms, so runs can overlap. Order held in both
  bursts tested, but nothing guarantees it.

Sample hook payload:

```json
{"event":"pane_agent_status_changed","data":{"type":"pane_agent_status_changed","pane_id":"wD:p1","workspace_id":"wD","agent_status":"blocked","agent":"claude"}}
```

## Agent status semantics

Values: `idle | working | blocked | done | unknown`.

- A reported `idle` after work can surface as `done`, which is herdr's
  "finished, not yet viewed" state. The API and hooks see the server's seen
  state, while TUI badges track it per client, so the board and the herdr UI
  can disagree. **Treat `idle` and `done` the same** on the board and let the
  tkt lane say what is done.
- Releasing an agent (`pane release-agent`) reports `unknown`.
- `blocked` means herdr saw an approval or question UI. It is distinct from a
  tkt dependency block.
- Observed status events carried `pane_id, workspace_id, agent_status, agent`.
  The schema also allows `display_agent`, `title` and `state_labels`, so
  parsers must tolerate extra fields. There is no `cwd`, so the ticket join
  needs `agent.list` / `agent.get` or the plugin's own pane map anyway.

## Lifecycle gaps the board must cover

| Action | Events fired | Not fired |
|---|---|---|
| Close one pane (live agent) | `pane.closed` | any status change |
| Last shell in a workspace exits | `pane.agent_detected` (released, final_status done), `pane.agent_status_changed` done, `pane.exited`; workspace then gone from `workspace list` | `workspace.closed`, `tab.closed`, `pane.closed` |
| Close a workspace (one pane working, one blocked) | `workspace.closed` | `pane.closed`, `tab.closed`, status changes |

Event order also differs by channel. On shell exit, hooks ran detected, then
status, then exited, while the socket delivered `pane_exited` first. Handlers
must not depend on order.

So "agent gone" must be derived from any of `pane.closed`, `pane.exited`,
`workspace.closed`, or simply from the pane missing in the next `agent.list`
poll. Polling handles all three without special cases.

## Socket subscription details

For reference if a subscriber is ever needed.

- One NDJSON request per connection for normal calls. herdr closes after
  replying. `events.subscribe` keeps the connection open: first line is
  `{"result":{"type":"subscription_started"}}`, then one event per line.
- `pane.agent_status_changed` requires `pane_id`. Missing: `invalid_request:
  missing field pane_id`. `"*"`: `pane_not_found`.
- One invalid entry rejects the whole request and closes the connection, so a
  pane closing between snapshot and subscribe fails every subscription in it.
  Tested with one global entry, one live pane and one missing pane. The error
  id names the failing index: `{"id":"<req>:sub:2:probe","error":{"code":"pane_not_found"}}`.
- Existing subscriptions do not cover new panes. Only the global
  `pane.agent_detected` arrives. A new connection naming the pane works.
- Naming mismatch: per-pane subscription events use `"event":"pane.agent_status_changed"`
  with no `type` in `data`; global events use underscore names
  (`"event":"pane_agent_detected"`).
- Bootstrap pattern if used: subscribe first, buffer, then `session.snapshot`,
  then apply buffered events.

## Poll cost

Go client, one connection per call, 6 agents:

```text
agent.list x200: total 174ms, per call 0.87ms
```

At 500 ms that is under 0.2% of a core. herdr.auto-title measured a six-pane
`session.snapshot` at 0.47 ms and 6 KB and polls it every 500 ms.
`agent.get` on a missing pane returns error code `agent_not_found`.

## Version pinning

`session.snapshot` returns `version` and `protocol`. herdr-board refuses
anything but herdr 0.9.0 / protocol 22, per the comment in its
`herdr-plugin.toml`. For tktban:

- Manifest: `min_herdr_version = "0.9.0"`.
- Runtime: read `protocol` once at startup. On mismatch, disable herdr features
  with a status-line notice instead of refusing to run. The board still works
  from tkt alone.

## Implications for follow-up tickets

- **TKB-21:** no socket calls needed. Manifest can already declare the hooks
  below.
- **TKB-22:** replace "subscribe goroutine" with a 500 ms `agent.list` poll
  emitting a Bubble Tea message. Join key from `cwd` → git branch → ticket key
  still needed. Merge rule `done == idle` confirmed.
- **TKB-24:** `[[events]] on = "pane.agent_status_changed"` → handler re-reads
  the pane, looks up its ticket, calls `notification.show`. Works when the
  board is closed.
- **TKB-26:** same hook plus `pane.created`, `pane.closed`, `pane.exited`,
  `workspace.closed` drive `tkt edit --agent-status` write-back. It needs the
  persisted pane map described above, because close events cannot be
  re-read. Dedupe writes, since hooks can overlap.

Suggested manifest fragment for TKB-24 / TKB-26:

```toml
[[events]]
on = "pane.created"
command = ["./bin/tktban", "herdr-hook"]

[[events]]
on = "pane.agent_status_changed"
command = ["./bin/tktban", "herdr-hook"]

[[events]]
on = "pane.closed"
command = ["./bin/tktban", "herdr-hook"]

[[events]]
on = "pane.exited"
command = ["./bin/tktban", "herdr-hook"]

[[events]]
on = "workspace.closed"
command = ["./bin/tktban", "herdr-hook"]
```

## Not tested

- `worktree.*` hooks fire (accepted at link; reviewr depends on them).
- Focus events (tests avoided stealing focus).
- `workspace.moved`, `workspace.reordered`, `tab.moved`, `pane.moved` (schema
  kinds never tried as hooks).
- How herdr detected the real Claude pane seen in the tests. The installed
  Claude integration only reports the session id, so status is presumably
  screen-detected; `herdr agent explain <pane>` would confirm.
- Hook behaviour under herdr server restart or `server reload-config`.
- Whether a hook can run concurrently with itself for the same pane beyond the
  7-event burst measured here.

## Verified in TKB-22

Rechecked against herdr 0.9.0 / protocol 22 while building the live badge
(`internal/herdr/client.go`, `live.go`):

- `ping` is the cheap protocol probe: `{"result":{"type":"pong","version":"0.9.0","protocol":22,...}}`.
  tktban calls it when the board opens, instead of reading
  `session.snapshot`. Any other protocol turns live status off for the run;
  any other failure (herdr not up yet, a timeout) re-probes every 2 s until
  herdr answers.
- `agent.list` replies `{"result":{"type":"agent_list","agents":[...]}}` and
  lists **only agent panes**. A closed pane just drops out of the next reply,
  which is how the board clears a badge without any close event.
- `agent.get` takes `{"target": "<pane>"}`, not `pane_id`. Sending `pane_id`
  fails with `invalid_request: missing field target`; a missing pane gives
  `agent_not_found`.
- Malformed requests (unknown method, missing field) reply with `"id":""`, so
  a client cannot match errors to requests by id. One request per connection
  makes that moot.
- AgentInfo has no branch. The ticket join reads git `HEAD` from the pane's
  `foreground_cwd` (falling back to `cwd`), following `.git` files for linked
  worktrees, and takes the key from `feature/{key-lower}-{slug}` or
  `hotfix/{key-lower}-{slug}`. With reftable ref storage `HEAD` is the stub
  `ref: refs/heads/.invalid`, so only then does it ask
  `git symbolic-ref --short -q HEAD` (checked with git 2.55).
- `HERDR_SOCKET_PATH` is set in plugin pane environments (seen on a running
  `overlay` plugin pane), not just in hooks.

## Verified in TKB-23

Rechecked against herdr 0.9.0 / protocol 22 while building the card ↔ pane
jump: the request shapes from `herdr api schema --json`, and the focus
behaviour itself by hand in a live session (see `internal/herdr/client.go`).

- `pane.focus` takes `{"pane_id": "<pane>"}` (schema `PaneTarget`), while
  `agent.focus` takes `{"target": "<pane|agent>"}` (`AgentTarget`). The two are
  easy to swap; sending `target` to `pane.focus` is an `invalid_request`. A
  success carries the pane info; an id herdr no longer knows gives
  `pane_not_found`, which the board turns into "Agent pane for KEY is gone".
- **One `pane.focus` is enough**, including across workspaces and tabs: herdr
  focuses the pane's workspace and tab on the way. No workspace.focus /
  tab.focus ladder is needed, though `PaneRef` carries `workspace_id` and
  `tab_id` if one ever is.
- A plugin pane with `placement = "popup"` has **no public pane id** — it is
  not in `pane list` — and `popup.close` takes `{}` (`EmptyParams`),
  SIGHUPping whichever popup is open. So the board must finish the
  `pane.focus` round trip **first** and only then exit; its exit is what closes
  the popup. It must never call `popup.close` to close itself.
- **The popup's exit does not undo the jump.** Verified by hand: pressing `o`
  on a card whose agent pane is on a branch naming that ticket lands focus in
  the agent's pane, and that focus survives the popup being torn down — herdr
  does not restore whatever was focused before the popup opened. That is the
  whole reason for the ordering above: focus, then quit. The reverse order
  would race the teardown, and no fallback (a delayed focus after the program
  returns, say) is needed.
- `plugin.pane.open` (`PluginPaneOpenParams`) accepts **no argv**, but it does
  take both `cwd` and `env` (plus `plugin_id`, `entrypoint`, placement and
  size). So a pane's command line is fixed by the manifest, and everything
  else about the invocation has to arrive as the working directory or as
  environment variables. `herdr plugin pane open` exposes both as `--cwd` and
  `--env KEY=VALUE`.
- What that means for the ticket the board opens on: **the working directory
  is the main path**. `scripts/open-board.sh` runs as an action, and actions
  are where `HERDR_PLUGIN_CONTEXT_JSON` is confirmed to be set; it reads
  `focused_pane_cwd` (else `workspace_cwd`) from there and passes it as
  `--cwd`, so the board's own process directory is already the right repo.
  The launcher also forwards the context with `--env`, because it was not
  confirmed that herdr sets `HERDR_PLUGIN_CONTEXT_JSON` for the *pane* it
  opens — so `herdr.SelectKey`'s context branches are a fallback for when it
  is there, not the path normally taken. Deriving the key inside tktban is
  therefore a choice (it keeps the launcher a dumb wrapper and the key
  resolution testable in Go), not something the API forces.
- A plugin pane started **without** `--cwd` runs in the plugin's own install
  directory, which is a checkout with a ticket key of its own. `SelectKey`
  refuses to read a working directory when it is inside herdr and was handed
  no context at all, so a launcher that cannot work out where it was invoked
  opens the board with nothing selected instead of with the plugin's own
  ticket selected.
- **An agent on a branch that names no ticket cannot be reached from the
  board.** Seen while testing the jump: a pane sitting on `main` (or any
  branch without a key) resolves to no ticket, so it badges nothing and no
  card can jump to it. This is inherent to deriving keys from branch names
  (TKB-22) rather than anything about the jump — herdr's `AgentInfo` carries
  no ticket of its own — and it applies equally to the badges. AC3's
  `pane.report_metadata` tokens would not change it either: the board would
  still have to know the ticket before it could report one.
- `pane.report_metadata` (`PaneReportMetadataParams`) requires **`pane_id` and
  `source`** and takes, all optional, `agent`, `title`, `display_agent`,
  `state_labels`, `tokens`, `applies_to_source`, `seq`, `ttl_ms`, and three
  booleans — `clear_title`, `clear_display_agent`, `clear_state_labels`.
  There is no `clear_tokens`: the CLI's `--clear-token NAME` is sugar for
  `tokens: {NAME: null}`. tktban sends `pane_id`, `source` (`odnf.tktban`)
  and `tokens`, nothing else.
- **Token constraints**: at most 16 tokens per call, names must match
  `^[A-Za-z0-9_-]{1,32}$`, values are `string | null`, and **null clears** that
  token (a key left out is simply not touched — that distinction is why
  `ReportPaneTokens` takes `map[string]*string`). `ttl_ms` is 1..86_400_000
  when given; tktban gives none, because the token has to outlive the popup
  that published it.
- **Tokens come back on reads**: both `AgentInfo` (`agent.list`) and `PaneInfo`
  (`pane.list`) carry a `tokens` map — up to 32 on the read side, against 16
  per call — alongside `title`, `display_agent` and `state_labels`. So what was
  published is observable without guessing. (`applies_to_source` suggests herdr
  keeps sources apart; how it merges two sources writing the same token name
  was not tested, and tktban writes only `ticket`.)
- **The sidebar is the consumer, and it is the user's to configure.**
  `herdr --default-config` documents `[ui.sidebar.agents] rows` (built-ins
  `state_icon`, `state_text`, `machine`, `workspace`, `tab`, `pane`, `agent`,
  `terminal_title`, `terminal_title_stripped`) and says "Custom values reported
  through pane metadata use a `$name` token", stylable as
  `{ token = "...", fg = "#89b4fa", bold = true }`. There is also
  `[ui.sidebar.agents.rows_by_agent]` per canonical agent id. Nothing tktban
  publishes shows up until a row asks for `$ticket`, which is what AC3's "when
  configured" means.
- **The `ticket` token namespace is ours.** Grepping `~/.config/herdr/plugins`
  for `report_metadata` hits only schema copies and docs — no installed plugin
  (auto-title, board, lazygit, nvim, reviewr, plugin-manager) writes pane
  metadata. `herdr.auto-title` renames *tabs* (`tab.rename`), which is a
  different surface; tktban deliberately sets neither `title` nor
  `display_agent` so the two never fight.
- **Publishing is best-effort and stateless.** `SocketSource.Poll` is now also
  a writer: after a successful poll it diffs the ticket it resolved for each
  pane against the `tokens` that same `agent.list` reply carried, and reports
  only the panes where the two differ — a value to set, or `{"ticket": null}`
  to clear one that is on no ticket any more (a branch naming none, or an
  agent herdr has released). Because the comparison is against **herdr's own
  state**, not against anything the process remembers, it is idempotent and
  self-healing: a board start corrects whatever an earlier board left behind,
  a steady-state poll makes zero calls, and a failed call needs no retry
  bookkeeping — herdr still does not hold the wanted value, so the next poll
  tries again. Nothing about it can fail a poll or change a badge.
- A pane that has **closed** is not reported to: it is gone from `agent.list`,
  it took its metadata with it, and `pane.report_metadata` for it would only
  answer `pane_not_found`. Only panes herdr still lists are reconciled.

## Verified in TKB-24

Rechecked against herdr 0.9.0 / protocol 22 while building the notify hook
(`internal/herdr/notify.go`, `notifystate*.go`, `tktban herdr-hook`). Request
and reply shapes are from `herdr api schema --json`; the runtime limits are
from the TKB-24 research against herdr 0.9.0 and are not re-measured here.

- **The hook envelope is confirmed by the schema.** `EventEnvelope` is
  `{"event": EventKind, "data": EventData}`, both required, and the
  `pane_agent_status_changed` variant of `data` requires `type`
  (const `"pane_agent_status_changed"`), `pane_id`, `workspace_id` and
  `agent_status`, and allows `agent`, `display_agent`, `title` (all
  `string | null`) and `state_labels` (a string map). That matches the spike's
  sample above exactly; `ParseStatusEvent` reads `data.pane_id` and
  `data.agent_status` from it and ignores the rest.
- **`notification.show`** (`NotificationShowParams`) takes `title` (required),
  `body` (`string | null`), `sound` (`none | done | request`) and `position`
  (`top-left | top-right | bottom-left | bottom-right | null`). herdr
  sanitises and clips title to 80 characters and body to 240. tktban sends
  `title`, `sound` and a short `body`, and clips the title itself so a long
  key list loses its tail visibly (`…`) rather than silently.
- The reply is `{"type":"notification_show","shown":bool,"reason":...}` with
  `reason` one of `shown | disabled | rate_limited | no_foreground_client |
  busy`. The hook retries `rate_limited` and `busy` (twice, 1.1 s apart) and
  gives up quietly on `disabled` (`ui.toast.delivery = "off"`) and
  `no_foreground_client`.
- **One global 1 s rate limit** covers every API caller, not per plugin. A
  burst of prompts across panes can therefore lose toasts to each other and to
  other plugins; the retries cover a short collision, not a storm.
- **No target, no click action, no dedupe key.** A toast cannot focus a pane
  and herdr will not collapse repeats, so dedupe is the caller's job.
- **herdr has its own toasts.** `[ui.toast]` `delivery = off | herdr | terminal
  | system` and `[ui.sound]` (with `done_path` / `request_path` and per-agent
  overrides) already announce background agents changing state. The hook does
  not replace them; its toast adds the ticket key, and both can appear.
- **`AgentInfo.state_change_seq`** (`uint64`, default 0) is on every
  `agent.get` / `agent.list` agent, so hooks that re-read the same transition
  see the same number. That corrects the spike's "no sequence number": the
  *payload* has none, but the re-read does. It is **stamped per pane and not
  persisted**: values seen were small (722..730), and `~/.config/herdr/session.json`
  keeps pane numbering (`public_pane_numbers`, `next_public_pane_number`) but
  no seq, so after a herdr restart a pane can keep its id (`wC:p1`) while its
  counter starts again low. So the seq only orders hooks over a short window:
  the hook treats an equal seq as handled however old (herdr may re-send a
  status event with no new transition), and a lower one as handled for 60 s,
  after that as a reset counter. Accepted edge: a herdr restart within 60 s
  of a toast can drop that pane's first prompt after it. It claims the seq in
  `$HERDR_PLUGIN_STATE_DIR/notify-state.json` under a flock on `notify.lock`
  before sending, so exactly one of several overlapping hooks toasts, and
  gives the claim back when the send fails for a reason a retry could get
  past, so the pane's next change to that status is not taken for a flap. Entries expire after 24 h, on load as well as on save. A 0 (an
  older herdr) leaves only the 30 s per-pane, per-status flap guard.
- `agent.get {"target": ...}` replies `{"type":"agent_info","agent":{...}}`,
  and `focused`, `foreground_cwd` and `cwd` come with it, so one call answers
  "still blocked?", "is the human looking?" and "which repo?".
- **Hook runtime.** A hook runs with the plugin root as its working directory
  (so `./bin/tktban` resolves), the payload is in `HERDR_PLUGIN_EVENT_JSON`
  (an env var, not stdin), and the env also carries `HERDR_ENV=1`,
  `HERDR_PLUGIN_ID`, `HERDR_PLUGIN_CONTEXT_JSON`, `HERDR_BIN_PATH`,
  `HERDR_PLUGIN_EVENT`, `HERDR_SOCKET_PATH`, `HERDR_PLUGIN_ROOT`,
  `HERDR_PLUGIN_CONFIG_DIR`, `HERDR_PLUGIN_STATE_DIR`, and the workspace, tab
  and pane ids when the context has them. PATH may be minimal.
- **herdr sets no hook timeout**, keeps at most 32 plugin commands in flight
  (runs beyond that are dropped) and caps output at 64 KB. So the hook bounds
  itself: a 5 s context for the work, plus up to 1 s each for giving a claim
  back and clearing its settle marker, so about 7 s at worst; and `working` / `idle` / `unknown` exit before any
  I/O.

## Verified in TKB-25

Read out of `herdr api schema --json` (protocol 22), `herdr --default-config`
and the herdr binary's own error-code strings, while building the dispatch dry
run. Everything below was checked against the running herdr, not remembered.

### No creation method takes a command

This is the finding that shapes the whole feature. **Nothing in herdr's API
starts a pane at a command of your choosing.** `worktree.create`,
`worktree.open` and `pane.split` all open a shell; `PaneSplitParams` carries
`cwd`, `direction`, `env`, `focus`, `ratio`, `right_click`, `target_pane_id`
and `workspace_id`, and no `command` or `argv` anywhere.

So a dispatch is necessarily two steps: make the worktree (which opens a pane
at a shell), then `agent.start` into that pane. Which is also why the second
step can fail with `agent_pane_busy` — the pane exists before its shell is at
a prompt — and why a retry, not an error, is the right answer to that code.

### The interface

| Method | Params | Reply |
|--------|--------|-------|
| `worktree.list` | `cwd?`, `workspace_id?`, `trust_repository` | `worktree_list`: `source` (required) + `worktrees[]` |
| `worktree.create` | `workspace_id?`, `cwd?`, `branch?`, `base?`, `path?`, `label?`, `focus` (default false), `trust_repository` | `worktree_created`: `workspace`, `tab`, `root_pane` (full `PaneInfo`), `worktree` — all required |
| `worktree.open` | the same minus `base` | `worktree_opened`: the above plus `already_open` |
| `agent.start` | `name`, `kind`, `pane_id` (all required), `args []string`, `timeout_ms?` | `agent_started`: `agent` + `argv` |
| `agent.prompt` | `target`, `text` (required), `wait?` (`{until[], timeout_ms}`) | — |

- `branch` is typed `string | null` but is **effectively required** on
  `worktree.create`: without it herdr answers "branch is required".
- `timeout_ms` must be **greater than 3000 and at most 300000**; omit it to
  take herdr's own default.
- `WorktreeInfo` is `path`, `is_bare`, `is_detached`, `is_prunable`,
  `is_linked_worktree`, `label` (required) plus `branch` and
  `open_workspace_id` (both nullable).
- `WorktreeSourceInfo` is `repo_key`, `repo_name`, `repo_root`,
  `source_checkout_path` (required) plus `source_workspace_id` (nullable).
- **`trust_repository` is a write.** tktban omits it from `worktree.list`, so
  listing stays a read; a pinned request-JSON test asserts it is not on the
  wire.

### Error codes

All of these are present in herdr's binary at protocol 22. The two a dispatch
must refuse outright rather than retry are marked.

| Code | Meaning |
|------|---------|
| `not_git_worktree` | **refuse** — the directory is not a checkout |
| `linked_worktree_source` | **refuse** — the source checkout is itself a linked worktree |
| `worktree_operation_in_progress` | another worktree operation is running |
| `worktree_create_failed` | the create itself failed |
| `stale_worktree_operation` | a previous operation was left half-done |
| `ambiguous_worktree_branch` | the branch matches more than one worktree |
| `worktree_not_found` | no such worktree |
| `agent_pane_busy` | the pane's shell is not at a prompt yet — retry |
| `agent_name_taken` | pick the next candidate name |
| `agent_blocked` | herdr refused to start this agent |
| `invalid_agent_name` | the name breaks the rule below |
| `invalid_agent_argument` | a bad entry in `args` |

`linked_worktree_source` also shows up without an error: a `worktree.list`
whose `source.source_checkout_path` appears in `worktrees[]` with
`is_linked_worktree: true` is herdr saying "you are inside a worktree
already", and tktban refuses on that too.

### Agent names

`^[a-z][a-z0-9_-]{0,31}$`, and unique among **live** agents. A ticket key
lowercases straight into one (`TKB-25` → `tkb-25`), which is what makes a
dispatched agent recognisable in herdr's own agent list; `agent_name_taken`
falls back to `tkb-25-2`, with the suffix sharing the 32-character budget.

### herdr owns worktree placement

`herdr --default-config` documents `[worktrees] directory`, defaulting to
`~/.herdr/worktrees`. tktban therefore sends **no `path`**, so a dispatched
worktree lands exactly where the ones a person makes by hand do.

### What tkt contributes

- `tkt cfg board.ownership --json` → `{"todo->in_progress":"agent",
  "in_progress->review":"agent","review->done":"agent"}`. The dispatch target
  is the first **agent**-owned transition out of the card's own role, ranked
  by board order; no role name is hard-coded.
- `tkt cfg vcs --json` → `branch_fmt` `"feature/{key-lower}-{slug}"`,
  `default_branch` `"main"`, `repo`. The branch is rendered in Go, not by tkt:
  `tkt cfg vcs.branch_fmt --ticket X` renders it without a slug and leaves a
  dangling separator (`feature/tkb-25-`), so the caller owns slugification
  anyway.
- A `branch_fmt` with no `{key-lower}` in it is refused up front. The card
  badge and the `o` jump both work by reading a pane's branch back through
  `KeysFromBranch`, so such an agent would be dispatched and then invisible.
- A target role that is not in `[board.roles]` is refused too: a config typo
  would otherwise transition a ticket into a lane no column can show.
- Both reads run under the dispatch's own 2 s budget
  (`tkt.WithContext` → `exec.CommandContext`), so a wedged `tkt` is killed
  rather than waited on. That matters because the board latches the `D` key
  while a preparation is in flight.

### Which herdr processes get refused a working directory

`HERDR_ENV=1` is set in **every** herdr pane, so it does not distinguish a
plugin process from a shell someone is typing in. The `HERDR_PLUGIN_*`
variables do: herdr sets them only for plugin processes. Only a plugin process
can be started in the plugin's own install checkout (the TKB-23 finding), so
only that one is refused a fall-back to its working directory — `tktban` run
by hand in a herdr terminal is where the person already is.
