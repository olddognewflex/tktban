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
        if c.assignee:
            yield Label(f"@{c.assignee}", classes="card-meta")


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
        ("f", "filter", "Filter"),
        ("v", "view", "View"),
        ("e", "edit", "Edit"),
        ("m", "move", "Move"),
        ("c", "comment", "Comment"),
        ("n", "new", "New"),
        ("q", "quit", "Quit"),
    ]

    def __init__(self, tkt: Tkt) -> None:
        self.tkt = tkt
        self._roles: dict[str, str] = {}
        self._filter: dict[str, str] = {"assignee": "", "prefix": ""}
        super().__init__()

    def compose(self) -> ComposeResult:
        yield Header()
        yield Horizontal(id="board")
        yield Footer()

    def on_mount(self) -> None:
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
        columns = build_board(roles, tickets)
        self.call_from_thread(self._render, roles, columns)

    def _render(self, roles: dict[str, str], columns: list[Column]) -> None:
        self._roles = roles
        board = self.query_one("#board", Horizontal)
        board.remove_children()
        for col in columns:
            board.mount(ColumnWidget(col))
        count = sum(len(c.cards) for c in columns)
        self.sub_title = f"{count} tickets{self._filter_label()}"

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

    # ---- actions -----------------------------------------------------------

    def action_refresh(self) -> None:
        self.refresh_board()

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
