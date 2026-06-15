"""Tests for the pure helpers behind the viewer and editor modals."""
from tktban.screens import compute_edit, ticket_markdown


def _orig(**over):
    base = {
        "summary": "old summary",
        "description": "old description",
        "priority": "Low",
        "assignee": "raymond",
        "labels": ["a", "b"],
    }
    base.update(over)
    return base


def test_compute_edit_no_changes_returns_empty():
    o = _orig()
    assert compute_edit(o, dict(o)) == {}


def test_compute_edit_single_field():
    o = _orig()
    new = dict(o, summary="new summary")
    assert compute_edit(o, new) == {"summary": "new summary"}


def test_compute_edit_description_maps_to_body():
    o = _orig()
    assert compute_edit(o, dict(o, description="new")) == {"body": "new"}


def test_compute_edit_priority_and_assignee():
    o = _orig()
    new = dict(o, priority="High", assignee="alex")
    assert compute_edit(o, new) == {"priority": "High", "assignee": "alex"}


def test_compute_edit_clearing_a_field_is_a_change():
    o = _orig()
    assert compute_edit(o, dict(o, assignee="")) == {"assignee": ""}


def test_compute_edit_label_diff():
    o = _orig(labels=["a", "b"])
    new = dict(o, labels=["b", "c"])  # drop a, add c
    out = compute_edit(o, new)
    assert out == {"add_labels": ["c"], "remove_labels": ["a"]}


def _ticket(**over):
    base = {
        "key": "TKT-1",
        "type": "Story",
        "type_class": "full_sdlc",
        "summary": "Add OAuth login",
        "status": "In Review",
        "status_role": "review",
        "assignee": "raymond",
        "priority": "High",
        "description": "Body text here.",
        "acceptance": ["criterion one", "criterion two"],
        "labels": ["api", "auth"],
        "blocked_by": [],
    }
    base.update(over)
    return base


def test_includes_header_meta_description_acceptance_labels():
    md = ticket_markdown(_ticket())
    assert md.startswith("# TKT-1 — Add OAuth login")
    assert "**Type:** Story (full_sdlc)" in md
    assert "**Status:** In Review (review)" in md
    assert "**Assignee:** raymond" in md
    assert "**Priority:** High" in md
    assert "## Description\nBody text here." in md
    assert "## Acceptance" in md
    assert "- criterion one" in md and "- criterion two" in md
    assert "**Labels:** api, auth" in md


def test_blockers_rendered_with_resolved_marks():
    md = ticket_markdown(_ticket(blocked_by=[
        {"key": "TKT-2", "resolved": True},
        {"key": "TKT-3", "resolved": False},
    ]))
    assert "## Blockers" in md
    assert "✅ TKT-2" in md
    assert "🔴 TKT-3" in md


def test_empty_description_shows_placeholder():
    md = ticket_markdown(_ticket(description=""))
    assert "## Description\n_(none)_" in md


def test_optional_sections_omitted_when_empty():
    md = ticket_markdown(_ticket(acceptance=[], labels=[], blocked_by=[]))
    assert "## Acceptance" not in md
    assert "## Blockers" not in md
    assert "**Labels:**" not in md
    # description section is always present
    assert "## Description" in md


def test_missing_fields_do_not_raise():
    # a sparse dict (e.g. a minimal backend) must still render
    md = ticket_markdown({"key": "X-1"})
    assert md.startswith("# X-1 —")
    assert "## Description" in md
