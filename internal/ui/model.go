// Package ui is the Bubble Tea kanban board over tkt — the Go port of the
// Textual app. Read path: a tea.Cmd shells out to tkt (roles + list + lane-time)
// off the UI loop and rebuilds the board. Write path: each action opens a modal,
// then runs the matching tkt verb in a command and refreshes. Selection is
// preserved across (auto-)refreshes by (column role, card key).
package ui

import (
	"fmt"
	"os"
	"sort"
	"strings"
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
	columns []model.Column // visible columns (allColumns minus hidden), rendered
	filter  filterState

	// allColumns is the full board from the last refresh; columns is allColumns
	// with hidden roles removed. hidden is the set of role keys the user has
	// hidden, persisted to settings.toml and seeded from the [ui.board]
	// hidden_roles config default on first run.
	allColumns []model.Column
	hidden     map[string]bool

	focusCol int
	sel      map[string]int // role -> selected card index

	// Vim motion state: pendingCount accumulates a numeric prefix (e.g. 3j),
	// pendingG records a half-typed `gg` sequence. Both reset on the next
	// non-digit / non-g key.
	pendingCount int
	pendingG     bool

	settings     map[string]any
	settingsPath string
	// pluginSettings says settingsPath is herdr's plugin settings file — the
	// one the notification hook reads — rather than the standalone one. Set by
	// WithPluginSettings; false for every board that is not herdr's own, which
	// includes a board in a plain herdr pane. Only it may promise that flipping
	// notify silences anything.
	pluginSettings bool
	themeName      string
	styles         styles

	refreshSecs float64
	autoOn      bool

	// live is herdr agent status for card badges; zero (off) outside herdr.
	live liveState

	// selectKey is a ticket to select once the board first loads, cleared as
	// soon as it has been applied. selectExplicit records that the person
	// typed it (--select) rather than it being read off a git branch
	// (--select-from-cwd), which decides how loudly a miss is reported.
	// selectFunc derives the key off the UI loop, so reading a branch on a
	// slow filesystem cannot hold up the first paint.
	// popup means the board is herdr's popup pane, which a successful jump
	// closes by exiting.
	selectKey      string
	selectExplicit bool
	selectFunc     func() string
	popup          bool

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
	// Seed the hidden set: from persisted settings once the file exists,
	// otherwise from the [ui.board] hidden_roles config default. "First run" is
	// "no settings.toml yet", so the config default is re-applied on every launch
	// until the first Save — which is correct for a default (a later change to
	// the config default is then honoured). The seed is written into the settings
	// map, so the first Save (a theme cycle or a hide/show toggle) persists it
	// rather than clobbering it with an empty value; from then on the persisted
	// value is authoritative and the config default is ignored.
	if !fileExists(settingsPath) {
		if def := tk.BoardHiddenRoles(); len(def) > 0 {
			s["hidden_roles"] = strings.Join(def, ",")
		}
	}
	hidden := map[string]bool{}
	for _, r := range parseLabels(str(s["hidden_roles"])) {
		hidden[r] = true
	}
	return Model{
		tkt:          tk,
		filter:       filterState{},
		sel:          map[string]int{},
		hidden:       hidden,
		settings:     s,
		settingsPath: settingsPath,
		themeName:    th.name,
		styles:       newStyles(th),
		refreshSecs:  secs,
		autoOn:       autoRefresh && refreshInterval > 0,
	}
}

// fileExists reports whether path is an existing file (used to detect a board's
// first run for config-default seeding).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		refreshCmd(m.tkt, m.filter),
		tickCmd(secondsToDuration(m.refreshSecs)),
		m.liveInit(),
		m.selectInit(),
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
			m.modal = newEditModal(msg.ticket, msg.priorities, m.styles.t.surface, m.width, m.height)
		} else {
			m.modal = newViewerModal(msg.ticket)
		}
		return m, nil

	case issueTypesMsg:
		if msg.err != nil {
			return m.fail(msg.err)
		}
		m.modal = newCreateModal(msg.types, msg.priorities, m.styles.t.surface, m.width, m.height)
		return m, nil

	case editorPrepMsg:
		if msg.err != nil {
			return m.fail(msg.err)
		}
		return m, launchEditorCmd(msg)

	case editorClosedMsg:
		return m.onEditorClosed(msg)

	case applyDoneMsg:
		return m.onApplyDone(msg)

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

	case liveProbeMsg:
		return m.onLiveProbe(msg)

	case liveMsg:
		return m.onLive(msg)

	case liveTickMsg:
		return m.onLiveTick()

	case jumpMsg:
		return m.onJump(msg)

	case selectKeyMsg:
		return m.onSelectKey(msg)

	case statusExpireMsg:
		if int(msg) == m.statusSeq {
			m.status, m.statusKind = "", ""
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := msg.String()

	// Numeric count prefix (vim): 1-9 starts a count, 0 extends one already in
	// progress. The count is consumed by the next motion.
	if (len(s) == 1 && s[0] >= '1' && s[0] <= '9') || (m.pendingCount > 0 && s == "0") {
		if m.pendingCount < 100000 { // cap to avoid overflow on mashed digits
			m.pendingCount = m.pendingCount*10 + int(s[0]-'0')
		}
		return m, nil
	}

	// `gg` jumps to the first card: the first g arms, the second fires.
	if s == "g" {
		if m.pendingG {
			m.pendingG, m.pendingCount = false, 0
			m.moveToFirst()
			return m, nil
		}
		m.pendingG = true
		return m, nil
	}

	// `ga` is the second spelling of the o jump, for hands that reach for a g
	// prefix. It has to be handled before the reset below, or the pending g
	// would be dropped and `a` would toggle auto-refresh instead.
	if s == "a" && m.pendingG {
		m.pendingG, m.pendingCount = false, 0
		return m.jumpToAgentPane()
	}

	// Any other key ends a pending count / half-typed gg. Motions below consume
	// the count (default 1); non-motion keys just discard it.
	count := max(m.pendingCount, 1)
	m.pendingCount, m.pendingG = 0, false

	switch s {
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
	case "b":
		return m.toggleNotify()
	case "f":
		m.modal = newFilterModal(m.filter, m.styles.t.surface)
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
		m.modal = newCommentModal(card, m.styles.t.surface)
		return m, nil
	case "d":
		card, ok := m.selectedCard()
		if !ok {
			return m, m.setStatus("Select a card first", "warn")
		}
		m.modal = newDateModal(card, today())
		return m, nil
	case "n":
		return m, issueTypesCmd(m.tkt)
	case "N":
		return m, prepEditorCreateCmd(m.tkt)
	case "E":
		card, ok := m.selectedCard()
		if !ok {
			return m, m.setStatus("Select a card first", "warn")
		}
		return m, prepEditorEditCmd(m.tkt, card.Key)
	case "o":
		return m.jumpToAgentPane()
	case "x":
		return m.hideFocusedColumn()
	case "X":
		return m.showAllColumns()
	case "/":
		m.modal = newFilterModal(m.filter, m.styles.t.surface)
		return m, nil
	case "left", "h":
		m.moveColumn(-count)
		return m, nil
	case "right", "l":
		m.moveColumn(count)
		return m, nil
	case "up", "k":
		m.moveRow(-count)
		return m, nil
	case "down", "j":
		m.moveRow(count)
		return m, nil
	case "G":
		m.moveToLast()
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
	if err := m.saveSettings(); err != nil {
		return m, m.setStatus("Theme set but not saved: "+err.Error(), "warn")
	}
	return m, m.setStatus("Theme: "+next.name, "")
}

// toggleNotify flips the notify setting the herdr notification hook reads and
// persists it. The hook is the only reader — the board itself never toasts —
// so outside herdr the status line says where the flip did and did not land:
// the standalone settings file is not the one the hook reads.
func (m Model) toggleNotify() (tea.Model, tea.Cmd) {
	// Flip what is on disk, not what this board loaded at startup: the hook
	// reads the file, and so does whoever edited it by hand since, or the
	// second board they left open. Flipping a stale value would write back the
	// value already there and report a change that never happened.
	cur, ok := settings.Load(m.settingsPath)["notify"].(bool)
	if !ok {
		cur = true // missing or not a TOML boolean reads as on, as the hook reads it
	}
	on := !cur
	label := "herdr notifications off"
	if on {
		label = "herdr notifications on"
	}
	if !m.pluginSettings {
		label += " (herdr's own board keeps a separate setting)"
	}
	prev, had := m.settings["notify"]
	m.settings["notify"] = on
	if err := m.saveSettings("notify"); err != nil {
		// Put the in-memory value back, or the subtitle would show a mute the
		// hook knows nothing about.
		if had {
			m.settings["notify"] = prev
		} else {
			delete(m.settings, "notify")
		}
		return m, m.setStatus(label+" (not saved: "+err.Error()+")", "warn")
	}
	return m, m.setStatus(label, "")
}

func (m Model) onBoard(msg boardMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.fail(msg.err)
	}
	curRole, curKey := m.currentLocation()
	m.roles = msg.roles
	m.allColumns = msg.columns
	m.applyHidden()
	m.loaded = true
	m.restoreSelection(curRole, curKey)
	// A pending select key overrides the restored selection, but only on the
	// load that consumes it. A board warning is the rarer, more serious thing,
	// so it is what stays on screen when both happen; the selection itself
	// still moved, and both status timers are armed.
	sel := m.applySelectKey()
	if msg.warn != "" {
		return m, tea.Batch(sel, m.setStatus(msg.warn, "warn"))
	}
	return m, sel
}

// selectKeyMsg carries a ticket key that was derived off the UI loop.
type selectKeyMsg struct{ key string }

// selectInit derives a --select-from-cwd key in a command, so a git branch on
// a slow or hung filesystem delays the selection rather than the whole board.
func (m Model) selectInit() tea.Cmd {
	derive := m.selectFunc
	if derive == nil {
		return nil
	}
	return func() tea.Msg { return selectKeyMsg{key: derive()} }
}

// onSelectKey takes a derived key. It arrives whenever it arrives: before the
// first board load it waits there for it, and afterwards it applies at once.
// An explicit --select is never overridden.
func (m Model) onSelectKey(msg selectKeyMsg) (tea.Model, tea.Cmd) {
	if msg.key == "" || m.selectExplicit {
		return m, nil
	}
	m.selectKey = normalizeKey(msg.key)
	if !m.loaded {
		return m, nil // the next board load consumes it
	}
	return m, m.applySelectKey()
}

// applySelectKey consumes a pending --select key: it focuses that ticket's
// column and card, or says why it cannot. The key is cleared either way, so a
// later auto-refresh never yanks the selection back from wherever the user has
// moved it since. Returns nil when there was nothing to select.
func (m *Model) applySelectKey() tea.Cmd {
	key := m.selectKey
	if key == "" {
		return nil
	}
	m.selectKey = ""
	for i, col := range m.columns {
		for j, c := range col.Cards {
			if strings.EqualFold(c.Key, key) {
				m.focusCol = i
				m.sel[col.Role] = j
				return m.setStatus("Selected "+key, "")
			}
		}
	}
	// Out of sight rather than absent: name the lane and the key that brings
	// it back. A key the person typed and did not get is a warning; one read
	// off the branch is a guess the board makes on every single open, and a
	// hidden column is a standing choice, so that one is said plainly.
	kind := "warn"
	if !m.selectExplicit {
		kind = ""
	}
	for _, col := range m.allColumns {
		for _, c := range col.Cards {
			if strings.EqualFold(c.Key, key) {
				return m.setStatus(key+" is in "+col.Lane+", a hidden column (X shows all columns)", kind)
			}
		}
	}
	if !m.selectExplicit {
		return nil // the branch names no ticket on this board; just open it
	}
	return m.setStatus(key+" is not on the board", "warn")
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

// applyHidden recomputes the visible columns from allColumns minus the hidden
// set. As a safety net it never hides every column (a board with nothing
// visible would be useless), so an over-broad or invalid hidden set falls back
// to showing all columns.
func (m *Model) applyHidden() {
	if len(m.hidden) == 0 {
		m.columns = m.allColumns
		return
	}
	vis := make([]model.Column, 0, len(m.allColumns))
	for _, c := range m.allColumns {
		if !m.hidden[c.Role] {
			vis = append(vis, c)
		}
	}
	if len(vis) == 0 {
		vis = m.allColumns
	}
	m.columns = vis
}

// hideFocusedColumn hides the focused column, keeps focus on a still-visible
// column, and persists the new hidden set. It refuses to hide the last visible
// column.
func (m Model) hideFocusedColumn() (tea.Model, tea.Cmd) {
	if m.focusCol < 0 || m.focusCol >= len(m.columns) {
		return m, nil
	}
	if len(m.columns) <= 1 {
		return m, m.setStatus("Can't hide the last visible column", "warn")
	}
	role := m.columns[m.focusCol].Role
	lane := m.columns[m.focusCol].Lane
	m.hidden[role] = true
	m.applyHidden()
	// The focused column is gone; clamp focus so it lands on a visible column
	// (the one that shifted into its slot, or the new last column).
	m.focusCol = clamp(m.focusCol, 0, len(m.columns)-1)
	return m, m.persistHidden("Hid " + lane + " (X shows all)")
}

// showAllColumns clears the hidden set and persists it.
func (m Model) showAllColumns() (tea.Model, tea.Cmd) {
	if len(m.hidden) == 0 {
		return m, m.setStatus("No hidden columns", "")
	}
	m.hidden = map[string]bool{}
	m.applyHidden()
	return m, m.persistHidden("Showing all columns")
}

// boardSettings are the settings keys the board owns and writes.
var boardSettings = []string{"theme", "hidden_roles"}

// saveSettings writes the board's own keys and nothing else. Both go every
// time, so the first save persists a hidden set seeded from the config
// default. Every other key on disk is re-read and kept, so a value edited by
// hand while the board was open (notify = false for the herdr hook) is not
// reverted to what the board loaded at startup.
//
// also names further keys this particular save owns: the b toggle passes
// "notify", which is otherwise left exactly as the file has it.
//
// A save therefore always republishes theme and hidden_roles from this board's
// memory, b included, and that is deliberate: writing notify through a bare
// Update would create the file without hidden_roles, and a settings file that
// exists is what stops New from re-seeding the hidden set from the [ui.board]
// config default — so the next launch would come up with the default silently
// dropped. The values written are the ones this board is showing, which is
// what the person pressing the key is looking at.
func (m Model) saveSettings(also ...string) error {
	own := make(map[string]any, len(boardSettings)+len(also))
	keys := make([]string, 0, len(boardSettings)+len(also))
	keys = append(keys, boardSettings...)
	keys = append(keys, also...)
	for _, k := range keys {
		if _, known := settings.Defaults[k]; !known {
			continue // never let an ad-hoc key reach the file
		}
		if v, ok := m.settings[k]; ok {
			own[k] = v
		}
	}
	return settings.Update(m.settingsPath, own)
}

// persistHidden stores the hidden set (sorted, comma-joined) in settings.toml,
// mirroring how the theme is persisted, and reports status.
func (m *Model) persistHidden(okMsg string) tea.Cmd {
	roles := make([]string, 0, len(m.hidden))
	for r := range m.hidden {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	// Writing "" (show-all) is load-bearing: it makes settings.toml exist, which
	// suppresses re-seeding from the config default on the next launch.
	m.settings["hidden_roles"] = strings.Join(roles, ",")
	if err := m.saveSettings(); err != nil {
		return m.setStatus(okMsg+" (not saved: "+err.Error()+")", "warn")
	}
	return m.setStatus(okMsg, "")
}

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

// moveToFirst / moveToLast jump the selection to the top / bottom card of the
// focused column (vim gg / G).
func (m *Model) moveToFirst() {
	if m.focusCol < 0 || m.focusCol >= len(m.columns) {
		return
	}
	if col := m.columns[m.focusCol]; len(col.Cards) > 0 {
		m.sel[col.Role] = 0
	}
}

func (m *Model) moveToLast() {
	if m.focusCol < 0 || m.focusCol >= len(m.columns) {
		return
	}
	if col := m.columns[m.focusCol]; len(col.Cards) > 0 {
		m.sel[col.Role] = len(col.Cards) - 1
	}
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

// WithSelect makes the board open with one ticket selected: the key the person
// asked for with --select. An empty key changes nothing.
func (m Model) WithSelect(key string) Model {
	m.selectKey = normalizeKey(key)
	m.selectExplicit = m.selectKey != ""
	return m
}

// WithSelectFunc selects a ticket the board works out for itself (--select-from-cwd
// reading a git branch). It runs as a command rather than before the program
// starts, so the board paints first; a miss is reported more quietly than a
// key the person typed. A nil func changes nothing.
func (m Model) WithSelectFunc(derive func() string) Model {
	m.selectFunc = derive
	return m
}

// normalizeKey puts a ticket key in the board's own spelling.
func normalizeKey(key string) string {
	return strings.ToUpper(strings.TrimSpace(key))
}

// WithPluginSettings says this board's settings file is the plugin one in
// herdr's state dir, which is the file the notification hook reads. Only the
// caller knows: the board is handed a path either way, and a board in a plain
// herdr pane — or one whose plugin path was rejected as a symlink — writes the
// standalone file while the hook goes on reading the plugin one. The notify
// toggle uses it to say so rather than promise a silence it cannot deliver.
func (m Model) WithPluginSettings(plugin bool) Model {
	m.pluginSettings = plugin
	return m
}

// WithPopup marks the board as herdr's popup pane. The popup has no pane id of
// its own, so a successful jump to an agent pane ends the process, which is
// what closes the popup.
func (m Model) WithPopup(popup bool) Model {
	m.popup = popup
	return m
}
