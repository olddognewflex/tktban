"""Modal dialogs for the three write actions. Each dialog only *collects input*
and returns it via dismiss(); the actual tkt verb call happens in the app (so all
mutations stay funnelled through tkt.py). A dialog that returns None = cancelled.
"""
from __future__ import annotations

from textual.app import ComposeResult
from textual.containers import Horizontal, Vertical
from textual.screen import ModalScreen
from textual.widgets import Button, Input, Label, ListItem, ListView

from .model import Card


class MoveModal(ModalScreen[str | None]):
    """Pick a target role. Returns the role key (e.g. 'review') or None."""

    BINDINGS = [("escape", "cancel", "Cancel")]

    def __init__(self, card: Card, roles: list[str]) -> None:
        self.card = card
        self.roles = roles
        super().__init__()

    def compose(self) -> ComposeResult:
        with Vertical(id="dialog"):
            yield Label(f"Move {self.card.key} to…")
            yield ListView(
                *[ListItem(Label(r), id=f"role-{i}") for i, r in enumerate(self.roles)]
            )

    def on_list_view_selected(self, event: ListView.Selected) -> None:
        idx = int(event.item.id.removeprefix("role-"))
        self.dismiss(self.roles[idx])

    def action_cancel(self) -> None:
        self.dismiss(None)


class CommentModal(ModalScreen[str | None]):
    """Single-line comment. Returns the body or None."""

    BINDINGS = [("escape", "cancel", "Cancel")]

    def __init__(self, card: Card) -> None:
        self.card = card
        super().__init__()

    def compose(self) -> ComposeResult:
        with Vertical(id="dialog"):
            yield Label(f"Comment on {self.card.key}")
            yield Input(placeholder="comment… (enter to submit, esc to cancel)", id="body")

    def on_input_submitted(self, event: Input.Submitted) -> None:
        self.dismiss(event.value.strip() or None)

    def action_cancel(self) -> None:
        self.dismiss(None)


class CreateModal(ModalScreen[dict | None]):
    """New-ticket form. Returns kwargs for Tkt.create(...) or None."""

    BINDINGS = [("escape", "cancel", "Cancel")]

    def compose(self) -> ComposeResult:
        with Vertical(id="dialog"):
            yield Label("New ticket")
            yield Input(placeholder="type (e.g. Task, Story, Bug)", id="type")
            yield Input(placeholder="summary", id="summary")
            yield Input(placeholder="priority (optional)", id="priority")
            yield Input(placeholder="assignee (optional, blank = you)", id="assignee")
            with Horizontal(id="buttons"):
                yield Button("Create", variant="success", id="create")
                yield Button("Cancel", id="cancel")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        if event.button.id == "cancel":
            self.dismiss(None)
            return
        issue_type = self.query_one("#type", Input).value.strip()
        summary = self.query_one("#summary", Input).value.strip()
        if not issue_type or not summary:
            self.notify("type and summary are required", severity="warning")
            return
        self.dismiss({
            "issue_type": issue_type,
            "summary": summary,
            "priority": self.query_one("#priority", Input).value.strip(),
            "assignee": self.query_one("#assignee", Input).value.strip(),
        })

    def action_cancel(self) -> None:
        self.dismiss(None)
