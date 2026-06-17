package ui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/olddognewflex/tktban/internal/model"
	"github.com/olddognewflex/tktban/internal/ticket"
	"github.com/olddognewflex/tktban/internal/tkt"
)

// modal is an overlay dialog. Like Textual's ModalScreen it only collects input;
// it returns its outcome as a message (via the returned tea.Cmd) that the root
// model handles and then clears the modal. The lone exception is the creator,
// which stays open while its create runs (see createModal).
type modal interface {
	Update(msg tea.Msg) (modal, tea.Cmd)
	View(st styles, width, height int) string
}

// ---- result messages ----

type moveResultMsg struct {
	role      string
	cancelled bool
}

type commentResultMsg struct {
	body      string
	cancelled bool
}

type filterResultMsg struct {
	assignee  string
	prefix    string
	cancelled bool
}

type editResultMsg struct {
	key       string
	opts      tkt.EditOpts
	changed   bool
	cancelled bool
}

type viewerClosedMsg struct{}

type createSubmitMsg struct{ payload createPayload }

type createCancelMsg struct{}

func send(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }

func isKey(msg tea.Msg, names ...string) (string, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return "", false
	}
	s := k.String()
	return s, slices.Contains(names, s)
}

// dialogBox centers a rendered dialog body on screen.
func dialogBox(st styles, width, height int, body string) string {
	box := st.dialog.Render(body)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// ---- Move ----

type moveModal struct {
	card   model.Card
	roles  []string
	cursor int
}

func newMoveModal(card model.Card, roles []string) moveModal {
	return moveModal{card: card, roles: roles}
}

func (m moveModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	if s, ok := msg.(tea.KeyMsg); ok {
		switch s.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.roles)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.roles) > 0 {
				return m, send(moveResultMsg{role: m.roles[m.cursor]})
			}
		case "esc", "escape":
			return m, send(moveResultMsg{cancelled: true})
		}
	}
	return m, nil
}

func (m moveModal) View(st styles, width, height int) string {
	var b strings.Builder
	b.WriteString(st.dialogTitle.Render("Move "+m.card.Key+" to…") + "\n")
	for i, r := range m.roles {
		cursor := "  "
		line := r
		if i == m.cursor {
			cursor = "▸ "
			line = st.cardMeta.Render(r)
		}
		b.WriteString(cursor + line + "\n")
	}
	b.WriteString("\n" + st.fieldLabel.Render("↑/↓ select · enter move · esc cancel"))
	return dialogBox(st, width, height, b.String())
}

// ---- Comment ----

type commentModal struct {
	card  model.Card
	input textinput.Model
}

func newCommentModal(card model.Card) commentModal {
	ti := textinput.New()
	ti.Placeholder = "comment… (enter to submit, esc to cancel)"
	ti.Focus()
	ti.Width = 50
	return commentModal{card: card, input: ti}
}

func (m commentModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	if _, ok := isKey(msg, "esc", "escape"); ok {
		return m, send(commentResultMsg{cancelled: true})
	}
	if _, ok := isKey(msg, "enter"); ok {
		body := strings.TrimSpace(m.input.Value())
		if body == "" {
			return m, send(commentResultMsg{cancelled: true})
		}
		return m, send(commentResultMsg{body: body})
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m commentModal) View(st styles, width, height int) string {
	body := st.dialogTitle.Render("Comment on "+m.card.Key) + "\n" + m.input.View()
	return dialogBox(st, width, height, body)
}

// ---- Filter ----

type filterModal struct {
	inputs []textinput.Model
	focus  int
}

func newFilterModal(current filterState) filterModal {
	assignee := textinput.New()
	assignee.Placeholder = "assignee (blank = any)"
	assignee.SetValue(current.assignee)
	assignee.Focus()
	assignee.Width = 40
	prefix := textinput.New()
	prefix.Placeholder = "key prefix, e.g. TKB (blank = any)"
	prefix.SetValue(current.prefix)
	prefix.Width = 40
	return filterModal{inputs: []textinput.Model{assignee, prefix}}
}

func (m filterModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	switch {
	case keyIn(msg, "esc", "escape"):
		return m, send(filterResultMsg{cancelled: true})
	case keyIn(msg, "ctrl+r"): // clear
		return m, send(filterResultMsg{assignee: "", prefix: ""})
	case keyIn(msg, "enter"):
		return m, send(filterResultMsg{
			assignee: strings.TrimSpace(m.inputs[0].Value()),
			prefix:   strings.TrimSpace(m.inputs[1].Value()),
		})
	case keyIn(msg, "tab", "down"):
		m.focus = (m.focus + 1) % len(m.inputs)
		m.refocus()
	case keyIn(msg, "shift+tab", "up"):
		m.focus = (m.focus - 1 + len(m.inputs)) % len(m.inputs)
		m.refocus()
	default:
		var cmd tea.Cmd
		m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *filterModal) refocus() {
	for i := range m.inputs {
		if i == m.focus {
			m.inputs[i].Focus()
		} else {
			m.inputs[i].Blur()
		}
	}
}

func (m filterModal) View(st styles, width, height int) string {
	var b strings.Builder
	b.WriteString(st.dialogTitle.Render("Filter cards") + "\n")
	b.WriteString(st.fieldLabel.Render("assignee") + "\n" + m.inputs[0].View() + "\n\n")
	b.WriteString(st.fieldLabel.Render("key prefix") + "\n" + m.inputs[1].View() + "\n\n")
	b.WriteString(st.fieldLabel.Render("tab switch · enter apply · ctrl+r clear · esc cancel"))
	return dialogBox(st, width, height, b.String())
}

// ---- Edit ----

type editModal struct {
	key     string
	orig    ticket.Fields
	summary textinput.Model
	desc    textarea.Model
	prio    textinput.Model
	asgn    textinput.Model
	labels  textinput.Model
	focus   int // 0..4
}

func newEditModal(t model.Ticket) editModal {
	orig := ticket.Fields{
		Summary:     str(t["summary"]),
		Description: str(t["description"]),
		Priority:    str(t["priority"]),
		Assignee:    str(t["assignee"]),
		Labels:      strList(t["labels"]),
	}
	mk := func(val, ph string) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = ph
		ti.SetValue(val)
		ti.Width = 50
		return ti
	}
	ta := textarea.New()
	ta.SetValue(orig.Description)
	ta.SetHeight(4)
	ta.SetWidth(52)
	em := editModal{
		key:     str(t["key"]),
		orig:    orig,
		summary: mk(orig.Summary, "summary"),
		desc:    ta,
		prio:    mk(orig.Priority, "priority"),
		asgn:    mk(orig.Assignee, "assignee"),
		labels:  mk(strings.Join(orig.Labels, ", "), "labels (comma-separated)"),
	}
	em.refocus()
	return em
}

func (m *editModal) refocus() {
	m.summary.Blur()
	m.desc.Blur()
	m.prio.Blur()
	m.asgn.Blur()
	m.labels.Blur()
	switch m.focus {
	case 0:
		m.summary.Focus()
	case 1:
		m.desc.Focus()
	case 2:
		m.prio.Focus()
	case 3:
		m.asgn.Focus()
	case 4:
		m.labels.Focus()
	}
}

func (m editModal) current() ticket.Fields {
	return ticket.Fields{
		Summary:     m.summary.Value(),
		Description: m.desc.Value(),
		Priority:    m.prio.Value(),
		Assignee:    m.asgn.Value(),
		Labels:      parseLabels(m.labels.Value()),
	}
}

func (m editModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	switch {
	case keyIn(msg, "esc", "escape"):
		return m, send(editResultMsg{key: m.key, cancelled: true})
	case keyIn(msg, "ctrl+s"):
		opts, changed := ticket.ComputeEdit(m.orig, m.current())
		return m, send(editResultMsg{key: m.key, opts: opts, changed: changed})
	case keyIn(msg, "tab"):
		m.focus = (m.focus + 1) % 5
		m.refocus()
		return m, nil
	case keyIn(msg, "shift+tab"):
		m.focus = (m.focus - 1 + 5) % 5
		m.refocus()
		return m, nil
	}
	var cmd tea.Cmd
	switch m.focus {
	case 0:
		m.summary, cmd = m.summary.Update(msg)
	case 1:
		m.desc, cmd = m.desc.Update(msg)
	case 2:
		m.prio, cmd = m.prio.Update(msg)
	case 3:
		m.asgn, cmd = m.asgn.Update(msg)
	case 4:
		m.labels, cmd = m.labels.Update(msg)
	}
	return m, cmd
}

func (m editModal) View(st styles, width, height int) string {
	var b strings.Builder
	b.WriteString(st.dialogTitle.Render("Edit "+m.key) + "\n")
	b.WriteString(st.fieldLabel.Render("summary") + "\n" + m.summary.View() + "\n")
	b.WriteString(st.fieldLabel.Render("description") + "\n" + m.desc.View() + "\n")
	b.WriteString(st.fieldLabel.Render("priority") + "\n" + m.prio.View() + "\n")
	b.WriteString(st.fieldLabel.Render("assignee") + "\n" + m.asgn.View() + "\n")
	b.WriteString(st.fieldLabel.Render("labels") + "\n" + m.labels.View() + "\n\n")
	b.WriteString(st.fieldLabel.Render("tab next field · ctrl+s save · esc cancel"))
	return dialogBox(st, width, height, b.String())
}

// ---- Create ----

type createModal struct {
	types   []string
	typeIdx int // -1 = none chosen
	summary textinput.Model
	desc    textarea.Model
	accept  textarea.Model
	prio    textinput.Model
	asgn    textinput.Model
	labels  textinput.Model
	focus   int // 0..6 (0 = type picker)
	busy    bool
	errMsg  string
}

func newCreateModal(types []string) createModal {
	mk := func(ph string) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = ph
		ti.Width = 50
		return ti
	}
	mkArea := func() textarea.Model {
		ta := textarea.New()
		ta.SetHeight(3)
		ta.SetWidth(52)
		return ta
	}
	cm := createModal{
		types:   types,
		typeIdx: -1,
		summary: mk("summary"),
		desc:    mkArea(),
		accept:  mkArea(),
		prio:    mk("priority (optional)"),
		asgn:    mk("assignee (optional, blank = you)"),
		labels:  mk("labels (optional, comma-separated)"),
	}
	cm.refocus()
	return cm
}

func (m *createModal) refocus() {
	m.summary.Blur()
	m.desc.Blur()
	m.accept.Blur()
	m.prio.Blur()
	m.asgn.Blur()
	m.labels.Blur()
	switch m.focus {
	case 1:
		m.summary.Focus()
	case 2:
		m.desc.Focus()
	case 3:
		m.accept.Focus()
	case 4:
		m.prio.Focus()
	case 5:
		m.asgn.Focus()
	case 6:
		m.labels.Focus()
	}
}

func (m createModal) typeName() string {
	if m.typeIdx < 0 || m.typeIdx >= len(m.types) {
		return "(choose)"
	}
	return m.types[m.typeIdx]
}

func (m createModal) validate() (createPayload, string) {
	if m.typeIdx < 0 || m.typeIdx >= len(m.types) {
		return createPayload{}, "Choose a type."
	}
	summary := strings.TrimSpace(m.summary.Value())
	if summary == "" {
		return createPayload{}, "Summary is required."
	}
	return createPayload{
		issueType: m.types[m.typeIdx],
		summary:   summary,
		priority:  strings.TrimSpace(m.prio.Value()),
		assignee:  strings.TrimSpace(m.asgn.Value()),
		body:      ticket.BuildBody(m.desc.Value(), m.accept.Value()),
		labels:    parseLabels(m.labels.Value()),
	}, ""
}

func (m createModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	if m.busy {
		return m, nil // ignore input while the create is in flight
	}
	switch {
	case keyIn(msg, "esc", "escape"):
		return m, send(createCancelMsg{})
	case keyIn(msg, "ctrl+s"):
		payload, err := m.validate()
		if err != "" {
			m.errMsg = err
			return m, nil
		}
		m.errMsg = ""
		m.busy = true
		return m, send(createSubmitMsg{payload: payload})
	case keyIn(msg, "tab"):
		m.focus = (m.focus + 1) % 7
		m.refocus()
		return m, nil
	case keyIn(msg, "shift+tab"):
		m.focus = (m.focus - 1 + 7) % 7
		m.refocus()
		return m, nil
	}
	if m.focus == 0 { // type picker
		if keyIn(msg, "left", "h") && len(m.types) > 0 {
			m.typeIdx = (m.typeIdx - 1 + len(m.types)) % len(m.types)
		} else if keyIn(msg, "right", "l") && len(m.types) > 0 {
			m.typeIdx = (m.typeIdx + 1) % len(m.types)
		}
		return m, nil
	}
	var cmd tea.Cmd
	switch m.focus {
	case 1:
		m.summary, cmd = m.summary.Update(msg)
	case 2:
		m.desc, cmd = m.desc.Update(msg)
	case 3:
		m.accept, cmd = m.accept.Update(msg)
	case 4:
		m.prio, cmd = m.prio.Update(msg)
	case 5:
		m.asgn, cmd = m.asgn.Update(msg)
	case 6:
		m.labels, cmd = m.labels.Update(msg)
	}
	return m, cmd
}

// setError/clearBusy are driven by the root when a create command returns.
func (m createModal) withError(message string) createModal {
	m.busy = false
	m.errMsg = message
	return m
}

func (m createModal) View(st styles, width, height int) string {
	typeLine := "type: " + m.typeName()
	if m.focus == 0 {
		typeLine = st.cardMeta.Render("▸ " + typeLine + "  ◄ ►")
	} else {
		typeLine = "  " + typeLine
	}
	var b strings.Builder
	b.WriteString(st.dialogTitle.Render("New ticket") + "\n")
	b.WriteString(typeLine + "\n\n")
	b.WriteString(st.fieldLabel.Render("summary") + "\n" + m.summary.View() + "\n")
	b.WriteString(st.fieldLabel.Render("description") + "\n" + m.desc.View() + "\n")
	b.WriteString(st.fieldLabel.Render("acceptance — one per line") + "\n" + m.accept.View() + "\n")
	b.WriteString(st.fieldLabel.Render("priority") + "\n" + m.prio.View() + "\n")
	b.WriteString(st.fieldLabel.Render("assignee") + "\n" + m.asgn.View() + "\n")
	b.WriteString(st.fieldLabel.Render("labels") + "\n" + m.labels.View() + "\n")
	if m.errMsg != "" {
		b.WriteString("\n" + st.errorText.Render(m.errMsg) + "\n")
	}
	hint := "tab next · ←/→ pick type · ctrl+s create · esc cancel"
	if m.busy {
		hint = "Creating…"
	}
	b.WriteString("\n" + st.fieldLabel.Render(hint))
	return dialogBox(st, width, height, b.String())
}

// ---- Viewer ----

type viewerModal struct {
	vp    viewport.Model
	ready bool
	md    string
}

func newViewerModal(t model.Ticket) viewerModal {
	return viewerModal{md: ticket.Markdown(t)}
}

func (m viewerModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	if keyIn(msg, "esc", "escape", "q") {
		return m, send(viewerClosedMsg{})
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m viewerModal) View(st styles, width, height int) string {
	w := width * 8 / 10
	h := height * 8 / 10
	if w < 20 {
		w = width
	}
	if h < 6 {
		h = height
	}
	m.vp.Width = w
	m.vp.Height = h
	if !m.ready {
		rendered := m.md
		if out, err := glamour.Render(m.md, glamourStyle(st.t)); err == nil {
			rendered = out
		}
		m.vp.SetContent(rendered)
		m.ready = true
	}
	box := st.dialog.Render(m.vp.View() + "\n" + st.fieldLabel.Render("↑/↓ scroll · esc/q close"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

func glamourStyle(t theme) string {
	// Glamour ships "dark"/"light" base styles; pick by the theme's surface.
	if t.name == "textual-light" {
		return "light"
	}
	return "dark"
}

// ---- shared helpers ----

func keyIn(msg tea.Msg, names ...string) bool {
	_, ok := isKey(msg, names...)
	return ok
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strList(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func parseLabels(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
