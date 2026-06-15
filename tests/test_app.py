"""App-level tests via Textual's headless pilot. These run the REAL tkt against
the fixture board (integration), proving the app mounts columns and that a write
action mutates the board through tkt verbs (status + history sidecar)."""
import asyncio
import pathlib
import shutil

from tktban.app import BanApp, ColumnWidget
from tktban.tkt import Tkt

FIXDIR = pathlib.Path(__file__).parent / "fixtures" / ".sdlc"
FIX = str(FIXDIR / "config.toml")


def _run(coro):
    return asyncio.run(coro)


def test_board_mounts_columns_in_role_order():
    async def go():
        app = BanApp(Tkt(config=FIX))
        async with app.run_test() as pilot:
            await app.workers.wait_for_complete()
            await pilot.pause()
            return [c.column.lane for c in app.query(ColumnWidget)]

    assert _run(go()) == [
        "Backlog", "To Do", "In Progress", "In Review", "Done", "Blocked"
    ]


def test_viewer_opens_with_full_ticket():
    from tktban.screens import ViewerModal

    async def go():
        app = BanApp(Tkt(config=FIX))
        async with app.run_test() as pilot:
            await app.workers.wait_for_complete()
            await pilot.pause()
            # TKT-6 has an unresolved blocker — exercises that branch too.
            worker = app._open_viewer("TKT-6")
            await worker.wait()
            await pilot.pause()
            top = app.screen
            return type(top).__name__, getattr(top, "ticket", {}).get("key")

    name, key = _run(go())
    assert name == "ViewerModal" and key == "TKT-6"


def test_write_dispatch_mutates_board_through_verbs(tmp_path):
    # Copy the fixture to a temp .sdlc so the committed fixture stays pristine.
    dst = tmp_path / ".sdlc"
    shutil.copytree(FIXDIR, dst)
    app = BanApp(Tkt(config=str(dst / "config.toml")))

    # The write path the modal callbacks drive — exercised synchronously here.
    app._apply_write("transition", ("TKT-5", "review"))
    app._apply_write("comment", ("TKT-5", "looks good"))
    app._apply_write("create", {"issue_type": "Task", "summary": "new ticket"})

    md = (dst / "board" / "TKT-5.md").read_text()
    assert "status: In Review" in md             # transition rewrote status
    assert "looks good" in md                     # comment appended
    history = (dst / "state" / "TKT-5.history.jsonl").read_text()
    assert "In Review" in history                 # history sidecar recorded the move
    assert (dst / "board" / "TKT-8.md").is_file()   # create made the next key


def test_editor_opens_prefilled():
    from textual.widgets import Input

    async def go():
        app = BanApp(Tkt(config=FIX))
        async with app.run_test() as pilot:
            await app.workers.wait_for_complete()
            await pilot.pause()
            worker = app._open_editor("TKT-3")
            await worker.wait()
            await pilot.pause()
            top = app.screen
            return type(top).__name__, top.query_one("#summary", Input).value

    name, summary = _run(go())
    assert name == "EditModal"
    assert summary == "Document the verb contract"  # TKT-3 fixture summary, pre-filled


def test_filter_narrows_board_to_matching_cards():
    async def go():
        app = BanApp(Tkt(config=FIX))
        async with app.run_test() as pilot:
            await app.workers.wait_for_complete()
            await pilot.pause()
            # alex owns exactly TKT-3 (In Review) and TKT-6 (Blocked).
            app._filter = {"assignee": "alex", "prefix": ""}
            app.refresh_board()
            await app.workers.wait_for_complete()
            await pilot.pause()
            cards = [c.key for col in app.query(ColumnWidget) for c in col.column.cards]
            return sorted(cards), app.sub_title

    cards, sub_title = _run(go())
    assert cards == ["TKT-3", "TKT-6"]
    assert "filter: @alex" in sub_title


def test_filter_modal_apply_drives_board_end_to_end():
    from textual.widgets import Input

    from tktban.screens import FilterModal

    async def go():
        app = BanApp(Tkt(config=FIX))
        async with app.run_test() as pilot:
            await app.workers.wait_for_complete()
            await pilot.pause()
            app.action_filter()
            await pilot.pause()
            assert isinstance(app.screen, FilterModal)
            app.screen.query_one("#assignee", Input).value = "alex"
            await pilot.click("#apply")
            await app.workers.wait_for_complete()
            await pilot.pause()
            cards = [c.key for col in app.query(ColumnWidget) for c in col.column.cards]
            return sorted(cards), app._filter, isinstance(app.screen, FilterModal)

    cards, flt, still_open = _run(go())
    assert cards == ["TKT-3", "TKT-6"]                 # filter applied
    assert flt == {"assignee": "alex", "prefix": ""}   # state stored
    assert not still_open                              # modal dismissed


def test_filter_modal_clear_and_cancel_contracts():
    from tktban.screens import FilterModal

    async def go():
        app = BanApp(Tkt(config=FIX))
        out = {}
        async with app.run_test() as pilot:
            await app.workers.wait_for_complete()
            await pilot.pause()
            # Clear returns an empty pair (distinct from cancel's None).
            app.push_screen(FilterModal({"assignee": "x", "prefix": "y"}),
                            lambda r: out.__setitem__("clear", r))
            await pilot.pause()
            await pilot.click("#clear")
            await pilot.pause()
            # Cancel returns None.
            app.push_screen(FilterModal({}), lambda r: out.__setitem__("cancel", r))
            await pilot.pause()
            await pilot.click("#cancel")
            await pilot.pause()
        return out

    out = _run(go())
    assert out["clear"] == {"assignee": "", "prefix": ""}
    assert out["cancel"] is None


def test_lane_time_badge_populated_and_graceful():
    from tktban.app import CardItem

    async def go():
        app = BanApp(Tkt(config=FIX))
        # Deterministic: stub the read-only lane-time call (TKT-1 has time,
        # everything else has no history -> None -> no badge).
        app.tkt.lane_time = lambda key, role: {"human": "2h 5m"} if key == "TKT-1" else None
        async with app.run_test() as pilot:
            await app.workers.wait_for_complete()
            await pilot.pause()
            return {ci.card.key: ci.card.lane_human for ci in app.query(CardItem)}

    cards = _run(go())
    assert cards["TKT-1"] == "2h 5m"   # populated from tkt
    assert cards["TKT-3"] == ""        # None -> graceful, no badge


def test_lane_time_read_only_does_not_record_worklog(tmp_path):
    import re

    from tktban.app import CardItem

    dst = tmp_path / ".sdlc"
    shutil.copytree(FIXDIR, dst)
    # Give TKT-7 (To Do) a history entry so read-only lane-time can compute.
    state = dst / "state"
    state.mkdir(parents=True, exist_ok=True)
    (state / "TKT-7.history.jsonl").write_text(
        '{"ts": "2026-06-15T00:00:00Z", "from": "", "to": "To Do"}\n'
    )
    app = BanApp(Tkt(config=str(dst / "config.toml")))

    async def go():
        async with app.run_test() as pilot:
            await app.workers.wait_for_complete()
            await pilot.pause()
            return {ci.card.key: ci.card.lane_human for ci in app.query(CardItem)}

    cards = _run(go())
    assert re.fullmatch(r"\d+h \d+m", cards["TKT-7"])     # a real duration rendered
    assert not (state / "worklog.jsonl").exists()         # read-only: recorded nothing


def test_edit_dispatch_mutates_fields_through_verb(tmp_path):
    dst = tmp_path / ".sdlc"
    shutil.copytree(FIXDIR, dst)
    app = BanApp(Tkt(config=str(dst / "config.toml")))

    app._apply_write("edit", ("TKT-3", {
        "summary": "Reworded summary",
        "priority": "Highest",
        "add_labels": ["docs"],
    }))

    md = (dst / "board" / "TKT-3.md").read_text()
    assert "# Reworded summary" in md       # summary heading rewritten
    assert "priority: Highest" in md        # frontmatter updated
    assert "docs" in md                     # label added
    assert "status: In Review" in md        # status untouched by edit (TKT-3 fixture lane)
