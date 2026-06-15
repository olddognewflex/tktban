"""Tests for the pure markdown builder behind the read-only ticket viewer."""
from tktban.screens import ticket_markdown


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
