package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/tkt"
)

// captureRunner is a fake tkt Runner: it records argv and replies with canned
// JSON keyed off the verb, so the UI can be driven without a real tkt or a TTY.
//
// It has no mutex, and that is deliberate: please do not add one. Every test
// here drives the model synchronously, so there is nothing to race — and the
// unsynchronised append is itself a guard. A tkt call issued from a bare
// goroutine rather than a tea.Cmd would land concurrently with the test's own
// recorded calls, and `go test -race` would report it. A mutex would make
// that write safe, and silently take the detector away.
type captureRunner struct {
	calls [][]string
	// vcs and ownership override the canned `tkt cfg` replies the dispatch
	// guards read. "" is the default reply below; failReply is a tkt that
	// exits non-zero, which both readers treat as "not configured".
	vcs       string
	ownership string
	// observeCtx records, per call made while it is on, whether the context
	// the runner was handed carried a deadline — i.e. whether the caller's
	// budget reached the subprocess at all.
	observeCtx bool
	ctxCalls   []ctxCall
	// failVerbs makes `tkt <verb>` exit non-zero. The dispatch failure matrix
	// needs it in both directions: a transition that fails must still leave a
	// comment, and a comment that fails must not block or undo anything.
	failVerbs map[string]bool
	// roles overrides the board.roles reply. laneSeconds adds a seconds field
	// to a key's lane-time entry. viewRole sets the status_role a `tkt view`
	// of that key reports (default "todo"). failOn makes one "verb KEY" exit
	// non-zero. list overrides the `tkt list` reply. agents overrides the
	// `tkt agents` reply (default: no runs); failReply is a tkt without it.
	roles       string
	laneSeconds map[string]float64
	viewRole    map[string]string
	failOn      map[string]bool
	list        string
	agents      string
	// viewAgent and viewAgentAt are the agent_status and agent_status_at a
	// `tkt view` of a key reports (absent when unset). An `edit KEY
	// --agent-status X` writes both, stamping a fresh agent_status_at only
	// when the value changes, as tkt does, so a view after a write agrees
	// with it. stamps counts those restamps; stampNow makes each restamp the
	// package clock's own second instead of a fixed early one.
	viewAgent   map[string]string
	viewAgentAt map[string]string
	stamps      int
	stampNow    bool
}

// ctxCall is one observed invocation: whether it was bounded, how long the
// budget was, and whether its context was still live when the call was made.
type ctxCall struct {
	args     []string
	deadline bool
	live     bool
	budget   time.Duration // time left on the deadline when the call was made
}

// failReply asks captureRunner for a failed tkt invocation.
const failReply = "!fail"

func (c *captureRunner) run(ctx context.Context, bin string, args, env []string) ([]byte, []byte, int, error) {
	c.calls = append(c.calls, args)
	if c.observeCtx {
		dl, ok := ctx.Deadline()
		call := ctxCall{args: args, deadline: ok, live: ctx.Err() == nil}
		if ok {
			call.budget = time.Until(dl)
		}
		c.ctxCalls = append(c.ctxCalls, call)
	}
	if len(args) > 0 && c.failVerbs[args[0]] {
		return nil, []byte(args[0] + " exploded"), 1, nil
	}
	if len(args) > 1 && c.failOn[args[0]+" "+args[1]] {
		return nil, []byte(args[0] + " refused"), 3, nil
	}
	switch {
	case eq(args, "cfg", "board.roles", "--json"):
		return cfgReply(c.roles, `{"todo": "To Do", "done": "Done"}`)
	case len(args) >= 2 && args[0] == "list" && c.list != "":
		return []byte(c.list), nil, 0, nil
	case len(args) >= 2 && args[0] == "list":
		return []byte(`[
			{"key":"TKT-1","summary":"first thing","status_role":"todo","priority":"High","assignee":"alice","blocked_by":[]},
			{"key":"TKT-2","summary":"second thing","status_role":"done","priority":"Low","assignee":"","blocked_by":[]}
		]`), nil, 0, nil
	case eq(args, "agents", "--json"):
		if c.agents == failReply {
			return nil, []byte("invalid choice: 'agents'"), 64, nil
		}
		return cfgReply(c.agents, `{"generated":"2026-09-28T00:00:00Z","stale_after":45,"agents":[]}`)
	case len(args) >= 1 && args[0] == "lane-time":
		return laneTimeReply(args, c.laneSeconds), nil, 0, nil
	case len(args) >= 1 && args[0] == "view":
		role := "todo"
		if r, ok := c.viewRole[args[1]]; ok {
			role = r
		}
		return []byte(`{"key":"` + args[1] + `","summary":"first thing","status_role":"` + role + `","description":"d","labels":[],"blocked_by":[]` +
			c.agentFields(args[1]) + `}`), nil, 0, nil
	case len(args) >= 4 && args[0] == "edit" && args[2] == "--agent-status":
		c.writeAgent(args[1], args[3])
		return []byte(`{"key":"` + args[1] + `"` + c.agentFields(args[1]) + `}`), nil, 0, nil
	case eq(args, "cfg", "issue_types", "--json"):
		return []byte(`{"full_sdlc":["Story","Bug"],"deliverable":["Task"]}`), nil, 0, nil
	case eq(args, "cfg", "vcs", "--json"):
		return cfgReply(c.vcs, `{"provider":"github","repo":"olddognewflex/tktban",`+
			`"default_branch":"main","branch_fmt":"feature/{key-lower}-{slug}",`+
			`"hotfix_fmt":"hotfix/{key-lower}-{slug}"}`)
	case eq(args, "cfg", "board.ownership", "--json"):
		return cfgReply(c.ownership, `{"todo->done":"agent"}`)
	case eq(args, "cfg", "priorities", "--json"):
		return []byte(`["Highest","High","Medium","Low","Lowest"]`), nil, 0, nil
	case len(args) >= 2 && args[0] == "apply" && args[1] == "--template":
		return []byte("---\ntype: Story\npriority: Medium\n---\n# summary\n\nbody\n"), nil, 0, nil
	case len(args) >= 1 && args[0] == "apply":
		return []byte(`{"key":"TKB-99"}`), nil, 0, nil
	default: // transition, comment, edit, create
		return []byte(`{"key":"TKT-1"}`), nil, 0, nil
	}
}

// agentFields is the agent_status JSON fields for key, with a leading comma,
// or "" when it has none.
func (c *captureRunner) agentFields(key string) string {
	st, ok := c.viewAgent[key]
	if !ok {
		return ""
	}
	return `,"agent_status":"` + st + `","agent_status_at":"` + c.viewAgentAt[key] + `"`
}

// writeAgent applies an `edit --agent-status`: the stamp moves only when the
// value changes or there is none yet, as tkt's markdown backend does
// (adapters/markdown.py edit) — re-asserting the same value keeps its stamp.
func (c *captureRunner) writeAgent(key, st string) {
	if c.viewAgent == nil {
		c.viewAgent, c.viewAgentAt = map[string]string{}, map[string]string{}
	}
	if cur, ok := c.viewAgent[key]; ok && cur == st && c.viewAgentAt[key] != "" {
		return
	}
	c.stamps++
	c.viewAgent[key] = st
	c.viewAgentAt[key] = fmt.Sprintf("2026-10-01T00:00:%02dZ", c.stamps)
	if c.stampNow {
		c.viewAgentAt[key] = now().UTC().Format(time.RFC3339)
	}
}

// cfgReply serves an overridden `tkt cfg` reply, the default, or a failure.
func cfgReply(override, def string) ([]byte, []byte, int, error) {
	switch override {
	case "":
		return []byte(def), nil, 0, nil
	case failReply:
		return nil, []byte("config error"), 2, nil
	default:
		return []byte(override), nil, 0, nil
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

// laneTimeReply echoes one worklog entry per requested key so the count matches,
// with seconds for any key that has them.
func laneTimeReply(args []string, seconds map[string]float64) []byte {
	keys := ""
	for i, a := range args {
		if a == "--keys" && i+1 < len(args) {
			keys = args[i+1]
		}
	}
	var entries []string
	for pair := range strings.SplitSeq(keys, ",") {
		k := strings.SplitN(pair, ":", 2)[0]
		secs := ""
		if v, ok := seconds[k]; ok {
			secs = `,"seconds":` + strconv.FormatFloat(v, 'f', -1, 64)
		}
		entries = append(entries, `{"key":"`+k+`","human":"1h 2m"`+secs+`}`)
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
	msg := m.refreshCmd()()
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
