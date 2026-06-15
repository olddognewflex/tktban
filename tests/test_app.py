"""App-level tests via Textual's headless pilot. These run the REAL tkt against
the fixture board (integration), proving the app mounts columns and that a write
action mutates the board through tkt verbs (status + history sidecar)."""
import asyncio
import pathlib
import shutil

from tktban.app import BanApp, ColumnWidget
from tktban.tkt import Tkt, TktError

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


def test_auto_on_config_state():
    # Defaults: on, 10s.
    a = BanApp(Tkt(config=FIX))
    assert a._auto_on is True and a._refresh_secs == 10.0
    # Explicit interval.
    assert BanApp(Tkt(config=FIX), refresh_interval=3)._refresh_secs == 3
    # Non-positive interval -> starts off, cadence falls back to 10.
    z = BanApp(Tkt(config=FIX), refresh_interval=0)
    assert z._auto_on is False and z._refresh_secs == 10.0
    # Explicit opt-out.
    assert BanApp(Tkt(config=FIX), auto_refresh=False)._auto_on is False


def test_auto_on_timer_created_and_toggles():
    async def go():
        app = BanApp(Tkt(config=FIX), refresh_interval=999)
        async with app.run_test() as pilot:
            await app.workers.wait_for_complete()
            await pilot.pause()
            states = [app._refresh_timer is not None, app._auto_on]
            app.action_toggle_auto()          # -> off
            await app.workers.wait_for_complete()
            await pilot.pause()
            states.append(app._auto_on)
            assert "auto off" in app.sub_title
            app.action_toggle_auto()          # -> on
            await app.workers.wait_for_complete()
            await pilot.pause()
            states.append(app._auto_on)
            return states, app.sub_title

    states, sub = _run(go())
    assert states == [True, True, False, True]
    assert "auto 999s" in sub


def test_auto_tick_skips_refresh_while_modal_open():
    from tktban.screens import FilterModal

    async def go():
        app = BanApp(Tkt(config=FIX), auto_refresh=False)
        async with app.run_test() as pilot:
            await app.workers.wait_for_complete()
            await pilot.pause()
            calls = []
            app.refresh_board = lambda: calls.append(1)   # spy on the worker call
            app._auto_tick()                              # no modal -> refreshes
            app.push_screen(FilterModal({}))
            await pilot.pause()
            app._auto_tick()                              # modal open -> skip
            await pilot.pause()
            return calls

    assert _run(go()) == [1]   # only the no-modal tick triggered a refresh


def test_focus_preserved_across_refresh():
    from tktban.app import CardItem

    async def go():
        app = BanApp(Tkt(config=FIX), auto_refresh=False)
        async with app.run_test() as pilot:
            await app.workers.wait_for_complete()
            await pilot.pause()
            # Focus a specific card.
            target = next(ci for ci in app.query(CardItem) if ci.card.key == "TKT-1")
            target.parent.focus()             # focus its ListView
            await pilot.pause()
            lv = app._focused_listview()
            lv.index = [getattr(i, "card", None) and i.card.key for i in lv.children].index("TKT-1")
            await pilot.pause()
            before = app._current_card().key
            # A refresh repaints every widget; focus must survive.
            app.refresh_board()
            await app.workers.wait_for_complete()
            await pilot.pause()
            after = app._current_card()
            return before, (after.key if after else None)

    before, after = _run(go())
    assert before == "TKT-1"
    assert after == "TKT-1"   # same card still focused after the repaint


def test_external_change_reflected_within_interval(tmp_path):
    from tktban.app import CardItem, ColumnWidget

    dst = tmp_path / ".sdlc"
    shutil.copytree(FIXDIR, dst)
    cfg = str(dst / "config.toml")

    def col_of(app, key):
        for cw in app.query(ColumnWidget):
            if any(getattr(ci, "card", None) and ci.card.key == key
                   for ci in cw.query(CardItem)):
                return cw.column.role
        return None

    async def go():
        app = BanApp(Tkt(config=cfg), refresh_interval=0.5)
        # Keep each refresh cheap so it finishes well inside the interval —
        # the per-card lane-time fan-out isn't what this test exercises.
        app.tkt.lane_time = lambda key, role: None
        async with app.run_test() as pilot:
            # Poll for first paint rather than awaiting workers: the interval
            # timer cancels in-flight refreshes (exclusive coalescing), which
            # would surface as WorkerCancelled if we awaited them.
            start = None
            for _ in range(25):
                await pilot.pause(0.1)
                start = col_of(app, "TKT-5")
                if start is not None:
                    break
            # External mutation (another process / CLI), not via the UI.
            Tkt(config=cfg).transition("TKT-5", "review")
            end = None
            for _ in range(30):
                await pilot.pause(0.2)
                end = col_of(app, "TKT-5")
                if end == "review":
                    break
            return start, end

    start, end = _run(go())
    assert start == "backlog"
    assert end == "review"   # auto-refresh picked up the external change, no manual r


def test_create_ticket_round_trips_description_acceptance_labels(tmp_path):
    # _create_ticket is the UI-free core — directly testable, no pilot needed.
    from tktban.screens import build_ticket_body

    dst = tmp_path / ".sdlc"
    shutil.copytree(FIXDIR, dst)
    cfg = str(dst / "config.toml")
    res = BanApp(Tkt(config=cfg))._create_ticket({
        "issue_type": "Story",
        "summary": "Created via creator",
        "priority": "High",
        "assignee": "",
        "body": build_ticket_body("A real description.", "crit a\ncrit b"),
        "labels": ["alpha", "beta"],
    })
    assert res["error"] is None and res["label_error"] == ""
    # Fixture has TKT-1..7, so the creator makes TKT-8.
    t = Tkt(config=cfg).view("TKT-8")
    assert t["summary"] == "Created via creator"
    assert t["description"] == "A real description."     # round-trips
    assert t["acceptance"] == ["crit a", "crit b"]        # parsed from body section
    assert set(t["labels"]) == {"alpha", "beta"}          # applied via follow-up edit


def test_create_ticket_reports_error_without_creating(tmp_path):
    dst = tmp_path / ".sdlc"
    shutil.copytree(FIXDIR, dst)
    cfg = str(dst / "config.toml")
    app = BanApp(Tkt(config=cfg))

    def boom(**kw):
        raise TktError("tkt create failed: bad type")

    app.tkt.create = boom
    res = app._create_ticket({
        "issue_type": "Nope", "summary": "x", "priority": "",
        "assignee": "", "body": "", "labels": [],
    })
    assert res["error"] and "bad type" in res["error"]    # surfaced, not raised
    assert not (dst / "board" / "TKT-8.md").exists()       # nothing created


def test_create_ticket_label_failure_keeps_the_ticket(tmp_path):
    dst = tmp_path / ".sdlc"
    shutil.copytree(FIXDIR, dst)
    cfg = str(dst / "config.toml")
    app = BanApp(Tkt(config=cfg))

    def boom_edit(*a, **k):
        raise TktError("edit failed: label rejected")

    app.tkt.edit = boom_edit
    res = app._create_ticket({
        "issue_type": "Task", "summary": "with labels", "priority": "",
        "assignee": "", "body": "", "labels": ["x"],
    })
    assert res["error"] is None                            # ticket WAS created
    assert "edit failed" in res["label_error"]             # label failure reported
    assert (dst / "board" / "TKT-8.md").is_file()          # not undone


def test_create_ticket_no_key_skips_label_edit(tmp_path):
    dst = tmp_path / ".sdlc"
    shutil.copytree(FIXDIR, dst)
    cfg = str(dst / "config.toml")
    app = BanApp(Tkt(config=cfg))
    app.tkt.create = lambda **kw: {}          # contract drift: no key
    edited = []
    app.tkt.edit = lambda *a, **k: edited.append((a, k))
    res = app._create_ticket({
        "issue_type": "Task", "summary": "s", "priority": "",
        "assignee": "", "body": "", "labels": ["x"],
    })
    assert res["error"] is None and res["key"] == ""
    assert res["label_error"] == "no key returned by create"
    assert edited == []                                    # never edits an empty key


def test_creator_modal_validates_then_creates(tmp_path):
    from textual.widgets import Input, Label, Select, TextArea

    from tktban.screens import CreateModal

    dst = tmp_path / ".sdlc"
    shutil.copytree(FIXDIR, dst)
    cfg = str(dst / "config.toml")

    async def go():
        app = BanApp(Tkt(config=cfg))
        # Roomy viewport so the tall create form's Create button is on-screen.
        async with app.run_test(size=(100, 60)) as pilot:
            await app.workers.wait_for_complete()
            await pilot.pause()
            app.action_new()                       # _open_creator worker -> push modal
            await app.workers.wait_for_complete()
            await pilot.pause()
            assert isinstance(app.screen, CreateModal)
            modal = app.screen
            types = list(modal.types)
            # Create with nothing -> validation error, modal stays open.
            await pilot.click("#create")
            await pilot.pause()
            err = str(modal.query_one("#create-error", Label).render())
            still_open = isinstance(app.screen, CreateModal)
            # Fill required fields, then submit (drive _submit directly — it's
            # what the Create button invokes; pilot.click is geometry-flaky once
            # the Select overlay is in play).
            modal.query_one("#type", Select).value = "Bug"
            modal.query_one("#summary", Input).value = "From the modal"
            modal.query_one("#description", TextArea).text = "modal desc"
            await pilot.pause()
            ok = modal._submit()                   # dispatches the create worker
            # Poll for the modal to close (the worker dismisses on success); don't
            # await workers — the trailing exclusive refresh can cancel and raise.
            for _ in range(30):
                await pilot.pause(0.05)
                if not isinstance(app.screen, CreateModal):
                    break
            closed = not isinstance(app.screen, CreateModal)
            return types, err, still_open, ok, closed

    types, err, still_open, ok, closed = _run(go())
    assert "Story" in types and "Bug" in types and "Task" in types   # from issue_types
    assert "Choose a type" in err                                    # validated inline
    assert still_open is True                                        # not closed on error
    assert ok is None                                                # create succeeded
    assert closed is True                                            # closed after success
    t = Tkt(config=cfg).view("TKT-8")
    assert t["summary"] == "From the modal" and t["type"] == "Bug"


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
