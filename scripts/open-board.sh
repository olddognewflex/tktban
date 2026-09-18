#!/usr/bin/env bash
# Launcher for the tktban popup, run by the `open-board` herdr action.
#
#   no popup open  -> open the board popup over the active pane
#   a popup open   -> close it (the key toggles the board)
#
# herdr allows one popup at a time and does not list popups in `pane list`, so
# "is the board open?" is answered by the open call itself: herdr refuses with
# code ui_busy while a popup is up, and the launcher then closes it. The close
# is herdr's popup.close, which closes whichever popup is open; pressing the key
# over another plugin's popup therefore closes that popup.
#
# The board is opened with --cwd set to the focused pane's directory (else the
# workspace root) so `tktban --herdr` finds that project's .sdlc/config.toml.
# The manifest declares the popup size; the CLI cannot, so no --placement here.
# python3 reads the herdr context and talks to the socket; without it the key
# only opens.
set -uo pipefail

herdr_bin="${HERDR_BIN_PATH:-herdr}"
plugin_id="${HERDR_PLUGIN_ID:-odnf.tktban}"
have_py=0
command -v python3 >/dev/null 2>&1 && have_py=1

cwd=""
if [ "$have_py" = 1 ]; then
  cwd="$(python3 - <<'PY' 2>/dev/null
import json, os
try:
    ctx = json.loads(os.environ.get("HERDR_PLUGIN_CONTEXT_JSON") or "{}")
except Exception:
    ctx = {}
if not isinstance(ctx, dict):
    ctx = {}
cwd = ctx.get("focused_pane_cwd") or ctx.get("workspace_cwd") or ""
print(cwd if isinstance(cwd, str) else "", end="")
PY
)"
fi

args=(plugin pane open --plugin "$plugin_id" --entrypoint board --focus)
[ -n "$cwd" ] && args+=(--cwd "$cwd")

out="$("$herdr_bin" "${args[@]}" 2>&1)"
status=$?
if [ $status -eq 0 ]; then
  printf '%s\n' "$out"
  exit 0
fi

# A popup is already open: toggle it closed.
case "$out" in
  *'"ui_busy"'*)
    if [ "$have_py" = 1 ] && [ -n "${HERDR_SOCKET_PATH:-}" ]; then
      python3 - <<'PY'
import json, os, socket, sys
s = socket.socket(socket.AF_UNIX)
s.settimeout(5)
s.connect(os.environ["HERDR_SOCKET_PATH"])
s.sendall((json.dumps({"id": "tktban:close", "method": "popup.close", "params": {}}) + "\n").encode())
reply = s.makefile().readline()
print(reply.strip())
# Closed by us, or already gone (e.g. q pressed in between): both mean closed.
sys.exit(0 if ('"result"' in reply or '"popup_not_open"' in reply) else 1)
PY
      exit $?
    fi
    ;;
esac

printf '%s\n' "$out" >&2
exit $status
