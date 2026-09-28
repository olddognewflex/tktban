package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/model"
	"github.com/olddognewflex/tktban/internal/tkt"
)

// ---- messages ----

// boardMsg is the result of a refresh: built columns, or an error to surface.
// warn carries a non-fatal note (e.g. time-in-lane unavailable). archive is the
// tickets auto-archive should sweep, across the whole board rather than just
// the filtered cards; empty when the sweep is off or has nothing to do.
type boardMsg struct {
	roles   []model.RolePair
	columns []model.Column
	warn    string
	archive []string
	err     error
}

// archiveMsg is the result of an auto-archive sweep: the keys moved, the ones
// that failed (to be re-read or to move), and the ones archived but left
// without their comment. Failures carry the error text, keyed by ticket.
// release is the keys the sweep did not settle — skipped because they had
// already left done, or not re-read at all — which a later sweep may take
// again; a moved ticket and a failed transition are settled.
type archiveMsg struct {
	archived      []string
	failed        map[string]string
	commentFailed map[string]string
	release       []string
}

// ticketMsg is a fetched ticket for the viewer or editor (purpose distinguishes).
// priorities feeds the editor's priority select box.
type ticketMsg struct {
	ticket     model.Ticket
	purpose    string // "view" | "edit"
	priorities []string
	err        error
}

// issueTypesMsg is the flattened list of configured issue types for the creator,
// plus the configured priorities for its priority select box.
type issueTypesMsg struct {
	types      []string
	priorities []string
	err        error
}

// writeMsg is the result of a transition/comment/edit write.
type writeMsg struct {
	success string
	err     error
}

// createMsg is the result of a create (+ optional labels).
type createMsg struct {
	key      string
	labelErr string
	err      error
}

// tickMsg fires on the auto-refresh interval.
type tickMsg time.Time

// statusExpireMsg clears a transient status line once its timeout elapses.
type statusExpireMsg int

// ---- commands ----

// refreshCmd shells out to tkt (roles + list, filtered, with time-in-lane) and
// builds the board, all off the UI loop.
//
// Lane time is read for the filtered cards, for their badges, and — when the
// sweep is on — for every done ticket as well, filtered or not, because the
// auto-archive sweep covers the whole board: a filter is a view, and a ticket
// that has sat in done for a week is due whether or not it is on screen. With
// the sweep off the read is the filtered cards alone, as it always was.
// archiveDays <= 0 turns the sweep off, and so does a board with no archived
// role or a lane-time read that failed — a sweep on missing data would move
// nothing anyway, and one on partial data is not worth reasoning about.
func refreshCmd(tk *tkt.Tkt, filter filterState, archiveDays float64) tea.Cmd {
	return func() tea.Msg {
		roles, err := tk.Roles()
		if err != nil {
			return boardMsg{err: err}
		}
		tickets, err := tk.ListAll()
		if err != nil {
			return boardMsg{err: err}
		}
		sweep := archiveDays > 0 && model.HasRole(roles, model.RoleArchived)
		visible := model.FilterTickets(tickets, filter.assignee, filter.prefix)
		lane, warn := attachLaneTime(tk, laneTickets(tickets, visible, sweep))
		attachRunState(tk, visible)
		var archive []string
		if sweep && warn == "" {
			archive = model.ArchiveCandidates(tickets, lane, archiveDays)
		}
		return boardMsg{roles: roles, columns: model.BuildBoard(roles, visible), warn: warn, archive: archive}
	}
}

// laneTickets is the tickets whose lane time a refresh reads: the visible ones,
// plus every done ticket when the sweep is on, in board order. With the sweep
// off it is visible itself.
func laneTickets(all, visible []model.Ticket, sweep bool) []model.Ticket {
	if !sweep {
		return visible
	}
	shown := make(map[string]bool, len(visible))
	for _, t := range visible {
		k, _ := t["key"].(string)
		shown[k] = true
	}
	var out []model.Ticket
	for _, t := range all {
		k, _ := t["key"].(string)
		r, _ := t["status_role"].(string)
		if shown[k] || r == model.RoleDone {
			out = append(out, t)
		}
	}
	return out
}

// archiveCallTimeout bounds each tkt call a sweep makes. The board latches
// m.archiving until the sweep reports back, so a wedged tkt must be killed
// rather than waited on, or no sweep would start again this session. It is
// generous because a transition can be a round trip to a remote backend.
const archiveCallTimeout = 15 * time.Second

// archiveCmd moves each key to the archived lane and says why on the ticket,
// one at a time, off the UI loop.
//
// Each ticket is re-read first and skipped unless it is still in done. The
// board is shared, and a second board open on it sweeps the same candidates:
// without the check, both would transition and both would comment. The check
// narrows that race to the width of one tkt call rather than a refresh
// interval. A view that fails counts as a failed archive, so it is reported
// rather than silently dropped; it and a ticket that had left done are
// released for a later sweep, since neither was touched.
//
// A failed transition skips the comment: there is nothing to explain. Every
// call runs under its own archiveCallTimeout.
func archiveCmd(tk *tkt.Tkt, keys []string, days float64) tea.Cmd {
	return func() tea.Msg {
		var out archiveMsg
		fail := func(key string, err error) {
			if out.failed == nil {
				out.failed = map[string]string{}
			}
			out.failed[key] = err.Error()
		}
		// A fresh bounded copy per call, never an assignment back to tk: see
		// dispatchPrepCmd for why the captured Tkt must stay unbounded.
		call := func(f func(*tkt.Tkt) error) error {
			ctx, cancel := context.WithTimeout(context.Background(), archiveCallTimeout)
			defer cancel()
			return f(tk.WithContext(ctx))
		}
		body := fmt.Sprintf("Auto-archived after %s days in Done.", strconv.FormatFloat(days, 'f', -1, 64))
		for _, key := range keys {
			var t model.Ticket
			err := call(func(b *tkt.Tkt) (err error) { t, err = b.View(key); return err })
			if err != nil {
				fail(key, err)
				out.release = append(out.release, key)
				continue
			}
			if role, _ := t["status_role"].(string); role != model.RoleDone {
				// Moved since the refresh, by a person or another board.
				out.release = append(out.release, key)
				continue
			}
			if err := call(func(b *tkt.Tkt) error { return b.Transition(key, model.RoleArchived) }); err != nil {
				fail(key, err)
				continue
			}
			out.archived = append(out.archived, key)
			if err := call(func(b *tkt.Tkt) error { return b.Comment(key, body) }); err != nil {
				if out.commentFailed == nil {
					out.commentFailed = map[string]string{}
				}
				out.commentFailed[key] = err.Error()
			}
		}
		return out
	}
}

// attachLaneTime annotates each ticket with its read-only time-in-lane via one
// batch call, and returns the batch for the auto-archive sweep. A card with no
// history just gets no badge; a genuine failure returns a warning string and
// leaves the cards unannotated.
func attachLaneTime(tk *tkt.Tkt, tickets []model.Ticket) (map[string]map[string]any, string) {
	var items [][2]string
	for _, t := range tickets {
		k, _ := t["key"].(string)
		r, _ := t["status_role"].(string)
		if k != "" && r != "" {
			items = append(items, [2]string{k, r})
		}
	}
	if len(items) == 0 {
		return nil, ""
	}
	batch, err := tk.LaneTimeBatch(items)
	if err != nil {
		return nil, "time-in-lane unavailable: " + err.Error()
	}
	for _, t := range tickets {
		k, _ := t["key"].(string)
		if wl := batch[k]; wl != nil {
			if h, ok := wl["human"].(string); ok && h != "" {
				t["lane_human"] = h
			}
		}
	}
	return batch, ""
}

// attachRunState stamps each ticket with its `tkt agents` run state and when
// the run last wrote it, for the badge merge (model.MergeAgent). Best-effort and silent: run state is only an
// overlay, and an older tkt without the verb would otherwise warn on every
// refresh.
func attachRunState(tk *tkt.Tkt, tickets []model.Ticket) {
	runs, err := tk.Agents()
	if err != nil || len(runs) == 0 {
		return
	}
	for _, t := range tickets {
		k, _ := t["key"].(string)
		if r, ok := runs[strings.ToUpper(k)]; ok {
			t["run_state"], t["run_updated"] = r.State, r.Updated
		}
	}
}

func viewCmd(tk *tkt.Tkt, key, purpose string) tea.Cmd {
	return func() tea.Msg {
		t, err := tk.View(key)
		// Priorities feed the editor's select box; best-effort (the editor still
		// opens with a blank/default selection if the lookup fails).
		var priorities []string
		if purpose == "edit" {
			priorities = tk.Priorities()
		}
		return ticketMsg{ticket: t, purpose: purpose, priorities: priorities, err: err}
	}
}

func issueTypesCmd(tk *tkt.Tkt) tea.Cmd {
	return func() tea.Msg {
		types, err := tk.IssueTypes()
		if err != nil {
			return issueTypesMsg{err: err}
		}
		return issueTypesMsg{types: flattenTypes(types), priorities: tk.Priorities()}
	}
}

// flattenTypes concatenates full_sdlc + deliverable issue-type lists in order.
func flattenTypes(types map[string]any) []string {
	var out []string
	for _, group := range []string{"full_sdlc", "deliverable"} {
		if list, ok := types[group].([]any); ok {
			for _, v := range list {
				if s, ok := v.(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func transitionCmd(tk *tkt.Tkt, key, role, success string) tea.Cmd {
	return func() tea.Msg { return writeMsg{success: success, err: tk.Transition(key, role)} }
}

func commentCmd(tk *tkt.Tkt, key, body, success string) tea.Cmd {
	return func() tea.Msg { return writeMsg{success: success, err: tk.Comment(key, body)} }
}

func editCmd(tk *tkt.Tkt, key string, opts tkt.EditOpts, success string) tea.Cmd {
	return func() tea.Msg {
		_, err := tk.Edit(key, opts)
		return writeMsg{success: success, err: err}
	}
}

// createPayload carries a validated new-ticket form to the create command.
type createPayload struct {
	issueType string
	summary   string
	priority  string
	assignee  string
	body      string
	labels    []string
}

func createCmd(tk *tkt.Tkt, p createPayload) tea.Cmd {
	return func() tea.Msg {
		ticket, err := tk.Create(p.issueType, p.summary, tkt.CreateOpts{
			Priority: p.priority, Assignee: p.assignee, Body: p.body,
		})
		if err != nil {
			return createMsg{err: err}
		}
		key, _ := ticket["key"].(string)
		labelErr := ""
		if len(p.labels) > 0 {
			if key == "" {
				labelErr = "no key returned by create"
			} else if _, e := tk.Edit(key, tkt.EditOpts{AddLabels: p.labels}); e != nil {
				labelErr = e.Error()
			}
		}
		return createMsg{key: key, labelErr: labelErr}
	}
}

// tickCmd schedules the next auto-refresh tick.
func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}
