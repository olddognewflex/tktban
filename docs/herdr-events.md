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
must not trust payload order. Two rules follow:

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
  tktban calls it once when the board opens and turns live status off on any
  other protocol, instead of reading `session.snapshot`.
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
  `hotfix/{key-lower}-{slug}`.
- `HERDR_SOCKET_PATH` is set in plugin pane environments (seen on a running
  `overlay` plugin pane), not just in hooks.
