"""Settings persistence — pure file I/O, no Textual/tkt needed."""
from pathlib import Path

from tktban import settings


def test_default_path_uses_xdg(monkeypatch):
    monkeypatch.setenv("XDG_CONFIG_HOME", "/tmp/xdgcfg")
    assert settings.default_path() == Path("/tmp/xdgcfg/tktban/settings.toml")


def test_default_path_falls_back_to_home(monkeypatch):
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    monkeypatch.setattr(Path, "home", classmethod(lambda cls: Path("/home/x")))
    assert settings.default_path() == Path("/home/x/.config/tktban/settings.toml")


def test_load_missing_returns_defaults(tmp_path):
    assert settings.load(tmp_path / "nope.toml") == settings.DEFAULTS


def test_load_corrupt_returns_defaults(tmp_path):
    p = tmp_path / "settings.toml"
    p.write_text("this is = not valid toml ===")
    assert settings.load(p) == settings.DEFAULTS


def test_load_takes_known_scalar_keys_only(tmp_path):
    p = tmp_path / "settings.toml"
    p.write_text('theme = "nord"\nbogus = "ignored"\n')
    out = settings.load(p)
    assert out["theme"] == "nord"
    assert "bogus" not in out                 # unknown keys dropped


def test_save_load_round_trip_is_human_readable(tmp_path):
    p = tmp_path / "sub" / "settings.toml"     # parent dir created by save
    settings.save(p, {"theme": "gruvbox"})
    assert p.is_file()
    assert 'theme = "gruvbox"' in p.read_text()
    assert settings.load(p)["theme"] == "gruvbox"


def test_save_persists_only_known_keys(tmp_path):
    p = tmp_path / "settings.toml"
    settings.save(p, {"theme": "nord", "transient": "x"})
    text = p.read_text()
    assert "theme" in text and "transient" not in text


def test_dump_toml_scalar_types():
    out = settings._dump_toml({"s": "hi", "b": True, "n": 5})
    assert 's = "hi"' in out
    assert "b = true" in out
    assert "n = 5" in out


def test_dump_toml_escapes_special_chars_round_trip():
    import tomllib

    # Newlines/tabs/quotes/backslashes and a control char must escape so the
    # output is valid TOML that parses back to the original string.
    value = 'a"b\\c\nd\te\x01f'
    out = settings._dump_toml({"theme": value})
    assert tomllib.loads(out)["theme"] == value
