"""Shared test fixtures."""
import pytest


@pytest.fixture(autouse=True)
def isolate_user_config(tmp_path_factory, monkeypatch):
    """Point XDG_CONFIG_HOME at a throwaway dir for every test so nothing ever
    reads or writes the developer's real ~/.config/tktban/settings.toml. Tests
    that care about the path semantics override this with their own monkeypatch.
    """
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path_factory.mktemp("xdg")))
