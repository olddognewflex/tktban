"""Machine-local UI preferences for tktban, persisted as human-readable TOML.

This is tktban UI state ONLY (theme, and room for more later) — never board
data, and never written through tkt. It lives under the standard config dir so
it survives across runs. Loading never raises: a missing or corrupt file falls
back to defaults so the app always starts.
"""
from __future__ import annotations

import os
import tomllib
from pathlib import Path

# Known UI preferences and their defaults. Extend this as more state is
# persisted (e.g. auto-refresh interval, default filters); unknown keys read
# from disk are ignored so an old/newer file never breaks startup.
DEFAULTS: dict = {"theme": "textual-dark"}


def default_path() -> Path:
    """`$XDG_CONFIG_HOME/tktban/settings.toml`, falling back to
    `~/.config/tktban/settings.toml`."""
    base = os.environ.get("XDG_CONFIG_HOME")
    root = Path(base) if base else Path.home() / ".config"
    return root / "tktban" / "settings.toml"


def load(path: Path) -> dict:
    """Read settings layered over DEFAULTS. A missing, unreadable, or corrupt
    file (or one whose top level isn't a table) yields a copy of DEFAULTS — this
    never raises, so a bad file can't stop tktban from starting. Only known keys
    with scalar values are taken from disk."""
    data = dict(DEFAULTS)
    try:
        with open(path, "rb") as fh:
            loaded = tomllib.load(fh)
    except (OSError, tomllib.TOMLDecodeError):
        return data
    if isinstance(loaded, dict):
        for key, value in loaded.items():
            if key in DEFAULTS and isinstance(value, (str, int, float, bool)):
                data[key] = value
    return data


# TOML basic-string escapes (https://toml.io/en/v1.0.0#string). Other control
# chars are \u-encoded; everything else is written literally.
_TOML_ESCAPES = {
    "\\": "\\\\", '"': '\\"', "\b": "\\b", "\t": "\\t",
    "\n": "\\n", "\f": "\\f", "\r": "\\r",
}


def _escape_toml_str(value: str) -> str:
    out = []
    for ch in value:
        if ch in _TOML_ESCAPES:
            out.append(_TOML_ESCAPES[ch])
        elif ord(ch) < 0x20 or ord(ch) == 0x7F:
            out.append(f"\\u{ord(ch):04x}")
        else:
            out.append(ch)
    return "".join(out)


def _dump_toml(data: dict) -> str:
    """Serialize a flat dict of scalar values to TOML. Supports str/bool/int/
    float — enough for UI prefs; nested tables aren't needed here. Strings are
    fully escaped to the TOML basic-string spec so any value round-trips back
    through tomllib."""
    lines = []
    for key, value in data.items():
        if isinstance(value, bool):          # bool before int (bool is an int)
            rendered = "true" if value else "false"
        elif isinstance(value, (int, float)):
            rendered = repr(value)
        else:
            rendered = f'"{_escape_toml_str(str(value))}"'
        lines.append(f"{key} = {rendered}")
    return ("\n".join(lines) + "\n") if lines else ""


def save(path: Path, data: dict) -> None:
    """Write settings as TOML, creating the config dir if needed. Only known
    keys are written, so transient/unknown state never leaks to disk."""
    path.parent.mkdir(parents=True, exist_ok=True)
    persisted = {k: v for k, v in data.items() if k in DEFAULTS}
    path.write_text(_dump_toml(persisted))
