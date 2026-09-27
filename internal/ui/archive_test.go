package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/settings"
	"github.com/olddognewflex/tktban/internal/tkt"
)

// TKB-27: tickets that have sat in Done for archive_after_days move to the
// archived lane after a refresh, with a comment saying why.

const day = 86400.0

// rolesWithArchive is a board that has somewhere to archive to.
const rolesWithArchive = `{"todo": "To Do", "done": "Done", "archived": "Archived"}`

// archiveModel is a board whose TKT-2 has been in done for eight days and
// still reads as done when the sweep re-checks it. settingsBody, when not
// empty, is written to the settings file before the board starts.
func archiveModel(t *testing.T, settingsBody string) (Model, *captureRunner) {
	t.Helper()
	cr := &captureRunner{
		roles:       rolesWithArchive,
		laneSeconds: map[string]float64{"TKT-1": 30 * day, "TKT-2": 8 * day},
		viewRole:    map[string]string{"TKT-2": "done"},
	}
	path := filepath.Join(t.TempDir(), "settings.toml")
	if settingsBody != "" {
		if err := os.WriteFile(path, []byte(settingsBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := New(tkt.New("", "tkt").WithRunner(cr.run), 10, true, path)
	m.width, m.height = 120, 30
	return m, cr
}

// refresh runs a refresh synchronously and feeds it to the model, returning
// the command the board answered with (which carries any sweep).
func refresh(m Model) (Model, tea.Cmd) {
	nm, cmd := m.Update(m.refreshCmd()())
	return nm.(Model), cmd
}

// await runs cmd, expanding batches, and returns the first message of type T.
// Each command runs in its own goroutine because a batch mixes quick commands
// with status-expiry ticks that sleep for seconds; those are abandoned. The
// tkt-calling command is received over a channel, so the test's later reads of
// the fake runner still happen after its writes.
func await[T tea.Msg](t *testing.T, cmd tea.Cmd) (T, bool) {
	t.Helper()
	var zero T
	if cmd == nil {
		return zero, false
	}
	msgs := make(chan tea.Msg, 16)
	var start func(tea.Cmd)
	start = func(c tea.Cmd) {
		go func() {
			msg := c()
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, sub := range batch {
					if sub != nil {
						start(sub)
					}
				}
				return
			}
			msgs <- msg
		}()
	}
	start(cmd)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case msg := <-msgs:
			if got, ok := msg.(T); ok {
				return got, true
			}
		case <-deadline:
			return zero, false
		}
	}
}

// callsTo returns every recorded call to verb.
func (c *captureRunner) callsTo(verb string) [][]string {
	var out [][]string
	for _, call := range c.calls {
		if len(call) > 0 && call[0] == verb {
			out = append(out, call)
		}
	}
	return out
}

// sweep refreshes and, if a sweep started, runs it and feeds its result back.
func sweep(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	m, cmd := refresh(m)
	if !m.archiving {
		return m, cmd
	}
	msg, ok := await[archiveMsg](t, cmd)
	if !ok {
		t.Fatal("a sweep was started but no archiveMsg arrived")
	}
	nm, cmd := m.Update(msg)
	return nm.(Model), cmd
}

func TestAutoArchiveMovesOldDoneTicket(t *testing.T) {
	m, cr := archiveModel(t, "")
	m, cmd := refresh(m)
	if !m.archiving {
		t.Fatal("no sweep started for a ticket eight days in done")
	}
	msg, ok := await[archiveMsg](t, cmd)
	if !ok {
		t.Fatal("no archiveMsg")
	}
	// Re-read, then move, then say why — in that order, for TKT-2 only.
	var writes [][]string
	for _, c := range cr.calls {
		if c[0] == "view" || c[0] == "transition" || c[0] == "comment" {
			writes = append(writes, c)
		}
	}
	if len(writes) != 3 ||
		!eq(writes[0], "view", "TKT-2", "--json") ||
		!eq(writes[1], "transition", "TKT-2", "archived") ||
		!eq(writes[2], "comment", "TKT-2", "Auto-archived after 7 days in Done.") {
		t.Fatalf("sweep calls = %v", writes)
	}
	nm, cmd := m.Update(msg)
	m = nm.(Model)
	if m.archiving {
		t.Fatal("still archiving after the result arrived")
	}
	if m.status != "archived 1 ticket" || m.statusKind != "" {
		t.Fatalf("status = %q (%q)", m.status, m.statusKind)
	}
	// And the board refreshes once more so the card leaves Done.
	if _, ok := await[boardMsg](t, cmd); !ok {
		t.Fatal("no refresh after archiving")
	}
}

func TestAutoArchiveLeavesRecentTicket(t *testing.T) {
	m, cr := archiveModel(t, "")
	cr.laneSeconds["TKT-2"] = 7*day - 1
	m, _ = refresh(m)
	if m.archiving || len(cr.callsTo("transition")) != 0 {
		t.Fatalf("archived a ticket a second short of the threshold: %v", cr.calls)
	}
}

func TestAutoArchiveOffWhenZero(t *testing.T) {
	m, cr := archiveModel(t, "archive_after_days = 0\n")
	if m.archiveDays != 0 {
		t.Fatalf("archiveDays = %v, want 0", m.archiveDays)
	}
	m, _ = refresh(m)
	if m.archiving || len(cr.callsTo("transition")) != 0 {
		t.Fatalf("swept with auto-archive off: %v", cr.calls)
	}
}

func TestAutoArchiveThresholdFromSettings(t *testing.T) {
	m, cr := archiveModel(t, "archive_after_days = 10\n")
	m, _ = refresh(m) // eight days is under ten
	if m.archiving {
		t.Fatal("swept below a configured ten-day threshold")
	}
	m, cr = archiveModel(t, "archive_after_days = 3\n")
	m, cmd := refresh(m)
	if _, ok := await[archiveMsg](t, cmd); !ok {
		t.Fatal("no sweep above a configured three-day threshold")
	}
	if got := cr.last("comment"); !eq(got, "comment", "TKT-2", "Auto-archived after 3 days in Done.") {
		t.Fatalf("comment = %v", got)
	}
}

func TestArchiveThresholdParsing(t *testing.T) {
	cases := []struct {
		in   any
		want float64
	}{
		{7, 7}, {int64(3), 3}, {1.5, 1.5}, {int64(0), 0}, {int64(-2), -2},
		{"7", defaultArchiveDays}, {true, defaultArchiveDays}, {nil, defaultArchiveDays},
	}
	for _, c := range cases {
		if got := archiveThreshold(c.in); got != c.want {
			t.Errorf("archiveThreshold(%#v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestAutoArchiveSkippedWithoutArchivedRole(t *testing.T) {
	m, cr := archiveModel(t, "")
	cr.roles = "" // the default: todo and done only
	m, _ = refresh(m)
	if m.archiving || len(cr.callsTo("transition")) != 0 {
		t.Fatalf("swept a board with no archived role: %v", cr.calls)
	}
	if m.status != "" {
		t.Fatalf("a board without the role should skip silently, status = %q", m.status)
	}
}

// The sweep covers every ticket, not just the ones the filter shows.
func TestAutoArchiveIgnoresFilter(t *testing.T) {
	m, cr := archiveModel(t, "")
	m.filter = filterState{assignee: "alice"} // TKT-2 is unassigned
	m, _ = sweep(t, m)
	if strings.Contains(m.View(), "TKT-2") {
		t.Fatal("filter did not hide TKT-2")
	}
	if got := cr.last("transition"); !eq(got, "transition", "TKT-2", "archived") {
		t.Fatalf("filtered-out ticket not archived: %v", cr.calls)
	}
}

func TestAutoArchiveTransitionFailureWarns(t *testing.T) {
	m, cr := archiveModel(t, "")
	cr.failVerbs = map[string]bool{"transition": true}
	m, _ = sweep(t, m)
	if len(cr.callsTo("comment")) != 0 {
		t.Fatalf("commented on a ticket that did not move: %v", cr.calls)
	}
	if m.archiving {
		t.Fatal("still archiving after a failed sweep")
	}
	if m.statusKind != "warn" || !strings.Contains(m.status, "archived 0 tickets; 1 failed: TKT-2: ") {
		t.Fatalf("status = %q (%q)", m.status, m.statusKind)
	}
	if !strings.Contains(m.View(), "TKT-2") {
		t.Fatal("board lost its cards after a failed sweep")
	}
}

func TestAutoArchiveCommentFailureWarns(t *testing.T) {
	m, cr := archiveModel(t, "")
	cr.failVerbs = map[string]bool{"comment": true}
	m, _ = sweep(t, m)
	if got := cr.last("transition"); !eq(got, "transition", "TKT-2", "archived") {
		t.Fatalf("transition = %v", got)
	}
	if m.statusKind != "warn" || !strings.HasPrefix(m.status, "archived 1 ticket; comment failed on TKT-2: ") {
		t.Fatalf("status = %q (%q)", m.status, m.statusKind)
	}
}

// The fake board never changes, so TKT-2 is a candidate on every refresh; a
// ticket already swept must not be swept again, whether it moved or failed.
func TestAutoArchiveDoesNotLoop(t *testing.T) {
	for _, fail := range []string{"", "transition"} {
		m, cr := archiveModel(t, "")
		cr.failVerbs = map[string]bool{fail: true}
		m, cmd := sweep(t, m)
		m = followUp(t, m, cmd) // the sweep's own refresh
		m, _ = sweep(t, m)
		m, _ = refresh(m)
		if m.archiving {
			t.Fatalf("fail=%q: a second sweep started for the same ticket", fail)
		}
		if n := len(cr.callsTo("transition")); n != 1 {
			t.Fatalf("fail=%q: %d transitions, want 1", fail, n)
		}
	}
}

func TestAutoArchiveNotStartedWhileInFlight(t *testing.T) {
	m, cr := archiveModel(t, "")
	m.archiving = true // a sweep is already running
	m, _ = refresh(m)
	if m.swept["TKT-2"] {
		t.Fatal("queued a ticket while a sweep was in flight")
	}
	if len(cr.callsTo("transition")) != 0 {
		t.Fatalf("transitioned while a sweep was in flight: %v", cr.calls)
	}
}

// Another board open on the same tkt board may have archived it first.
func TestAutoArchiveSkipsTicketNoLongerDone(t *testing.T) {
	m, cr := archiveModel(t, "")
	cr.viewRole["TKT-2"] = "archived"
	m, cmd := sweep(t, m)
	if len(cr.callsTo("transition")) != 0 || len(cr.callsTo("comment")) != 0 {
		t.Fatalf("archived a ticket that had already left done: %v", cr.calls)
	}
	if m.archiving || m.status != "" {
		t.Fatalf("status = %q, archiving = %v", m.status, m.archiving)
	}
	if _, ok := await[boardMsg](t, cmd); !ok {
		t.Fatal("no follow-up refresh after a sweep")
	}
}

// followUp feeds the sweep's own follow-up refresh back into the board.
func followUp(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	msg, ok := await[boardMsg](t, cmd)
	if !ok {
		t.Fatal("no follow-up refresh after a sweep")
	}
	return step(m, msg)
}

// A skipped ticket was not touched, so it is not settled: the follow-up
// refresh leaves it alone (or the sweep would never end), but the refresh
// after that may take it again.
func TestAutoArchiveSkippedTicketEligibleAgain(t *testing.T) {
	m, cr := archiveModel(t, "")
	cr.viewRole["TKT-2"] = "archived" // lane-time still says done
	m, cmd := sweep(t, m)
	m = followUp(t, m, cmd)
	if m.archiving {
		t.Fatal("the follow-up refresh re-swept the skipped ticket")
	}
	m, _ = refresh(m)
	if !m.archiving {
		t.Fatal("the skipped ticket was not eligible on the next refresh")
	}
}

// Same for a ticket whose re-read failed, which is also reported.
func TestAutoArchiveViewFailureWarnsAndReleases(t *testing.T) {
	m, cr := archiveModel(t, "")
	cr.failOn = map[string]bool{"view TKT-2": true}
	m, cmd := sweep(t, m)
	if len(cr.callsTo("transition")) != 0 {
		t.Fatalf("transitioned a ticket it could not re-read: %v", cr.calls)
	}
	if m.statusKind != "warn" || !strings.Contains(m.status, "1 failed: TKT-2: ") {
		t.Fatalf("status = %q (%q)", m.status, m.statusKind)
	}
	m = followUp(t, m, cmd)
	if m.archiving {
		t.Fatal("the follow-up refresh re-swept at once")
	}
	m, _ = refresh(m)
	if !m.archiving {
		t.Fatal("the unread ticket was not eligible on the next refresh")
	}
}

// A failed transition is settled: not retried by any later refresh.
func TestAutoArchiveTransitionFailureNotRetried(t *testing.T) {
	m, cr := archiveModel(t, "")
	cr.failVerbs = map[string]bool{"transition": true}
	m, cmd := sweep(t, m)
	m = followUp(t, m, cmd)
	m, _ = refresh(m)
	m, _ = refresh(m)
	if m.archiving || len(cr.callsTo("transition")) != 1 {
		t.Fatalf("failed transition retried: archiving = %v, calls = %v", m.archiving, cr.callsTo("transition"))
	}
}

// Every tkt call a sweep makes carries a deadline, so a wedged tkt cannot
// leave the board archiving forever.
func TestAutoArchiveCallsAreBounded(t *testing.T) {
	m, cr := archiveModel(t, "")
	cr.observeCtx = true
	sweep(t, m)
	seen := 0
	for _, c := range cr.ctxCalls {
		switch c.args[0] {
		case "view", "transition", "comment":
			seen++
			if !c.deadline || !c.live {
				t.Errorf("%v: deadline = %v, live = %v", c.args, c.deadline, c.live)
			}
		}
	}
	if seen != 3 {
		t.Fatalf("observed %d sweep calls, want 3: %v", seen, cr.calls)
	}
}

// laneKeys is the --keys argument of the last lane-time call.
func laneKeys(cr *captureRunner) string {
	args := cr.last("lane-time")
	for i, a := range args {
		if a == "--keys" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// Lane time is read for the whole board only as far as the sweep needs: the
// filtered cards, plus done tickets when the sweep is on.
func TestLaneTimeKeysFollowTheSweep(t *testing.T) {
	m, cr := archiveModel(t, "")
	m.filter = filterState{assignee: "alice"} // shows TKT-1 only
	m, _ = refresh(m)
	if got := laneKeys(cr); got != "TKT-1:todo,TKT-2:done" {
		t.Fatalf("sweep on: --keys %q", got)
	}

	m, cr = archiveModel(t, "archive_after_days = 0\n")
	m.filter = filterState{assignee: "alice"}
	m, _ = refresh(m)
	if got := laneKeys(cr); got != "TKT-1:todo" {
		t.Fatalf("sweep off: --keys %q", got)
	}

	m, cr = archiveModel(t, "")
	cr.roles = "" // no archived role: the sweep is off
	m.filter = filterState{assignee: "alice"}
	refresh(m)
	if got := laneKeys(cr); got != "TKT-1:todo" {
		t.Fatalf("no archived role: --keys %q", got)
	}
}

func TestAutoArchiveSkippedWhenLaneTimeFails(t *testing.T) {
	m, cr := archiveModel(t, "")
	cr.failVerbs = map[string]bool{"lane-time": true}
	m, _ = refresh(m)
	if m.archiving || len(cr.callsTo("view")) != 0 {
		t.Fatalf("swept without lane time: %v", cr.calls)
	}
	if m.statusKind != "warn" || !strings.Contains(m.status, "time-in-lane unavailable") {
		t.Fatalf("status = %q (%q)", m.status, m.statusKind)
	}
	if v := m.View(); !strings.Contains(v, "TKT-1") || !strings.Contains(v, "TKT-2") {
		t.Fatalf("board did not render:\n%s", v)
	}
}

func TestAutoArchiveMixedOutcomes(t *testing.T) {
	m, cr := archiveModel(t, "")
	cr.list = `[
		{"key":"TKT-1","summary":"a","status_role":"todo","blocked_by":[]},
		{"key":"TKT-3","summary":"c","status_role":"done","blocked_by":[]},
		{"key":"TKT-4","summary":"d","status_role":"done","blocked_by":[]},
		{"key":"TKT-5","summary":"e","status_role":"done","blocked_by":[]},
		{"key":"TKT-6","summary":"f","status_role":"done","blocked_by":[]}
	]`
	for _, k := range []string{"TKT-3", "TKT-4", "TKT-5", "TKT-6"} {
		cr.laneSeconds[k] = 9 * day
		cr.viewRole[k] = "done"
	}
	cr.failOn = map[string]bool{"transition TKT-6": true, "transition TKT-3": true}
	m, _ = sweep(t, m)
	want := "archived 2 tickets; 2 failed: " +
		"TKT-3: tkt transition TKT-3 archived failed (provider error): transition refused; " +
		"TKT-6: tkt transition TKT-6 archived failed (provider error): transition refused"
	if m.status != want || m.statusKind != "warn" {
		t.Fatalf("status = %q (%q)\nwant     %q", m.status, m.statusKind, want)
	}
	if n := len(cr.callsTo("comment")); n != 2 {
		t.Fatalf("%d comments, want 2 (the moved tickets only)", n)
	}
}

func TestArchivedColumnHiddenByDefault(t *testing.T) {
	m, _ := archiveModel(t, "")
	m.archiveDays = 0 // just the columns
	m, _ = refresh(m)
	if roleSet(m)["archived"] {
		t.Fatalf("archived column shown by default: %v", roleSet(m))
	}
	m = step(m, key("X"))
	if !roleSet(m)["archived"] {
		t.Fatalf("X did not show the archived column: %v", roleSet(m))
	}
}

// A board with no archived column has nothing hidden, so X says so rather
// than clearing the default that would hide the lane once it is added.
func TestArchivedDefaultHidesNothingWithoutTheRole(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	m = step(m, key("X"))
	if m.status != "No hidden columns" || !m.hidden["archived"] {
		t.Fatalf("status = %q, hidden = %v", m.status, m.hidden)
	}
}

// On a board without the archived role, X shows what it has and keeps the
// archived default for when the role is added.
func TestShowAllKeepsRolesNotOnTheBoard(t *testing.T) {
	m, _ := testModel(t) // todo and done, no archived
	m = loadBoard(m)
	m = step(m, key("x")) // hide todo
	if !m.hidden["todo"] || !m.hidden["archived"] {
		t.Fatalf("hidden = %v", m.hidden)
	}
	m = step(m, key("X"))
	if m.hidden["todo"] || !m.hidden["archived"] || len(m.columns) != 2 {
		t.Fatalf("hidden = %v, columns = %v", m.hidden, roleSet(m))
	}
	if got := str(settings.Load(m.settingsPath)["hidden_roles"]); got != "archived" {
		t.Fatalf("persisted hidden_roles = %q, want archived", got)
	}
}

// Before the first load there is no board to compare against: X clears all.
func TestShowAllBeforeLoadClearsEverything(t *testing.T) {
	m, _ := testModel(t)
	m = step(m, key("X"))
	if len(m.hidden) != 0 || m.status != "Showing all columns" {
		t.Fatalf("hidden = %v, status = %q", m.hidden, m.status)
	}
}

func TestArchivedHiddenAlongsideConfigDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.toml")
	tk := tkt.New("", "tkt").WithRunner(runnerWithHiddenDefault(`["done"]`))
	m := New(tk, 10, true, path)
	if !m.hidden["done"] || !m.hidden["archived"] {
		t.Fatalf("hidden = %v, want done and archived", m.hidden)
	}
}
