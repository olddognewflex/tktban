"""tktban — a Textual kanban TUI for the tkt board.

tktban renders a tkt board as kanban columns and performs move/comment/create
through tkt verbs. It speaks ONLY the tkt CLI verb contract (shell-out, JSON out)
and never imports tkt internals or re-parses a backend's storage — so the same TUI
works against any backend tkt supports (markdown, Jira, GitHub, ...).
"""

__version__ = "0.1.0"
