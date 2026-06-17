package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/model"
	"github.com/olddognewflex/tktban/internal/tkt"
)

// ---- messages ----

// boardMsg is the result of a refresh: built columns, or an error to surface.
// warn carries a non-fatal note (e.g. time-in-lane unavailable).
type boardMsg struct {
	roles   []model.RolePair
	columns []model.Column
	warn    string
	err     error
}

// ticketMsg is a fetched ticket for the viewer or editor (purpose distinguishes).
type ticketMsg struct {
	ticket  model.Ticket
	purpose string // "view" | "edit"
	err     error
}

// issueTypesMsg is the flattened list of configured issue types for the creator.
type issueTypesMsg struct {
	types []string
	err   error
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
func refreshCmd(tk *tkt.Tkt, filter filterState) tea.Cmd {
	return func() tea.Msg {
		roles, err := tk.Roles()
		if err != nil {
			return boardMsg{err: err}
		}
		tickets, err := tk.ListAll()
		if err != nil {
			return boardMsg{err: err}
		}
		tickets = model.FilterTickets(tickets, filter.assignee, filter.prefix)
		warn := attachLaneTime(tk, tickets)
		return boardMsg{roles: roles, columns: model.BuildBoard(roles, tickets), warn: warn}
	}
}

// attachLaneTime annotates each visible ticket with its read-only time-in-lane
// via one batch call. A card with no history just gets no badge; a genuine
// failure returns a warning string and leaves the cards unannotated.
func attachLaneTime(tk *tkt.Tkt, tickets []model.Ticket) string {
	var items [][2]string
	for _, t := range tickets {
		k, _ := t["key"].(string)
		r, _ := t["status_role"].(string)
		if k != "" && r != "" {
			items = append(items, [2]string{k, r})
		}
	}
	if len(items) == 0 {
		return ""
	}
	batch, err := tk.LaneTimeBatch(items)
	if err != nil {
		return "time-in-lane unavailable: " + err.Error()
	}
	for _, t := range tickets {
		k, _ := t["key"].(string)
		if wl := batch[k]; wl != nil {
			if h, ok := wl["human"].(string); ok && h != "" {
				t["lane_human"] = h
			}
		}
	}
	return ""
}

func viewCmd(tk *tkt.Tkt, key, purpose string) tea.Cmd {
	return func() tea.Msg {
		t, err := tk.View(key)
		return ticketMsg{ticket: t, purpose: purpose, err: err}
	}
}

func issueTypesCmd(tk *tkt.Tkt) tea.Cmd {
	return func() tea.Msg {
		types, err := tk.IssueTypes()
		if err != nil {
			return issueTypesMsg{err: err}
		}
		return issueTypesMsg{types: flattenTypes(types)}
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
