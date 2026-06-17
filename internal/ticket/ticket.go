// Package ticket holds the pure, UI-free helpers behind the create/edit/viewer
// modals: composing a create body, diffing an edit into tkt.Edit kwargs, and
// rendering a ticket as markdown. Kept dependency-light (only model + tkt types)
// so it is unit-testable without a TUI.
package ticket

import (
	"fmt"
	"slices"
	"strings"

	"github.com/olddognewflex/tktban/internal/model"
	"github.com/olddognewflex/tktban/internal/tkt"
)

// BuildBody composes a `tkt create --body` string from a free-text description
// and acceptance criteria (one per line). Criteria become a `## Acceptance`
// markdown section so they round-trip into the ticket's acceptance list.
func BuildBody(description, acceptanceText string) string {
	body := strings.TrimSpace(description)
	var criteria []string
	for line := range strings.SplitSeq(acceptanceText, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			criteria = append(criteria, s)
		}
	}
	if len(criteria) > 0 {
		if body != "" {
			body += "\n\n"
		}
		body += "## Acceptance\n"
		for i, c := range criteria {
			if i > 0 {
				body += "\n"
			}
			body += "- " + c
		}
	}
	return body
}

// Fields are the editable ticket fields, used to diff an edit.
type Fields struct {
	Summary     string
	Description string
	Priority    string
	Assignee    string
	Labels      []string
}

// ComputeEdit diffs orig against edited and returns only the changed kwargs as a
// tkt.EditOpts (with a flag for whether anything changed). An unchanged field is
// left nil, so editing only priority never rewrites the description. Description
// maps to Body; label changes split into AddLabels/RemoveLabels.
func ComputeEdit(orig, edited Fields) (tkt.EditOpts, bool) {
	var opts tkt.EditOpts
	changed := false
	if edited.Summary != orig.Summary {
		opts.Summary = ref(edited.Summary)
		changed = true
	}
	if edited.Description != orig.Description {
		opts.Body = ref(edited.Description)
		changed = true
	}
	if edited.Priority != orig.Priority {
		opts.Priority = ref(edited.Priority)
		changed = true
	}
	if edited.Assignee != orig.Assignee {
		opts.Assignee = ref(edited.Assignee)
		changed = true
	}
	add := minus(edited.Labels, orig.Labels)
	remove := minus(orig.Labels, edited.Labels)
	if len(add) > 0 {
		opts.AddLabels = add
		changed = true
	}
	if len(remove) > 0 {
		opts.RemoveLabels = remove
		changed = true
	}
	return opts, changed
}

// Markdown renders a normalized ticket dict (from `tkt view --json`) as a
// markdown document. Pure, so it is testable without a TUI.
func Markdown(t model.Ticket) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s\n\n", getStr(t, "key"), getStr(t, "summary"))

	typ := orDash(getStr(t, "type"))
	if tclass := getStr(t, "type_class"); tclass != "" {
		typ = fmt.Sprintf("%s (%s)", typ, tclass)
	}
	meta := []string{
		"**Type:** " + typ,
		fmt.Sprintf("**Status:** %s (%s)", orDash(getStr(t, "status")), getStr(t, "status_role")),
		"**Assignee:** " + orDash(getStr(t, "assignee")),
		"**Priority:** " + orDash(getStr(t, "priority")),
	}
	b.WriteString(strings.Join(meta, "  ·  "))
	b.WriteString("\n\n")

	if blocked := getList(t, "blocked_by"); len(blocked) > 0 {
		b.WriteString("## Blockers\n")
		for _, item := range blocked {
			m, _ := item.(map[string]any)
			mark := "🔴"
			if r, _ := m["resolved"].(bool); r {
				mark = "✅"
			}
			key, _ := m["key"].(string)
			fmt.Fprintf(&b, "- %s %s\n", mark, key)
		}
		b.WriteString("\n")
	}

	desc := strings.TrimSpace(getStr(t, "description"))
	if desc == "" {
		desc = "_(none)_"
	}
	b.WriteString("## Description\n" + desc + "\n\n")

	if acc := getStrList(t, "acceptance"); len(acc) > 0 {
		b.WriteString("## Acceptance\n")
		for _, a := range acc {
			b.WriteString("- " + a + "\n")
		}
		b.WriteString("\n")
	}

	if labels := getStrList(t, "labels"); len(labels) > 0 {
		b.WriteString("**Labels:** " + strings.Join(labels, ", "))
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ---- helpers ----

func ref(s string) *string { return &s }

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// minus returns the items in a that are not in b (order-preserving).
func minus(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func getStr(t model.Ticket, key string) string {
	if v, ok := t[key].(string); ok {
		return v
	}
	return ""
}

func getList(t model.Ticket, key string) []any {
	if v, ok := t[key].([]any); ok {
		return v
	}
	return nil
}

// getStrList reads a list field as []string, tolerating both []string and the
// []any that JSON decoding produces.
func getStrList(t model.Ticket, key string) []string {
	switch v := t[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
