"""Pure board model — no I/O, no Textual. Turns tkt's normalized ticket dicts
into ordered columns of cards. Kept dependency-free so it is trivially testable.
"""
from __future__ import annotations

from dataclasses import dataclass, field

# tkt stores priority as a free string and sorts it lexicographically
# (core/query.py JqlSubset), which is wrong semantically ("Medium" > "Highest").
# A board must rank priority by meaning, so tktban owns the order. Higher = more
# urgent; unknown/empty sinks to 0.
PRIORITY_RANK = {
    "Highest": 5,
    "High": 4,
    "Medium": 3,
    "Low": 2,
    "Lowest": 1,
}

UNMAPPED = "(unmapped)"


def priority_rank(priority: str) -> int:
    return PRIORITY_RANK.get(priority, 0)


@dataclass
class Card:
    key: str
    summary: str = ""
    assignee: str = ""
    priority: str = ""
    status_role: str = ""
    blocker_count: int = 0

    @classmethod
    def from_ticket(cls, d: dict) -> "Card":
        blocked = d.get("blocked_by") or []
        blocker_count = sum(1 for b in blocked if not b.get("resolved"))
        return cls(
            key=d.get("key", ""),
            summary=d.get("summary", ""),
            assignee=d.get("assignee", ""),
            priority=d.get("priority", ""),
            status_role=d.get("status_role", ""),
            blocker_count=blocker_count,
        )

    def sort_key(self) -> tuple[int, str]:
        # priority DESC (negate rank), then key ASC.
        return (-priority_rank(self.priority), self.key)


@dataclass
class Column:
    role: str          # canonical role key, or UNMAPPED
    lane: str          # provider's literal lane label (display title)
    cards: list[Card] = field(default_factory=list)


def key_prefix(key: str) -> str:
    """The project prefix of a ticket key — the part before the first '-'
    ('TKB-1' -> 'TKB'). Returns the whole key if there is no '-'."""
    return key.split("-", 1)[0]


def filter_tickets(
    tickets: list[dict], assignee: str = "", prefix: str = ""
) -> list[dict]:
    """Narrow a ticket list by assignee and/or key prefix. Both filters are
    case-insensitive and optional; an empty string disables that filter, so
    no arguments returns the list unchanged. Pure (no I/O) so it is testable
    apart from build_board.

    - assignee: exact match against the ticket's `assignee`.
    - prefix: matches the ticket key's project prefix ('TKB' matches 'TKB-1').
    """
    a = assignee.strip().lower()
    p = prefix.strip().lower()
    if not a and not p:
        return tickets
    out = []
    for t in tickets:
        if a and (t.get("assignee") or "").lower() != a:
            continue
        if p and key_prefix(t.get("key") or "").lower() != p:
            continue
        out.append(t)
    return out


def build_board(roles: dict[str, str], tickets: list[dict]) -> list[Column]:
    """Group tickets into columns in `roles` insertion order.

    `roles` is the ordered role→lane map from `tkt cfg board.roles --json`.
    Each card lands in the column whose role == its `status_role`. Tickets whose
    role isn't configured go into a trailing UNMAPPED column (only if any exist),
    so nothing is silently dropped. Each column is sorted by priority then key.
    """
    columns = [Column(role=role, lane=lane) for role, lane in roles.items()]
    by_role = {col.role: col for col in columns}
    unmapped = Column(role=UNMAPPED, lane=UNMAPPED)

    for d in tickets:
        card = Card.from_ticket(d)
        (by_role.get(card.status_role) or unmapped).cards.append(card)

    for col in columns:
        col.cards.sort(key=Card.sort_key)
    if unmapped.cards:
        unmapped.cards.sort(key=Card.sort_key)
        columns.append(unmapped)

    return columns
