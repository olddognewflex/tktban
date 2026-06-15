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
