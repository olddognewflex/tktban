package ui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/tkt"
)

// captureRunner is a fake tkt Runner: it records argv and replies with canned
// JSON keyed off the verb, so the UI can be driven without a real tkt or a TTY.
type captureRunner struct {
	calls [][]string
}

func (c *captureRunner) run(bin string, args, env []string) ([]byte, []byte, int, error) {
	c.calls = append(c.calls, args)
	switch {
	case eq(args, "cfg", "board.roles", "--json"):
		return []byte(`{"todo": "To Do", "done": "Done"}`), nil, 0, nil
	case len(args) >= 2 && args[0] == "list":
		return []byte(`[
			{"key":"TKT-1","summary":"first thing","status_role":"todo","priority":"High","assignee":"alice","blocked_by":[]},
			{"key":"TKT-2","summary":"second thing","status_role":"done","priority":"Low","assignee":"","blocked_by":[]}
		]`), nil, 0, nil
	case len(args) >= 1 && args[0] == "lane-time":
		return laneTimeReply(args), nil, 0, nil
	case len(args) >= 1 && args[0] == "view":
		return []byte(`{"key":"` + args[1] + `","summary":"first thing","status_role":"todo","description":"d","labels":[],"blocked_by":[]}`), nil, 0, nil
	case eq(args, "cfg", "issue_types", "--json"):
		return []byte(`{"full_sdlc":["Story","Bug"],"deliverable":["Task"]}`), nil, 0, nil
	default: // transition, comment, edit, create
		return []byte(`{"key":"TKT-1"}`), nil, 0, nil
	}
}

func (c *captureRunner) last(verb string) []string {
	for i := len(c.calls) - 1; i >= 0; i-- {
		if len(c.calls[i]) > 0 && c.calls[i][0] == verb {
			return c.calls[i]
		}
	}
	return nil
}

func eq(args []string, want ...string) bool {
	if len(args) != len(want) {
		return false
	}
	for i := range want {
		if args[i] != want[i] {
			return false
		}
	}
	return true
}

// laneTimeReply echoes one worklog entry per requested key so the count matches.
func laneTimeReply(args []string) []byte {
	keys := ""
	for i, a := range args {
		if a == "--keys" && i+1 < len(args) {
			keys = args[i+1]
		}
	}
	var entries []string
	for pair := range strings.SplitSeq(keys, ",") {
		k := strings.SplitN(pair, ":", 2)[0]
		entries = append(entries, `{"key":"`+k+`","human":"1h 2m"}`)
	}
	return []byte("[" + strings.Join(entries, ",") + "]")
}

func testModel(t *testing.T) (Model, *captureRunner) {
	t.Helper()
	cr := &captureRunner{}
	tk := tkt.New("", "tkt").WithRunner(cr.run)
	m := New(tk, 10, true, filepath.Join(t.TempDir(), "settings.toml"))
	m.width, m.height = 120, 30
	return m, cr
}

func step(m Model, msg tea.Msg) Model {
	nm, _ := m.Update(msg)
	return nm.(Model)
}

// loadBoard runs a refresh synchronously and feeds the result into the model.
func loadBoard(m Model) Model {
	msg := refreshCmd(m.tkt, m.filter)()
	return step(m, msg)
}

func key(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestBoardLoadsAndRenders(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	view := m.View()
	for _, want := range []string{"To Do", "Done", "TKT-1", "TKT-2", "first thing", "@alice"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "2 tickets") {
		t.Fatalf("subtitle missing count:\n%s", view)
	}
}

func TestNavigationAndSelection(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	card, ok := m.selectedCard()
	if !ok || card.Key != "TKT-1" {
		t.Fatalf("initial selection = %v ok=%v", card.Key, ok)
	}
	m = step(m, key("l")) // move to "done" column
	card, _ = m.selectedCard()
	if card.Key != "TKT-2" {
		t.Fatalf("after right, selection = %v", card.Key)
	}
}

func TestSelectionRestoredAcrossRefresh(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	m = step(m, key("l")) // select done/TKT-2
	m = loadBoard(m)      // refresh
	card, ok := m.selectedCard()
	if !ok || card.Key != "TKT-2" {
		t.Fatalf("selection not restored: %v ok=%v", card.Key, ok)
	}
}

func TestMoveDispatchesTransition(t *testing.T) {
	m, cr := testModel(t)
	m = loadBoard(m)
	m = step(m, key("m")) // open move modal on TKT-1
	if m.modal == nil {
		t.Fatal("move modal did not open")
	}
	// Simulate the modal returning a chosen role.
	nm, cmd := m.Update(moveResultMsg{role: "done"})
	m = nm.(Model)
	if m.modal != nil {
		t.Fatal("modal should close after move result")
	}
	if cmd == nil {
		t.Fatal("expected a transition command")
	}
	if wm, ok := cmd().(writeMsg); !ok || wm.err != nil {
		t.Fatalf("transition write failed: %+v", cmd())
	}
	if got := cr.last("transition"); !eq(got, "transition", "TKT-1", "done") {
		t.Fatalf("transition argv = %v", got)
	}
}

func TestNewOpensCreatorWithTypes(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	// 'n' fetches issue types; run that command and feed the result.
	_, cmd := m.Update(key("n"))
	msg := cmd()
	itm, ok := msg.(issueTypesMsg)
	if !ok {
		t.Fatalf("expected issueTypesMsg, got %T", msg)
	}
	if strings.Join(itm.types, ",") != "Story,Bug,Task" {
		t.Fatalf("types = %v", itm.types)
	}
	m = step(m, itm)
	if _, ok := m.modal.(createModal); !ok {
		t.Fatalf("create modal not opened, modal=%T", m.modal)
	}
}

func TestToggleAutoUpdatesSubtitle(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	if !strings.Contains(m.View(), "auto 10s") {
		t.Fatalf("expected auto on in subtitle:\n%s", m.View())
	}
	m = step(m, key("a"))
	if !strings.Contains(m.View(), "auto off") {
		t.Fatalf("expected auto off after toggle:\n%s", m.View())
	}
}

func TestThemeCyclePersists(t *testing.T) {
	m, _ := testModel(t)
	before := m.themeName
	m = step(m, key("t"))
	if m.themeName == before {
		t.Fatal("theme did not change")
	}
	// Reload settings from disk via a fresh model on the same path.
	m2 := New(m.tkt, 10, true, m.settingsPath)
	if m2.themeName != m.themeName {
		t.Fatalf("theme not persisted: %q != %q", m2.themeName, m.themeName)
	}
}

func TestFilterFlow(t *testing.T) {
	m, _ := testModel(t)
	m = loadBoard(m)
	nm, _ := m.Update(filterResultMsg{assignee: "alice"})
	m = nm.(Model)
	m = loadBoard(m)
	if !strings.Contains(m.View(), "filter: @alice") {
		t.Fatalf("filter label missing:\n%s", m.View())
	}
	// alice owns only TKT-1, so the done column should now be empty of TKT-2.
	if strings.Contains(m.View(), "TKT-2") {
		t.Fatalf("filter did not drop TKT-2:\n%s", m.View())
	}
}

func TestHelpers(t *testing.T) {
	if got := parseLabels(" a , ,b ,"); strings.Join(got, ",") != "a,b" {
		t.Fatalf("parseLabels = %v", got)
	}
	if truncate("hello world", 5) != "hell…" {
		t.Fatalf("truncate = %q", truncate("hello world", 5))
	}
	if truncate("hi", 5) != "hi" {
		t.Fatalf("truncate short changed value")
	}
}
