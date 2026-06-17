// Package ui is the Bubble Tea kanban board over tkt — the Go port of the
// Textual app. Read path: a tea.Cmd shells out to tkt (roles + list + lane-time)
// off the UI loop and rebuilds the board. Write path: each action opens a modal,
// then runs the matching tkt verb in a command and refreshes. Selection is
// preserved across (auto-)refreshes by (column role, card key).
package ui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/model"
	"github.com/olddognewflex/tktban/internal/settings"
	"github.com/olddognewflex/tktban/internal/tkt"
)

type filterState struct {
	assignee string
	prefix   string
}

// Model is the root Bubble Tea model.
type Model struct {
	tkt     *tkt.Tkt
	roles   []model.RolePair
	columns []model.Column
	filter  filterState

	focusCol int
	sel      map[string]int // role -> selected card index

	settings     map[string]any
	settingsPath string
	themeName    string
	styles       styles

	refreshSecs float64
	autoOn      bool

	width, height int

	modal modal

	status     string
	statusKind string // "", "error", "warn"
	statusSeq  int

	loaded bool
}

// New builds the root model. A non-positive interval starts auto-refresh off but
// still toggles on at a 10s default (mirrors the Python).
func New(tk *tkt.Tkt, refreshInterval float64, autoRefresh bool, settingsPath string) Model {
	if settingsPath == "" {
		settingsPath = settings.DefaultPath()
	}
	s := settings.Load(settingsPath)
	secs := refreshInterval
	if secs <= 0 {
		secs = 10.0
	}
	themeName := str(s["theme"])
	th, ok := themeByName(themeName)
	if !ok {
		th, _ = themeByName("textual-dark")
	}
	return Model{
		tkt:          tk,
		filter:       filterState{},
		sel:          map[string]int{},
		settings:     s,
		settingsPath: settingsPath,
		themeName:    th.name,
		styles:       newStyles(th),
		refreshSecs:  secs,
		autoOn:       autoRefresh && refreshInterval > 0,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		refreshCmd(m.tkt, m.filter),
		tickCmd(secondsToDuration(m.refreshSecs)),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		if m.modal != nil {
			var cmd tea.Cmd
			m.modal, cmd = m.modal.Update(msg)
			return m, cmd
		}
		return m.handleKey(msg)

	case boardMsg:
		return m.onBoard(msg)

	case writeMsg:
		if msg.err != nil {
			return m.fail(msg.err)
		}
		cmd := m.setStatus(msg.success, "")
		return m, tea.Batch(cmd, refreshCmd(m.tkt, m.filter))

	case ticketMsg:
		if msg.err != nil {
			return m.fail(msg.err)
		}
		if msg.purpose == "edit" {
			m.modal = newEditModal(msg.ticket)
		} else {
			m.modal = newViewerModal(msg.ticket)
		}
		return m, nil

	case issueTypesMsg:
		if msg.err != nil {
			return m.fail(msg.err)
		}
		m.modal = newCreateModal(msg.types)
		return m, nil

	case createMsg:
		return m.onCreate(msg)

	case moveResultMsg:
		m.modal = nil
		if msg.cancelled || msg.role == "" {
			return m, nil
		}
		card, ok := m.selectedCard()
		if !ok {
			return m, nil
		}
		return m, transitionCmd(m.tkt, card.Key, msg.role, fmt.Sprintf("Moved %s → %s", card.Key, msg.role))

	case commentResultMsg:
		m.modal = nil
		if msg.cancelled || msg.body == "" {
			return m, nil
		}
		card, ok := m.selectedCard()
		if !ok {
			return m, nil
		}
		return m, commentCmd(m.tkt, card.Key, msg.body, "Commented on "+card.Key)

	case filterResultMsg:
		m.modal = nil
		if msg.cancelled {
			return m, nil
		}
		m.filter = filterState{assignee: msg.assignee, prefix: msg.prefix}
		return m, refreshCmd(m.tkt, m.filter)

	case editResultMsg:
		m.modal = nil
		if msg.cancelled {
			return m, nil
		}
		if !msg.changed {
			return m, m.setStatus("No changes made", "")
		}
		return m, editCmd(m.tkt, msg.key, msg.opts, "Edited "+msg.key)

	case viewerClosedMsg, createCancelMsg:
		m.modal = nil
		return m, nil

	case createSubmitMsg:
		return m, createCmd(m.tkt, msg.payload)

	case tickMsg:
		var cmd tea.Cmd
		if m.autoOn && m.modal == nil {
			cmd = refreshCmd(m.tkt, m.filter)
		}
		return m, tea.Batch(cmd, tickCmd(secondsToDuration(m.refreshSecs)))

	case statusExpireMsg:
		if int(msg) == m.statusSeq {
			m.status, m.statusKind = "", ""
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "r":
		return m, refreshCmd(m.tkt, m.filter)
	case "a":
		m.autoOn = !m.autoOn
		label := "Auto-refresh off"
		if m.autoOn {
			label = fmt.Sprintf("Auto-refresh on (%ds)", int(m.refreshSecs))
		}
		return m, m.setStatus(label, "")
	case "t":
		return m.cycleTheme()
	case "f":
		m.modal = newFilterModal(m.filter)
		return m, nil
	case "v":
		return m.openForCard("view")
	case "e":
		return m.openForCard("edit")
	case "m":
		card, ok := m.selectedCard()
		if !ok {
			return m, m.setStatus("Select a card first", "warn")
		}
		m.modal = newMoveModal(card, m.roleKeys())
		return m, nil
	case "c":
		card, ok := m.selectedCard()
		if !ok {
			return m, m.setStatus("Select a card first", "warn")
		}
		m.modal = newCommentModal(card)
		return m, nil
	case "n":
		return m, issueTypesCmd(m.tkt)
	case "left", "h":
		m.moveColumn(-1)
		return m, nil
	case "right", "l":
		m.moveColumn(1)
		return m, nil
	case "up", "k":
		m.moveRow(-1)
		return m, nil
	case "down", "j":
		m.moveRow(1)
		return m, nil
	}
	return m, nil
}

func (m Model) openForCard(purpose string) (tea.Model, tea.Cmd) {
	card, ok := m.selectedCard()
	if !ok {
		return m, m.setStatus("Select a card first", "warn")
	}
	return m, viewCmd(m.tkt, card.Key, purpose)
}

func (m Model) cycleTheme() (tea.Model, tea.Cmd) {
	next := nextTheme(m.themeName)
	m.themeName = next.name
	m.styles = newStyles(next)
	m.settings["theme"] = next.name
	if err := settings.Save(m.settingsPath, m.settings); err != nil {
		return m, m.setStatus("Theme set but not saved: "+err.Error(), "warn")
	}
	return m, m.setStatus("Theme: "+next.name, "")
}

func (m Model) onBoard(msg boardMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.fail(msg.err)
	}
	curRole, curKey := m.currentLocation()
	m.roles = msg.roles
	m.columns = msg.columns
	m.loaded = true
	m.restoreSelection(curRole, curKey)
	if msg.warn != "" {
		return m, m.setStatus(msg.warn, "warn")
	}
	return m, nil
}

func (m Model) onCreate(msg createMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if cm, ok := m.modal.(createModal); ok {
			m.modal = cm.withError(msg.err.Error())
		}
		return m, nil
	}
	m.modal = nil
	label := "Created ticket"
	if msg.key != "" {
		label = "Created " + msg.key
	}
	if msg.labelErr != "" {
		label += ", but labels not applied: " + msg.labelErr
		cmd := m.setStatus(label, "warn")
		return m, tea.Batch(cmd, refreshCmd(m.tkt, m.filter))
	}
	cmd := m.setStatus(label, "")
	return m, tea.Batch(cmd, refreshCmd(m.tkt, m.filter))
}

func (m Model) fail(err error) (tea.Model, tea.Cmd) {
	return m, m.setStatus(err.Error(), "error")
}

// setStatus records a transient status line and returns a command that clears it
// after a delay (unless superseded by a newer status first).
func (m *Model) setStatus(text, kind string) tea.Cmd {
	m.status = text
	m.statusKind = kind
	m.statusSeq++
	seq := m.statusSeq
	secs := 6
	if kind == "error" {
		secs = 10
	}
	return tea.Tick(time.Duration(secs)*time.Second, func(time.Time) tea.Msg { return statusExpireMsg(seq) })
}

// ---- selection ----

func (m Model) roleKeys() []string {
	out := make([]string, len(m.roles))
	for i, r := range m.roles {
		out[i] = r.Role
	}
	return out
}

func (m Model) selectedCard() (model.Card, bool) {
	if m.focusCol < 0 || m.focusCol >= len(m.columns) {
		return model.Card{}, false
	}
	col := m.columns[m.focusCol]
	if len(col.Cards) == 0 {
		return model.Card{}, false
	}
	idx := clamp(m.sel[col.Role], 0, len(col.Cards)-1)
	return col.Cards[idx], true
}

func (m Model) currentLocation() (role, key string) {
	if m.focusCol < 0 || m.focusCol >= len(m.columns) {
		return "", ""
	}
	col := m.columns[m.focusCol]
	role = col.Role
	if len(col.Cards) > 0 {
		idx := clamp(m.sel[col.Role], 0, len(col.Cards)-1)
		key = col.Cards[idx].Key
	}
	return role, key
}

func (m *Model) restoreSelection(role, key string) {
	m.focusCol = 0
	for i, col := range m.columns {
		if col.Role == role {
			m.focusCol = i
			break
		}
	}
	if role != "" && key != "" {
		col := m.columns[m.focusCol]
		for i, c := range col.Cards {
			if c.Key == key {
				m.sel[col.Role] = i
				break
			}
		}
	}
}

func (m *Model) moveColumn(delta int) {
	if len(m.columns) == 0 {
		return
	}
	m.focusCol = clamp(m.focusCol+delta, 0, len(m.columns)-1)
}

func (m *Model) moveRow(delta int) {
	if m.focusCol < 0 || m.focusCol >= len(m.columns) {
		return
	}
	col := m.columns[m.focusCol]
	if len(col.Cards) == 0 {
		return
	}
	m.sel[col.Role] = clamp(m.sel[col.Role]+delta, 0, len(col.Cards)-1)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func secondsToDuration(secs float64) time.Duration {
	return time.Duration(secs * float64(time.Second))
}
