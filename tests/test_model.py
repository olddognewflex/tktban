"""Board model tests — pure functions, no tkt/Textual needed."""
from tktban.model import UNMAPPED, Card, build_board, priority_rank

ROLES = {
    "backlog": "Backlog",
    "todo": "To Do",
    "in_progress": "In Progress",
    "review": "In Review",
    "done": "Done",
    "blocked": "Blocked",
}


def ticket(key, role, priority="", assignee="", blocked_by=None):
    return {
        "key": key,
        "summary": f"summary {key}",
        "assignee": assignee,
        "priority": priority,
        "status_role": role,
        "blocked_by": blocked_by or [],
    }


def test_priority_rank_order():
    assert priority_rank("Highest") > priority_rank("High") > priority_rank("Medium")
    assert priority_rank("Medium") > priority_rank("Low") > priority_rank("Lowest")
    assert priority_rank("") == 0
    assert priority_rank("Bogus") == 0


def test_card_blocker_count_counts_only_unresolved():
    d = ticket("TKT-1", "todo", blocked_by=[
        {"key": "TKT-2", "resolved": True},
        {"key": "TKT-3", "resolved": False},
        {"key": "TKT-4", "resolved": False},
    ])
    card = Card.from_ticket(d)
    assert card.blocker_count == 2


def test_columns_follow_role_order():
    cols = build_board(ROLES, [])
    assert [c.role for c in cols] == list(ROLES.keys())
    assert [c.lane for c in cols] == list(ROLES.values())


def test_grouping_by_status_role():
    tickets = [
        ticket("TKT-1", "todo"),
        ticket("TKT-2", "done"),
        ticket("TKT-3", "todo"),
    ]
    cols = {c.role: c for c in build_board(ROLES, tickets)}
    assert {c.key for c in cols["todo"].cards} == {"TKT-1", "TKT-3"}
    assert [c.key for c in cols["done"].cards] == ["TKT-2"]
    assert cols["backlog"].cards == []


def test_sort_priority_desc_then_key_asc():
    tickets = [
        ticket("TKT-3", "todo", priority="Low"),
        ticket("TKT-1", "todo", priority="Highest"),
        ticket("TKT-2", "todo", priority="Highest"),
        ticket("TKT-4", "todo", priority=""),
    ]
    cols = {c.role: c for c in build_board(ROLES, tickets)}
    order = [c.key for c in cols["todo"].cards]
    # Highest first (TKT-1, TKT-2 by key), then Low, then empty.
    assert order == ["TKT-1", "TKT-2", "TKT-3", "TKT-4"]


def test_unmapped_role_bucketed_not_dropped():
    tickets = [ticket("TKT-1", "todo"), ticket("TKT-7", "Archived")]
    cols = build_board(ROLES, tickets)
    assert cols[-1].role == UNMAPPED
    assert [c.key for c in cols[-1].cards] == ["TKT-7"]


def test_no_unmapped_column_when_all_mapped():
    cols = build_board(ROLES, [ticket("TKT-1", "todo")])
    assert all(c.role != UNMAPPED for c in cols)
    assert len(cols) == len(ROLES)
