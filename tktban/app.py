"""The Textual app: a kanban board over tkt.

Read path: a thread worker shells out to tkt (roles + list_all), builds the model,
and repaints columns. Write path: each action opens a modal, then runs the matching
tkt verb in a worker and refreshes. All tkt calls run off the UI thread so the board
never freezes on a subprocess.
"""
from __future__ import annotations

from textual import work
from textual.app import App, ComposeResult
from textual.containers import Horizontal, Vertical
from textual.timer import Timer
from textual.widgets import Footer, Header, Label, ListItem, ListView

from .model import Card, Column, build_board, filter_tickets
from .screens import (
    CommentModal,
    CreateModal,
    EditModal,
    FilterModal,
    MoveModal,
    ViewerModal,
)
from .tkt import Tkt, TktError


class CardItem(ListItem):
    """One card in a column. Carries its Card so actions can read the selection."""

    def __init__(self, card: Card) -> None:
        self.card = card
        super().__init__()

    def compose(self) -> ComposeResult:
        c = self.card
        prio = f"[{c.priority}] " if c.priority else ""
        badge = f"  ⛔{c.blocker_count}" if c.blocker_count else ""
        yield Label(f"{prio}{c.key}{badge}", classes="card-head")
        yield Label(c.summary, classes="card-summary")
        meta = []
        if c.assignee:
            meta.append(f"@{c.assignee}")
        if c.lane_human:
            meta.append(f"⏱ {c.lane_human}")
        if meta:
            yield Label("  ".join(meta), classes="card-meta")


class ColumnWidget(Vertical):
    """A titled column wrapping a ListView of CardItems."""

    def __init__(self, column: Column) -> None:
        self.column = column
        super().__init__()

    def compose(self) -> ComposeResult:
        yield Label(f"{self.column.lane}  ({len(self.column.cards)})", classes="col-title")
        yield ListView(*[CardItem(c) for c in self.column.cards])


class BanApp(App):
    CSS_PATH = "styles.tcss"
    TITLE = "tktban"

    BINDINGS = [
        ("r", "refresh", "Refresh"),
        ("a", "toggle_auto", "Auto-refresh"),
        ("f", "filter", "Filter"),
        ("v", "view", "View"),
        ("e", "edit", "Edit"),
        ("m", "move", "Move"),
        ("c", "comment", "Comment"),
        ("n", "new", "New"),
        ("q", "quit", "Quit"),
    ]

    def __init__(self, tkt: Tkt, refresh_interval: float = 10.0,
                 auto_refresh: bool = True) -> None:
        self.tkt = tkt
        self._roles: dict[str, str] = {}
        self._filter: dict[str, str] = {"assignee": "", "prefix": ""}
        # NB: names are prefixed `_auto_*`/`_refresh_*` and deliberately avoid
        # Textual App's own `auto_refresh` attribute, which would clobber them.
        # Cadence (seconds) is always positive; `_auto_on` is the on/off state,
        # toggled at runtime with `a`. A non-positive interval starts disabled
        # but still toggles on at the 10s default.
        self._refresh_secs: float = refresh_interval if refresh_interval > 0 else 10.0
        self._auto_on: bool = auto_refresh and refresh_interval > 0
        self._refresh_timer: Timer | None = None
        super().__init__()

    def compose(self) -> ComposeResult:
        yield Header()
        yield Horizontal(id="board")
        yield Footer()

    def on_mount(self) -> None:
        self.refresh_board()
        # One repeating timer; pause/resume is the toggle. refresh_board is an
        # exclusive worker, so a tick that lands while one is running coalesces.
        self._refresh_timer = self.set_interval(self._refresh_secs, self._auto_tick)
        if not self._auto_on:
            self._refresh_timer.pause()

    def _auto_tick(self) -> None:
        """Interval callback (UI thread). Skip the repaint while a modal is open
        so an auto-refresh never rebuilds the board under a dialog or steals
        focus from a field the user is editing. The next tick picks it up once
        the modal closes."""
        if len(self.screen_stack) <= 1:
            self.refresh_board()

    # ---- read / render -----------------------------------------------------

    @work(thread=True, exclusive=True)
    def refresh_board(self) -> None:
        try:
            roles = self.tkt.roles()
            tickets = self.tkt.list_all()
        except TktError as e:
            self.call_from_thread(self.notify, str(e), severity="error", timeout=10)
            return
        tickets = filter_tickets(tickets, **self._filter)
        self._attach_lane_time(tickets)
        columns = build_board(roles, tickets)
        self.call_from_thread(self._render, roles, columns)

    def _attach_lane_time(self, tickets: list[dict]) -> None:
        """Annotate each (visible) ticket with its read-only time-in-current-lane.

        Cost: one `tkt lane-time --read-only` subprocess per visible card (the
        verb contract has no batch form), so refresh is O(visible cards) in
        process spawns. Fine for the local markdown backend; for a large remote
        board prefer filtering first. Runs in the read worker, so the UI never
        blocks. A card with no lane history simply gets no badge; a genuine tkt
        failure is surfaced once and aborts the rest of the pass rather than
        blanking every card silently."""
        for t in tickets:
            key, role = t.get("key", ""), t.get("status_role", "")
            if not key or not role:
                continue
            try:
                wl = self.tkt.lane_time(key, role)
            except TktError as e:
                self.call_from_thread(
                    self.notify, f"time-in-lane unavailable: {e}",
                    severity="warning", timeout=8,
                )
                return
            if wl and wl.get("human"):
                t["lane_human"] = wl["human"]

    def _render(self, roles: dict[str, str], columns: list[Column]) -> None:
        self._roles = roles
        # Remember where the user was so an (auto-)refresh repaint doesn't yank
        # their place; restore after the new widgets have mounted.
        role, key = self._focused_location()
        board = self.query_one("#board", Horizontal)
        board.remove_children()
        for col in columns:
            board.mount(ColumnWidget(col))
        self._update_subtitle()
        if role is not None:
            self.call_after_refresh(self._restore_focus, role, key)

    def _update_subtitle(self) -> None:
        """Repaint the sub-title (ticket count + active filter + auto state) from
        the mounted columns. Cheap and UI-thread only — no tkt calls — so it can
        be used to reflect a state change (e.g. an auto toggle) without a full
        board reload."""
        count = sum(len(cw.column.cards) for cw in self.query(ColumnWidget))
        self.sub_title = f"{count} tickets{self._filter_label()}{self._auto_label()}"

    def _auto_label(self) -> str:
        if self._auto_on:
            return f"  ·  auto {int(self._refresh_secs)}s"
        return "  ·  auto off"

    def _filter_label(self) -> str:
        """Human suffix describing the active filter, e.g.
        '  ·  filter: @raymond, TKB'. Empty when no filter is set."""
        parts = []
        if self._filter["assignee"]:
            parts.append(f"@{self._filter['assignee']}")
        if self._filter["prefix"]:
            parts.append(self._filter["prefix"])
        return ("  ·  filter: " + ", ".join(parts)) if parts else ""

    # ---- selection helpers -------------------------------------------------

    def _focused_listview(self) -> ListView | None:
        node = self.focused
        while node is not None and not isinstance(node, ListView):
            node = node.parent
        return node

    def _current_card(self) -> Card | None:
        lv = self._focused_listview()
        if lv is None:
            return None
        return getattr(lv.highlighted_child, "card", None)

    def _focused_location(self) -> tuple[str | None, str | None]:
        """The (column role, card key) currently focused, for restoring across a
        repaint. (None, None) when nothing is focused (e.g. first mount)."""
        lv = self._focused_listview()
        if lv is None:
            return (None, None)
        node = lv
        while node is not None and not isinstance(node, ColumnWidget):
            node = node.parent
        role = node.column.role if node is not None else None
        card = getattr(lv.highlighted_child, "card", None)
        return (role, card.key if card else None)

    def _restore_focus(self, role: str, key: str | None) -> None:
        """Re-focus the column that had focus, and the same card if it still
        exists. A vanished card falls back to its column; a vanished column
        leaves focus at the default."""
        for cw in self.query(ColumnWidget):
            if cw.column.role != role:
                continue
            lv = cw.query_one(ListView)
            if key is not None:
                for i, item in enumerate(lv.children):
                    if getattr(item, "card", None) and item.card.key == key:
                        lv.index = i
                        break
            lv.focus()
            return

    # ---- actions -----------------------------------------------------------

    def action_refresh(self) -> None:
        self.refresh_board()

    def action_toggle_auto(self) -> None:
        """Toggle auto-refresh at runtime (pauses/resumes the interval timer)."""
        self._auto_on = not self._auto_on
        if self._refresh_timer is not None:
            self._refresh_timer.resume() if self._auto_on else self._refresh_timer.pause()
        self.notify(
            f"Auto-refresh on ({int(self._refresh_secs)}s)" if self._auto_on
            else "Auto-refresh off"
        )
        self._update_subtitle()   # reflect the new state without a full board reload

    def action_filter(self) -> None:
        def done(result: dict | None) -> None:
            if result is not None:  # None = cancelled; {} pair = clear
                self._filter = result
                self.refresh_board()

        self.push_screen(FilterModal(dict(self._filter)), done)

    def action_view(self) -> None:
        card = self._current_card()
        if card is None:
            self.notify("Select a card first", severity="warning")
            return
        self._open_viewer(card.key)

    @work(thread=True)
    def _open_viewer(self, key: str) -> None:
        """Fetch the full ticket off-thread, then show it read-only."""
        try:
            ticket = self.tkt.view(key)
        except TktError as e:
            self.call_from_thread(self.notify, str(e), severity="error", timeout=10)
            return
        self.call_from_thread(self.push_screen, ViewerModal(ticket))

    def action_edit(self) -> None:
        card = self._current_card()
        if card is None:
            self.notify("Select a card first", severity="warning")
            return
        self._open_editor(card.key)

    @work(thread=True)
    def _open_editor(self, key: str) -> None:
        """Fetch the full ticket off-thread (to pre-fill), then open the editor."""
        try:
            ticket = self.tkt.view(key)
        except TktError as e:
            self.call_from_thread(self.notify, str(e), severity="error", timeout=10)
            return
        self.call_from_thread(self._push_editor, ticket)

    def _push_editor(self, ticket: dict) -> None:
        def done(changes: dict | None) -> None:
            if changes:
                self._do_write("edit", (ticket["key"], changes), f"Edited {ticket['key']}")
            elif changes is not None:  # {} -> saved with nothing changed
                self.notify("No changes made")

        self.push_screen(EditModal(ticket), done)

    def action_move(self) -> None:
        card = self._current_card()
        if card is None:
            self.notify("Select a card first", severity="warning")
            return

        def done(role: str | None) -> None:
            if role:
                self._do_write(
                    "transition", (card.key, role), f"Moved {card.key} → {role}"
                )

        self.push_screen(MoveModal(card, list(self._roles.keys())), done)

    def action_comment(self) -> None:
        card = self._current_card()
        if card is None:
            self.notify("Select a card first", severity="warning")
            return

        def done(body: str | None) -> None:
            if body:
                self._do_write("comment", (card.key, body), f"Commented on {card.key}")

        self.push_screen(CommentModal(card), done)

    def action_new(self) -> None:
        def done(payload: dict | None) -> None:
            if payload:
                self._do_write("create", payload, "Created ticket")

        self.push_screen(CreateModal(), done)

    def _apply_write(self, verb: str, payload) -> None:
        """Dispatch a write to the matching tkt verb. Sync + pure dispatch
        (no UI), so it is directly testable; the worker below wraps it."""
        if verb == "transition":
            self.tkt.transition(*payload)
        elif verb == "comment":
            self.tkt.comment(*payload)
        elif verb == "create":
            self.tkt.create(**payload)
        elif verb == "edit":
            key, changes = payload
            self.tkt.edit(key, **changes)
        else:  # pragma: no cover - guards against a typo'd verb
            raise ValueError(f"unknown write verb: {verb}")

    @work(thread=True)
    def _do_write(self, verb: str, payload, success_msg: str) -> None:
        """Run a write verb off-thread, toast the result, then refresh."""
        try:
            self._apply_write(verb, payload)
        except TktError as e:
            self.call_from_thread(self.notify, str(e), severity="error", timeout=10)
            return
        self.call_from_thread(self.notify, success_msg)
        self.call_from_thread(self.refresh_board)
