#!/usr/bin/env bash
# Launcher for the tktban popup, run by the `open-board` herdr action.
#
#   no popup open            -> open the board popup over the active pane
#   the tktban popup is open -> close it (the key toggles the board)
#   another popup is open    -> leave it alone
#
# herdr allows one popup at a time, does not list popups in `pane list`, and
# gives them no pane id, so the launcher cannot target the board pane directly.
# Instead: herdr refuses the open with code ui_busy while any popup is up, and
# a running board holds an exclusive flock on $HERDR_PLUGIN_STATE_DIR/board.lock.
# Only when that lock is held is the open popup ours, and only then is herdr's
# popup.close (which closes whichever popup is open) sent.
#
# The board is opened with --cwd set to the focused pane's directory (else the
# workspace root) so `tktban --herdr` finds that project's .sdlc/config.toml
# and --select-from-cwd reads that project's branch. The context is forwarded
# in the environment too: plugin.pane.open carries cwd and env but no argv,
# and herdr sets HERDR_PLUGIN_CONTEXT_JSON for an action like this one, not
# necessarily for the pane it opens. Getting the directory wrong would open
# the wrong project's board, so when python3 is missing a plain-bash reader
# takes over rather than letting the pane start in the plugin's own install
# directory. The manifest declares the popup size; the CLI cannot, so no
# --placement here. python3 also talks to the socket; without it the key only
# opens and never toggles.
set -uo pipefail

herdr_bin="${HERDR_BIN_PATH:-herdr}"
plugin_id="${HERDR_PLUGIN_ID:-odnf.tktban}"
have_py=0
command -v python3 >/dev/null 2>&1 && have_py=1

ctx="${HERDR_PLUGIN_CONTEXT_JSON:-}"

# ctx_field KEY prints the string value of a top-level key of the context.
# The context is flat JSON of plain strings, so a bash match is enough; this
# is only the fallback for a host without python3, and a path holding a quote
# or a backslash reads as empty (no --cwd) rather than as the wrong path.
ctx_field() {
  [ -n "$ctx" ] || return 0
  if [[ $ctx =~ \"$1\"[[:space:]]*:[[:space:]]*\"([^\"\\]*)\" ]]; then
    printf %s "${BASH_REMATCH[1]}"
  fi
}

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

[ -n "$cwd" ] || cwd="$(ctx_field focused_pane_cwd)"
[ -n "$cwd" ] || cwd="$(ctx_field workspace_cwd)"

args=(plugin pane open --plugin "$plugin_id" --entrypoint board --focus)
[ -n "$cwd" ] && args+=(--cwd "$cwd")
[ -n "$ctx" ] && args+=(--env "HERDR_PLUGIN_CONTEXT_JSON=$ctx")

out="$("$herdr_bin" "${args[@]}" 2>&1)"
status=$?
if [ $status -eq 0 ]; then
  printf '%s\n' "$out"
  exit 0
fi

# A popup is already open: close it only if it is the running board.
case "$out" in
  *'"ui_busy"'*)
    if [ "$have_py" = 1 ] && [ -n "${HERDR_SOCKET_PATH:-}" ]; then
      python3 - <<'PY'
import errno, fcntl, json, os, socket, sys

def board_running():
    state = os.environ.get("HERDR_PLUGIN_STATE_DIR") or ""
    if not state:
        return False
    try:
        fd = os.open(os.path.join(state, "board.lock"), os.O_RDONLY | os.O_NOFOLLOW)
    except OSError:
        return False  # no lock file: no board has run
    try:
        fcntl.flock(fd, fcntl.LOCK_SH | fcntl.LOCK_NB)
    except OSError as e:
        return e.errno in (errno.EWOULDBLOCK, errno.EAGAIN)  # held: board is up
    finally:
        os.close(fd)
    return False  # we got the lock, so no board holds it

if not board_running():
    print("tktban: another popup is open; leaving it alone", file=sys.stderr)
    sys.exit(1)

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
