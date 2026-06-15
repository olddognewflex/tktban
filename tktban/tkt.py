"""The only place in tktban that knows tkt exists.

A thin wrapper around the `tkt` CLI: every method shells out to a verb and parses
its `--json` output. tktban never imports tkt's `core/`/`adapters/` and never reads
a backend's storage directly — this module is the entire coupling surface, and it
is the verb contract, nothing more. Point tkt at any backend and tktban follows.

Binary resolution: `TKT_BIN` env var, else `tkt` on PATH.
Config: an optional explicit path passed through as `--config` (else tkt auto-discovers
via `$TKT_CONFIG` / nearest `.sdlc/config.toml`).
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess

# tkt's typed exit codes (core/errors.py) → human labels.
EXIT_LABELS = {
    2: "config error",
    3: "provider error",
    4: "not found",
    64: "usage error",
}


class TktError(Exception):
    """A tkt invocation failed (nonzero exit, missing binary, or bad JSON)."""

    def __init__(self, message: str, *, exit_code: int | None = None, stderr: str = ""):
        super().__init__(message)
        self.exit_code = exit_code
        self.stderr = stderr


class Tkt:
    def __init__(self, config: str | None = None, binary: str | None = None):
        self.config = config
        self.binary = binary or os.environ.get("TKT_BIN", "tkt")

    # ---- internals ---------------------------------------------------------

    def _env(self) -> dict | None:
        # Pass config via TKT_CONFIG rather than --config: tkt's global --config
        # placed before the verb is clobbered by the subparser default (an
        # argparse parent-parser quirk), and the env var works uniformly for
        # every verb without positional-ordering pitfalls.
        if not self.config:
            return None
        return {**os.environ, "TKT_CONFIG": self.config}

    def _run(self, args: list[str], *, as_json: bool = False):
        cmd = [self.binary] + args
        try:
            proc = subprocess.run(cmd, capture_output=True, text=True, env=self._env())
        except FileNotFoundError as e:
            raise TktError(
                f"tkt binary not found: '{self.binary}'. "
                f"Put tkt on PATH or set TKT_BIN."
            ) from e
        if proc.returncode != 0:
            label = EXIT_LABELS.get(proc.returncode, f"exit {proc.returncode}")
            stderr = (proc.stderr or "").strip()
            raise TktError(
                f"tkt {' '.join(args)} failed ({label}): {stderr or '(no stderr)'}",
                exit_code=proc.returncode,
                stderr=stderr,
            )
        if as_json:
            try:
                return json.loads(proc.stdout)
            except json.JSONDecodeError as e:
                raise TktError(f"tkt {' '.join(args)} returned invalid JSON: {e}") from e
        return proc.stdout.strip()

    # ---- read verbs --------------------------------------------------------

    def roles(self) -> dict[str, str]:
        """Ordered role→lane map (column order). `tkt cfg board.roles --json`."""
        return self._run(["cfg", "board.roles", "--json"], as_json=True)

    def list_all(self) -> list[dict]:
        """Every ticket on the board. Requires a `[queries].all` query (see doctor)."""
        return self._run(["list", "--query", "all", "--json"], as_json=True)

    def view(self, key: str) -> dict:
        return self._run(["view", key, "--json"], as_json=True)

    def issue_types(self) -> dict:
        """{"full_sdlc": [...], "deliverable": [...]} — hints the create form."""
        return self._run(["cfg", "issue_types", "--json"], as_json=True)

    def lane_time(self, key: str, role: str) -> dict | None:
        """Read-only time-in-lane for `role` via `tkt lane-time --read-only` — a
        Worklog-shaped dict ({key, role, lane, seconds, human, ...}). Read-only,
        so it never records a worklog.

        Returns None ONLY for the benign "this ticket has never been in that
        lane" case (no entry in the provider's history/changelog) — the board
        just omits the badge. Any other failure (missing binary, config or
        provider/network error) is re-raised so the caller can surface it rather
        than silently blanking every card."""
        out = self.lane_time_batch([(key, role)])
        return out.get(key)

    def lane_time_batch(self, items: list[tuple[str, str]]) -> dict[str, dict | None]:
        """Batch read-only time-in-lane for all (key, role) pairs.

        Returns a dict keyed by key. Entries for tickets with no history in the
        requested lane map to None; genuine errors raise."""
        if not items:
            return {}
        pairs = ",".join(f"{key}:{role}" for key, role in items)
        try:
            result = self._run(
                ["lane-time", "--keys", pairs, "--read-only", "--json"],
                as_json=True,
            )
        except TktError as e:
            blob = (e.stderr or str(e)).lower()
            if any(s in blob for s in ("no entry", "history", "changelog")):
                return {key: None for key, _ in items}
            raise
        if len(result) != len(items):
            raise TktError(
                f"lane_time_batch: tkt returned {len(result)} entries for {len(items)} inputs"
            )
        return {
            entry.get("key", key): entry
            for (key, _), entry in zip(items, result)
        }

    # ---- write verbs (mutations go through tkt so history/worklog stay correct) ----

    def transition(self, key: str, role: str) -> None:
        self._run(["transition", key, role])

    def comment(self, key: str, body: str) -> None:
        self._run(["comment", key, body])

    def create(
        self,
        issue_type: str,
        summary: str,
        priority: str = "",
        assignee: str = "",
        body: str = "",
    ) -> dict:
        args = ["create", "--type", issue_type, "--summary", summary]
        if priority:
            args += ["--priority", priority]
        if assignee:
            args += ["--assignee", assignee]
        if body:
            args += ["--body", body]
        return self._run(args + ["--json"], as_json=True)

    def edit(
        self,
        key: str,
        summary: str | None = None,
        body: str | None = None,
        priority: str | None = None,
        assignee: str | None = None,
        add_labels: list[str] | None = None,
        remove_labels: list[str] | None = None,
    ) -> dict:
        """Edit content/fields via `tkt edit`. Only non-None args are sent; an
        empty string is a real value (e.g. clear the assignee)."""
        args = ["edit", key]
        if summary is not None:
            args += ["--summary", summary]
        if body is not None:
            args += ["--body", body]
        if priority is not None:
            args += ["--priority", priority]
        if assignee is not None:
            args += ["--assignee", assignee]
        for lbl in add_labels or []:
            args += ["--add-label", lbl]
        for lbl in remove_labels or []:
            args += ["--remove-label", lbl]
        return self._run(args + ["--json"], as_json=True)

    # ---- diagnostics -------------------------------------------------------

    def doctor(self) -> list[tuple[str, bool, str]]:
        """(name, ok, detail) checks: binary on PATH, config readable, `all` query."""
        checks: list[tuple[str, bool, str]] = []

        found = shutil.which(self.binary) is not None or os.path.isfile(self.binary)
        checks.append(
            ("tkt binary", found, self.binary if found else f"'{self.binary}' not found; set TKT_BIN")
        )
        if not found:
            return checks

        try:
            roles = self.roles()
            checks.append(("board.roles readable", bool(roles),
                           f"{len(roles)} roles" if roles else "no roles configured"))
        except TktError as e:
            checks.append(("board.roles readable", False, str(e)))
            return checks

        try:
            self.list_all()
            checks.append(("'all' query present", True, "tkt list --query all OK"))
        except TktError:
            checks.append(("'all' query present", False,
                           "add to [queries]:  all = 'ORDER BY key ASC'"))
        return checks
