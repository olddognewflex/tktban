package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/olddognewflex/tktban/internal/model"
	"github.com/olddognewflex/tktban/internal/tkt"
)

var fixedToday = time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)

func TestDateFieldStepClampsAndKeepsDayValid(t *testing.T) {
	// Day segment clamps within the month (Feb 2026, 28 days).
	f := newDateField("2026-02-28", fixedToday)
	f.seg = 2
	f.step(1)
	if f.value() != "2026-02-28" {
		t.Fatalf("day over-month = %q, want 2026-02-28", f.value())
	}
	// Month step re-clamps the day: Jan 31 -> Feb -> 28.
	g := newDateField("2026-01-31", fixedToday)
	g.seg = 1
	g.step(1)
	if g.value() != "2026-02-28" {
		t.Fatalf("month step day clamp = %q, want 2026-02-28", g.value())
	}
	// Leap year keeps Feb 29.
	h := newDateField("2024-01-29", fixedToday)
	h.seg = 1
	h.step(1)
	if h.value() != "2024-02-29" {
		t.Fatalf("leap clamp = %q, want 2024-02-29", h.value())
	}
	// Stepping the year off a leap day re-clamps Feb 29 -> Feb 28.
	k := newDateField("2024-02-29", fixedToday)
	k.seg = 0
	k.step(1)
	if k.value() != "2025-02-28" {
		t.Fatalf("year leap re-clamp = %q, want 2025-02-28", k.value())
	}
}

func TestDateFieldUnsetStepAdoptsAndClear(t *testing.T) {
	f := newDateField("", fixedToday) // unset, seeded with today
	if f.value() != "" {
		t.Fatalf("unset value = %q, want empty", f.value())
	}
	f.step(1) // year segment +1, becomes set
	if f.value() != "2027-06-18" {
		t.Fatalf("adopted value = %q, want 2027-06-18", f.value())
	}
	f.clear()
	if f.value() != "" {
		t.Fatal("clear did not unset")
	}
}

// Keyboard navigation: tab between rows, h/l move segment, k/j step, c clears.
func TestDateModalKeyboardNav(t *testing.T) {
	card := model.Card{Key: "TKB-1", Due: "2026-06-01"}
	dm := newDateModal(card, fixedToday)
	if dm.focus != 0 {
		t.Fatalf("focus = %d, want 0", dm.focus)
	}
	nm, _ := dm.Update(key("k")) // step due year +1
	dm = nm.(dateModal)
	if dm.fields[0].value() != "2027-06-01" {
		t.Fatalf("k step = %q", dm.fields[0].value())
	}
	nm, _ = dm.Update(key("l")) // move to month segment
	dm = nm.(dateModal)
	nm, _ = dm.Update(key("j")) // step month -1 -> clamps at 1 (June->May actually)
	dm = nm.(dateModal)
	if dm.fields[0].value() != "2027-05-01" {
		t.Fatalf("l then j = %q, want 2027-05-01", dm.fields[0].value())
	}
	nm, _ = dm.Update(key("c")) // clear due
	dm = nm.(dateModal)
	if dm.fields[0].value() != "" {
		t.Fatal("c did not clear due")
	}
	// "t" sets the focused (cleared) field to the modal's captured clock.
	nm, _ = dm.Update(key("t"))
	dm = nm.(dateModal)
	if dm.fields[0].value() != "2026-06-18" {
		t.Fatalf("t key = %q, want 2026-06-18 (modal clock)", dm.fields[0].value())
	}
}

// Badges beyond the budget width are dropped so the line stays one row.
func TestDateBadgesFitsBudget(t *testing.T) {
	th, _ := themeByName("textual-dark")
	m := Model{styles: newStyles(th)}
	c := model.Card{Due: "2999-01-02", Scheduled: "2999-03-04", Completed: "2999-05-06"}
	// Budget for a single ~6-cell badge: only the first should render.
	got := m.dateBadges(c, 6)
	if !strings.Contains(got, "⚑") || strings.Contains(got, "◷") || strings.Contains(got, "✓") {
		t.Fatalf("narrow budget should keep only the due badge: %q", got)
	}
	if lipgloss.Width(got) > 6 {
		t.Fatalf("badge line %q exceeds budget 6", got)
	}
}

// save() diffs against originals: unchanged -> no diff; a change and a clear both
// produce pointers (clear -> pointer to "").
func TestDateModalSaveDiff(t *testing.T) {
	card := model.Card{Key: "TKB-1", Due: "2026-06-01", Scheduled: "2026-05-01"}
	dm := newDateModal(card, fixedToday)
	if _, changed := dm.save(); changed {
		t.Fatal("untouched modal reports a change")
	}
	dm.fields[0].seg = 2
	dm.fields[0].step(1) // due 06-01 -> 06-02
	dm.fields[1].clear() // clear scheduled
	opts, changed := dm.save()
	if !changed {
		t.Fatal("expected a change")
	}
	if opts.Due == nil || *opts.Due != "2026-06-02" {
		t.Fatalf("due diff = %v", opts.Due)
	}
	if opts.Scheduled == nil || *opts.Scheduled != "" {
		t.Fatalf("scheduled clear = %v, want pointer to empty", opts.Scheduled)
	}
	if opts.Completed != nil {
		t.Fatalf("completed should be unchanged, got %v", opts.Completed)
	}
}

// Dates persist as real tkt fields via `tkt edit` flags.
func TestEditOptsDateArgv(t *testing.T) {
	cr := &captureRunner{}
	tk := tkt.New("", "tkt").WithRunner(cr.run)
	due, clear := "2026-07-01", ""
	tk.Edit("TKB-1", tkt.EditOpts{Due: &due, Scheduled: &clear})
	got := cr.last("edit")
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--due 2026-07-01") || !strings.Contains(joined, "--scheduled ") {
		t.Fatalf("edit argv missing date flags: %v", got)
	}
}

func TestDueOverdue(t *testing.T) {
	now := fixedToday
	cases := []struct {
		due, completed string
		want           bool
	}{
		{"2026-06-17", "", true},            // yesterday, unfinished
		{"2026-06-18", "", false},           // today is not overdue
		{"2026-06-19", "", false},           // tomorrow
		{"2026-01-01", "2026-06-10", false}, // past but completed
		{"", "", false},                     // no due date
		{"garbage", "", false},              // unparsable
	}
	for _, c := range cases {
		if got := dueOverdue(c.due, c.completed, now); got != c.want {
			t.Fatalf("dueOverdue(%q,%q) = %v, want %v", c.due, c.completed, got, c.want)
		}
	}
}

func TestShortDate(t *testing.T) {
	if shortDate("2026-07-01") != "07-01" {
		t.Fatalf("shortDate = %q", shortDate("2026-07-01"))
	}
	if shortDate("weird") != "weird" {
		t.Fatal("shortDate mangled a non-date")
	}
}

// Date-less cards produce no badge (render exactly as before); set dates produce
// badges, and an overdue due date carries the error colour.
func TestDateBadges(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	th, _ := themeByName("textual-dark")
	m := Model{styles: newStyles(th)}
	errcSeq := fgSeq(th.errc) // statusErr foreground

	if got := m.dateBadges(model.Card{Key: "X"}, 80); got != "" {
		t.Fatalf("no-date card badge = %q, want empty", got)
	}
	// Future due: badge present, not error-coloured.
	future := m.dateBadges(model.Card{Due: "2999-01-02", Scheduled: "2999-03-04"}, 80)
	if !strings.Contains(future, "⚑") || !strings.Contains(future, "01-02") || !strings.Contains(future, "◷") {
		t.Fatalf("future badges missing: %q", future)
	}
	if strings.Contains(future, errcSeq) {
		t.Fatalf("non-overdue due should not be error-coloured: %q", future)
	}
	// Overdue due: error-coloured.
	overdue := m.dateBadges(model.Card{Due: "2000-01-01"}, 80)
	if !strings.Contains(overdue, errcSeq) {
		t.Fatalf("overdue due not error-coloured: %q", overdue)
	}
	// Past but completed: not overdue.
	done := m.dateBadges(model.Card{Due: "2000-01-01", Completed: "2000-02-02"}, 80)
	if strings.Contains(done, errcSeq) {
		t.Fatalf("completed due should not be error-coloured: %q", done)
	}
}
