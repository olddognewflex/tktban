package ticket

import (
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/olddognewflex/tktban/internal/model"
)

func TestBuildBodyDescriptionOnly(t *testing.T) {
	if got := BuildBody("Hello world.", ""); got != "Hello world." {
		t.Fatalf("got %q", got)
	}
}

func TestBuildBodyWithAcceptanceSection(t *testing.T) {
	want := "Desc.\n\n## Acceptance\n- one\n- two"
	if got := BuildBody("Desc.", "one\ntwo"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildBodyAcceptanceOnly(t *testing.T) {
	want := "## Acceptance\n- a\n- b"
	if got := BuildBody("", " a \n\n b "); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildBodyEmptyIsEmpty(t *testing.T) {
	if got := BuildBody("   ", "  \n  "); got != "" {
		t.Fatalf("got %q", got)
	}
}

func orig(over Fields) Fields {
	base := Fields{
		Summary:     "old summary",
		Description: "old description",
		Priority:    "Low",
		Assignee:    "alice",
		Labels:      []string{"a", "b"},
	}
	if over.Summary != "" {
		base.Summary = over.Summary
	}
	if over.Description != "" {
		base.Description = over.Description
	}
	if over.Priority != "" {
		base.Priority = over.Priority
	}
	if over.Labels != nil {
		base.Labels = over.Labels
	}
	return base
}

func TestComputeEditNoChanges(t *testing.T) {
	o := orig(Fields{})
	if _, changed := ComputeEdit(o, o); changed {
		t.Fatal("identical fields should report no change")
	}
}

func TestComputeEditSingleField(t *testing.T) {
	o := orig(Fields{})
	n := o
	n.Summary = "new summary"
	opts, changed := ComputeEdit(o, n)
	if !changed || opts.Summary == nil || *opts.Summary != "new summary" {
		t.Fatalf("opts = %+v changed=%v", opts, changed)
	}
	if opts.Body != nil || opts.Priority != nil || opts.Assignee != nil {
		t.Fatal("only summary should be set")
	}
}

func TestComputeEditDescriptionMapsToBody(t *testing.T) {
	o := orig(Fields{})
	n := o
	n.Description = "new"
	opts, changed := ComputeEdit(o, n)
	if !changed || opts.Body == nil || *opts.Body != "new" {
		t.Fatalf("description must map to Body, got %+v", opts)
	}
	if opts.Summary != nil {
		t.Fatal("summary unchanged")
	}
}

func TestComputeEditClearingFieldIsChange(t *testing.T) {
	o := orig(Fields{})
	n := o
	n.Assignee = ""
	opts, changed := ComputeEdit(o, n)
	if !changed || opts.Assignee == nil || *opts.Assignee != "" {
		t.Fatalf("clearing assignee must be a change, got %+v", opts)
	}
}

func TestComputeEditLabelDiff(t *testing.T) {
	o := orig(Fields{Labels: []string{"a", "b"}})
	n := o
	n.Labels = []string{"b", "c"} // drop a, add c
	opts, changed := ComputeEdit(o, n)
	if !changed {
		t.Fatal("label diff should change")
	}
	if !reflect.DeepEqual(opts.AddLabels, []string{"c"}) {
		t.Fatalf("add = %v", opts.AddLabels)
	}
	if !reflect.DeepEqual(opts.RemoveLabels, []string{"a"}) {
		t.Fatalf("remove = %v", opts.RemoveLabels)
	}
}

func fullTicket(over map[string]any) model.Ticket {
	base := model.Ticket{
		"key": "TKT-1", "type": "Story", "type_class": "full_sdlc",
		"summary": "Add OAuth login", "status": "In Review", "status_role": "review",
		"assignee": "alice", "priority": "High", "description": "Body text here.",
		"acceptance": []any{"criterion one", "criterion two"},
		"labels":     []any{"api", "auth"}, "blocked_by": []any{},
	}
	maps.Copy(base, over)
	return base
}

func TestMarkdownIncludesAllSections(t *testing.T) {
	md := Markdown(fullTicket(nil))
	for _, want := range []string{
		"# TKT-1 — Add OAuth login",
		"**Type:** Story (full_sdlc)",
		"**Status:** In Review (review)",
		"**Assignee:** alice",
		"**Priority:** High",
		"## Description\nBody text here.",
		"## Acceptance", "- criterion one", "- criterion two",
		"**Labels:** api, auth",
	} {
		if !strings.HasPrefix(md, "# TKT-1") {
			t.Fatalf("must start with header, got %q", md[:20])
		}
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q in:\n%s", want, md)
		}
	}
}

func TestMarkdownBlockersWithMarks(t *testing.T) {
	md := Markdown(fullTicket(map[string]any{"blocked_by": []any{
		map[string]any{"key": "TKT-2", "resolved": true},
		map[string]any{"key": "TKT-3", "resolved": false},
	}}))
	for _, want := range []string{"## Blockers", "✅ TKT-2", "🔴 TKT-3"} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q in:\n%s", want, md)
		}
	}
}

func TestMarkdownEmptyDescriptionPlaceholder(t *testing.T) {
	md := Markdown(fullTicket(map[string]any{"description": ""}))
	if !strings.Contains(md, "## Description\n_(none)_") {
		t.Fatalf("missing placeholder in:\n%s", md)
	}
}

func TestMarkdownOptionalSectionsOmitted(t *testing.T) {
	md := Markdown(fullTicket(map[string]any{
		"acceptance": []any{}, "labels": []any{}, "blocked_by": []any{},
	}))
	if strings.Contains(md, "## Acceptance") || strings.Contains(md, "## Blockers") || strings.Contains(md, "**Labels:**") {
		t.Fatalf("optional sections should be omitted:\n%s", md)
	}
	if !strings.Contains(md, "## Description") {
		t.Fatal("description always present")
	}
}

func TestMarkdownMissingFieldsDoNotPanic(t *testing.T) {
	md := Markdown(model.Ticket{"key": "X-1"})
	if !strings.HasPrefix(md, "# X-1 —") || !strings.Contains(md, "## Description") {
		t.Fatalf("sparse ticket render = %q", md)
	}
}
