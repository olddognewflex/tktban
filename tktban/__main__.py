"""Entry point: `tktban` (board) and `tktban doctor`."""
from __future__ import annotations

import argparse
import sys

from .tkt import Tkt


def _doctor(tkt: Tkt) -> int:
    ok_all = True
    for name, ok, detail in tkt.doctor():
        mark = "ok  " if ok else "FAIL"
        line = f"[{mark}] {name}"
        if detail:
            line += f" — {detail}"
        print(line)
        ok_all = ok_all and ok
    if not ok_all:
        print("\ntktban needs every check above to pass. "
              "The board reads all tickets via a `[queries].all` query.")
    return 0 if ok_all else 1


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog="tktban", description="Textual kanban board for tkt")
    p.add_argument("--config", help="path to .sdlc/config.toml (else tkt auto-discovers)")
    p.add_argument("command", nargs="?", default="board", choices=["board", "doctor"],
                   help="board (default) launches the TUI; doctor validates setup")
    args = p.parse_args(argv)

    tkt = Tkt(config=args.config)
    if args.command == "doctor":
        return _doctor(tkt)

    from .app import BanApp  # lazy: keeps `doctor` import-light
    BanApp(tkt).run()
    return 0


if __name__ == "__main__":
    sys.exit(main())
