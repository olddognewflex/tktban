#!/usr/bin/env bash
# Launcher for the tktban overlay, run by the `open-board` herdr action.
#
#   no tktban pane in this workspace       -> open the overlay, focused
#   a tktban pane exists but isn't focused -> focus it
#   the focused pane is the tktban pane    -> close it (reopening is cheap)
#
# The board is opened with --cwd set to the focused pane's directory (else the
# workspace root) so `tktban --herdr` finds that project's .sdlc/config.toml.
# herdr passes the invocation context in HERDR_PLUGIN_CONTEXT_JSON.
#
# Panes are matched by label, which herdr sets from the manifest pane title.
# Focus and close go through `herdr plugin pane ...`, which refuses panes this
# plugin does not own, so a user pane that happens to be labelled "tktban" is
# never closed; the launcher then just opens a real board. Toggling needs
# python3; without it every press opens a board.
set -uo pipefail

herdr_bin="${HERDR_BIN_PATH:-herdr}"
plugin_id="${HERDR_PLUGIN_ID:-odnf.tktban}"
pane_title="tktban"

# Prints three NUL-terminated fields: cwd, workspace id, decision
# (OPEN | FOCUS <pane> | CLOSE <pane>). cwd and workspace are printed before the
# pane scan so a bad pane list can only cost the toggle, never the cwd.
plan() {
  local panes
  panes="$("$herdr_bin" pane list 2>/dev/null || true)"
  PANES="$panes" TITLE="$pane_title" python3 - <<'PY' 2>/dev/null
import json, os, sys
out = sys.stdout
try:
    ctx = json.loads(os.environ.get("HERDR_PLUGIN_CONTEXT_JSON") or "{}")
except Exception:
    ctx = {}
if not isinstance(ctx, dict):
    ctx = {}
cwd = ctx.get("focused_pane_cwd") or ctx.get("workspace_cwd") or ""
ws = ctx.get("workspace_id") or ""
out.write(f"{cwd if isinstance(cwd, str) else ''}\0{ws if isinstance(ws, str) else ''}\0")
out.flush()
decision = "OPEN"
try:
    data = json.loads(os.environ.get("PANES") or "{}")
    res = data.get("result", data) if isinstance(data, dict) else {}
    panes = res.get("panes", []) if isinstance(res, dict) else []
    if not isinstance(panes, list):
        panes = []
except Exception:
    panes = []
match = None
for p in panes:
    if not isinstance(p, dict) or not p.get("pane_id"):
        continue
    if ws and p.get("workspace_id") != ws:
        continue
    if p.get("label") != os.environ["TITLE"]:
        continue
    if match is None or (p.get("focused") and not match.get("focused")):
        match = p
if match is not None:
    decision = ("CLOSE " if match.get("focused") else "FOCUS ") + str(match["pane_id"])
out.write(decision + "\0")
PY
}

cwd=""; ws=""; decision="OPEN"
if command -v python3 >/dev/null 2>&1; then
  { IFS= read -r -d '' cwd; IFS= read -r -d '' ws; IFS= read -r -d '' decision; } < <(plan)
  [ -n "$decision" ] || decision="OPEN"
fi

# Focus/close only succeed on this plugin's own pane; on failure open a board.
case "$decision" in
  "FOCUS "*) "$herdr_bin" plugin pane focus "${decision#FOCUS }" >/dev/null 2>&1 && exit 0 ;;
  "CLOSE "*) "$herdr_bin" plugin pane close "${decision#CLOSE }" >/dev/null 2>&1 && exit 0 ;;
esac

args=(plugin pane open --plugin "$plugin_id" --entrypoint board --placement overlay --focus)
[ -n "$cwd" ] && args+=(--cwd "$cwd")
[ -n "$ws" ] && args+=(--workspace "$ws")
exec "$herdr_bin" "${args[@]}"
