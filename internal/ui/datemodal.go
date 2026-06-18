package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/olddognewflex/tktban/internal/model"
	"github.com/olddognewflex/tktban/internal/tkt"
)

// dateField is a keyboard-driven YYYY-MM-DD picker: a date with a focused
// segment (year/month/day). set=false means "no date". Stepping an unset field
// adopts the value (starting from today) and sets it.
type dateField struct {
	y, m, d int
	set     bool
	seg     int // 0=year, 1=month, 2=day
}

func newDateField(value string, today time.Time) dateField {
	f := dateField{y: today.Year(), m: int(today.Month()), d: today.Day()}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		f.y, f.m, f.d, f.set = t.Year(), int(t.Month()), t.Day(), true
	}
	return f
}

func daysInMonth(y, m int) int {
	return time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func (f dateField) value() string {
	if !f.set {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", f.y, f.m, f.d)
}

func (f *dateField) clear()        { f.set = false }
func (f *dateField) moveSeg(d int) { f.seg = clamp(f.seg+d, 0, 2) }
func (f *dateField) setToday(t time.Time) {
	f.y, f.m, f.d, f.set = t.Year(), int(t.Month()), t.Day(), true
}

// step changes the focused segment, clamping each to a valid range and keeping
// the day valid for the resulting month/year.
func (f *dateField) step(delta int) {
	f.set = true
	switch f.seg {
	case 0:
		f.y = clamp(f.y+delta, 1970, 9999)
	case 1:
		f.m = clamp(f.m+delta, 1, 12)
	case 2:
		f.d = clamp(f.d+delta, 1, daysInMonth(f.y, f.m))
	}
	if max := daysInMonth(f.y, f.m); f.d > max {
		f.d = max
	}
}

// ---- modal ----

type dateModal struct {
	key    string
	labels [3]string
	orig   [3]string // original values, for the save diff
	fields [3]dateField
	focus  int       // 0..2 (which date row)
	now    time.Time // clock captured at open, used by the "t" (today) key
}

func newDateModal(card model.Card, now time.Time) dateModal {
	vals := [3]string{card.Due, card.Scheduled, card.Completed}
	dm := dateModal{
		key:    card.Key,
		labels: [3]string{"due", "scheduled", "completed"},
		orig:   vals,
		now:    now,
	}
	for i, v := range vals {
		dm.fields[i] = newDateField(v, now)
	}
	return dm
}

// save diffs each field against its original and returns only the changed dates
// (a cleared date sends a pointer to "").
func (m dateModal) save() (tkt.EditOpts, bool) {
	opts := tkt.EditOpts{}
	changed := false
	set := func(dst **string, orig, cur string) {
		if cur != orig {
			v := cur
			*dst = &v
			changed = true
		}
	}
	set(&opts.Due, m.orig[0], m.fields[0].value())
	set(&opts.Scheduled, m.orig[1], m.fields[1].value())
	set(&opts.Completed, m.orig[2], m.fields[2].value())
	return opts, changed
}

func (m dateModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	switch {
	case keyIn(msg, "esc", "escape"):
		return m, send(editResultMsg{key: m.key, cancelled: true})
	case keyIn(msg, "ctrl+s"):
		opts, changed := m.save()
		return m, send(editResultMsg{key: m.key, opts: opts, changed: changed})
	case keyIn(msg, "tab"):
		m.focus = (m.focus + 1) % 3
		return m, nil
	case keyIn(msg, "shift+tab"):
		m.focus = (m.focus - 1 + 3) % 3
		return m, nil
	case keyIn(msg, "left", "h"):
		m.fields[m.focus].moveSeg(-1)
	case keyIn(msg, "right", "l"):
		m.fields[m.focus].moveSeg(1)
	case keyIn(msg, "up", "k"):
		m.fields[m.focus].step(1)
	case keyIn(msg, "down", "j"):
		m.fields[m.focus].step(-1)
	case keyIn(msg, "c"):
		m.fields[m.focus].clear()
	case keyIn(msg, "t"):
		m.fields[m.focus].setToday(m.now)
	}
	return m, nil
}

func (m dateModal) View(st styles, width, height int) string {
	var b strings.Builder
	b.WriteString(st.dialogTitle.Render("Dates for "+m.key) + "\n")
	for i := range m.fields {
		focused := i == m.focus
		row := m.labels[i] + ": " + dateFieldView(st, m.fields[i], focused)
		if focused {
			row = st.cardMeta.Render("▸ ") + row
		} else {
			row = "  " + row
		}
		b.WriteString(row + "\n")
	}
	b.WriteString("\n" + st.fieldLabel.Render("tab field · ←/→ segment · ↑/↓ change · c clear · t today · ctrl+s save · esc cancel"))
	return dialogBox(st, width, height, b.String())
}

// dateFieldView renders a date, highlighting the focused segment when the row is
// focused.
func dateFieldView(st styles, f dateField, focused bool) string {
	if !f.set {
		return st.fieldLabel.Render("(none)")
	}
	segs := []string{fmt.Sprintf("%04d", f.y), fmt.Sprintf("%02d", f.m), fmt.Sprintf("%02d", f.d)}
	if focused {
		segs[f.seg] = st.cardMeta.Render(segs[f.seg])
	}
	return segs[0] + "-" + segs[1] + "-" + segs[2]
}

// today is a hook so the picker's "t" key and overdue logic use one clock.
func today() time.Time { return time.Now() }

// ---- card badges ----

// shortDate compresses "YYYY-MM-DD" to "MM-DD" for a compact card badge; any
// other string is returned unchanged.
func shortDate(d string) string {
	if len(d) == 10 && d[4] == '-' && d[7] == '-' {
		return d[5:]
	}
	return d
}

// dueOverdue reports whether an unfinished due date is in the past.
func dueOverdue(due, completed string, now time.Time) bool {
	if due == "" || completed != "" {
		return false
	}
	t, err := time.Parse("2006-01-02", due)
	if err != nil {
		return false
	}
	// Date-only comparison: both the parsed due date and today's calendar date are
	// taken at UTC midnight, so "today" is never overdue and there's no
	// timezone-boundary off-by-one.
	y, mo, d := now.Date()
	return t.Before(time.Date(y, mo, d, 0, 0, 0, 0, time.UTC))
}

// dateBadges renders a card's set dates as a compact line: due (⚑), scheduled
// (◷), completed (✓). An overdue due date is shown in the error colour. Badges
// that would exceed the budget width are dropped so the line never wraps (which
// would break the column height accounting). Empty when the card has no dates,
// so date-less cards render exactly as before.
func (m Model) dateBadges(c model.Card, budget int) string {
	now := today()
	type badge struct {
		text  string
		style lipgloss.Style
	}
	var badges []badge
	if c.Due != "" {
		style := m.styles.cardMeta
		if dueOverdue(c.Due, c.Completed, now) {
			style = m.styles.statusErr
		}
		badges = append(badges, badge{"⚑" + shortDate(c.Due), style})
	}
	if c.Scheduled != "" {
		badges = append(badges, badge{"◷" + shortDate(c.Scheduled), m.styles.cardMeta})
	}
	if c.Completed != "" {
		badges = append(badges, badge{"✓" + shortDate(c.Completed), m.styles.cardMeta})
	}

	var parts []string
	used := 0
	for i, b := range badges {
		sep := 0
		if i > 0 {
			sep = 1 // the joining space
		}
		w := lipgloss.Width(b.text)
		if used+sep+w > budget {
			break // dropping this one keeps the line within one row
		}
		parts = append(parts, b.style.Render(b.text))
		used += sep + w
	}
	return strings.Join(parts, " ")
}
