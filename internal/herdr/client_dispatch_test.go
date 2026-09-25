package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"reflect"
	"testing"
)

// pinRequest serves one request over a REAL unix socket, hands the raw
// request line back, and replies with reply.
//
// A real socket rather than a net.Pipe because this is the test that has to
// fail on a herdr upgrade: the thing that rots is the parameter names on the
// wire, and the only honest way to pin them is to read the bytes a genuine
// connection carried.
func pinRequest(t *testing.T, reply string) (*Client, func() []byte) {
	t.Helper()
	got := make(chan []byte, 1)
	c := realSocket(t, func(conn net.Conn) {
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			t.Errorf("server: read request: %v", err)
			return
		}
		got <- line
		conn.Write([]byte(reply + "\n"))
	})
	return c, func() []byte {
		select {
		case line := <-got:
			return line
		default:
			t.Fatal("the server never saw a request")
			return nil
		}
	}
}

// params decodes the params object of a recorded request line.
func params(t *testing.T, line []byte) (string, map[string]any) {
	t.Helper()
	var req struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		t.Fatalf("bad request line %q: %v", line, err)
	}
	return req.Method, req.Params
}

// The wire shape of the one call a dry-run dispatch makes, pinned over a real
// socket: the method name, the exact parameter names, and — just as
// important — that trust_repository is NOT sent. Trusting a repository is a
// write, and listing has to stay a read.
func TestWorktreeListRequestJSONPinned(t *testing.T) {
	c, recorded := pinRequest(t, `{"id":"x","result":{"type":"worktree_list",`+
		`"source":{"repo_key":"github.com/olddognewflex/tktban","repo_name":"tktban",`+
		`"repo_root":"/src/tktban","source_checkout_path":"/src/tktban","source_workspace_id":"wC"},`+
		`"worktrees":[`+
		`{"path":"/src/tktban","branch":"main","label":"tktban","open_workspace_id":"wC",`+
		`"is_bare":false,"is_detached":false,"is_linked_worktree":false,"is_prunable":false},`+
		`{"path":"/wt/tkb-24","branch":"feature/tkb-24-x","label":"tkb-24","open_workspace_id":null,`+
		`"is_bare":false,"is_detached":false,"is_linked_worktree":true,"is_prunable":false}]}}`)

	list, err := c.WorktreeList(context.Background(), WorktreeListParams{Cwd: "/src/tktban"})
	if err != nil {
		t.Fatal(err)
	}

	method, p := params(t, recorded())
	if method != "worktree.list" {
		t.Errorf("method = %q, want worktree.list", method)
	}
	if want := map[string]any{"cwd": "/src/tktban"}; !reflect.DeepEqual(p, want) {
		t.Errorf("params = %v, want exactly %v", p, want)
	}

	if list.Source.RepoRoot != "/src/tktban" || list.Source.RepoName != "tktban" ||
		list.Source.SourceCheckoutPath != "/src/tktban" || list.Source.SourceWorkspaceID != "wC" ||
		list.Source.RepoKey != "github.com/olddognewflex/tktban" {
		t.Errorf("source = %+v", list.Source)
	}
	if len(list.Worktrees) != 2 {
		t.Fatalf("worktrees = %+v", list.Worktrees)
	}
	if list.Worktrees[0].Branch != "main" || list.Worktrees[0].IsLinkedWorktree {
		t.Errorf("worktrees[0] = %+v", list.Worktrees[0])
	}
	// A null open_workspace_id must decode to "", not blow up the reply.
	if w := list.Worktrees[1]; w.Path != "/wt/tkb-24" || w.Branch != "feature/tkb-24-x" ||
		!w.IsLinkedWorktree || w.OpenWorkspaceID != "" {
		t.Errorf("worktrees[1] = %+v", w)
	}
}

// The calls PR 1 does not make still have to be right when they are made, so
// their parameter names are pinned now too, while the schema they were read
// out of is the one in front of us.
func TestDispatchCallRequestShapes(t *testing.T) {
	cases := []struct {
		name   string
		reply  string
		call   func(*Client) error
		method string
		params map[string]any
	}{
		{
			name: "worktree.create omits path so herdr places it",
			reply: `{"id":"x","result":{"type":"worktree_created",` +
				`"workspace":{"workspace_id":"wE","label":"tkb-25","number":5},` +
				`"tab":{"tab_id":"wE:t1","workspace_id":"wE","label":"tkb-25"},` +
				`"root_pane":{"pane_id":"wE:p1","workspace_id":"wE","tab_id":"wE:t1",` +
				`"terminal_id":"term_1","cwd":"/wt/tkb-25","focused":false,"agent_status":"unknown","revision":1},` +
				`"worktree":{"path":"/wt/tkb-25","branch":"feature/tkb-25-x","label":"tkb-25",` +
				`"is_bare":false,"is_detached":false,"is_linked_worktree":true,"is_prunable":false}}}`,
			call: func(c *Client) error {
				_, err := c.WorktreeCreate(context.Background(), WorktreeCreateParams{
					Cwd: "/src/tktban", Branch: "feature/tkb-25-x", Base: "main", Label: "tkb-25",
				})
				return err
			},
			method: "worktree.create",
			params: map[string]any{
				"cwd": "/src/tktban", "branch": "feature/tkb-25-x",
				"base": "main", "label": "tkb-25", "focus": false,
			},
		},
		{
			name: "worktree.open takes no base",
			reply: `{"id":"x","result":{"type":"worktree_opened","already_open":true,` +
				`"workspace":{"workspace_id":"wE","label":"tkb-25","number":5},` +
				`"tab":{"tab_id":"wE:t1","workspace_id":"wE","label":"tkb-25"},` +
				`"root_pane":{"pane_id":"wE:p1","workspace_id":"wE","tab_id":"wE:t1",` +
				`"terminal_id":"term_1","cwd":"/wt/tkb-25","focused":true,"agent_status":"idle","revision":2},` +
				`"worktree":{"path":"/wt/tkb-25","branch":"feature/tkb-25-x","label":"tkb-25",` +
				`"is_bare":false,"is_detached":false,"is_linked_worktree":true,"is_prunable":false}}}`,
			call: func(c *Client) error {
				_, err := c.WorktreeOpen(context.Background(), WorktreeOpenParams{
					Cwd: "/src/tktban", Branch: "feature/tkb-25-x",
				})
				return err
			},
			method: "worktree.open",
			params: map[string]any{"cwd": "/src/tktban", "branch": "feature/tkb-25-x", "focus": false},
		},
		{
			name: "agent.start names a pane that already exists",
			reply: `{"id":"x","result":{"type":"agent_started","argv":["claude"],` +
				`"agent":{"pane_id":"wE:p1","workspace_id":"wE","tab_id":"wE:t1",` +
				`"agent":"claude","agent_status":"idle","cwd":"/wt/tkb-25"}}}`,
			call: func(c *Client) error {
				_, err := c.AgentStart(context.Background(), AgentStartParams{
					Name: "tkb-25", Kind: "claude", PaneID: "wE:p1", Args: []string{"--x"},
				})
				return err
			},
			method: "agent.start",
			params: map[string]any{
				"name": "tkb-25", "kind": "claude", "pane_id": "wE:p1",
				"args": []any{"--x"},
			},
		},
		{
			name:  "agent.prompt targets the agent by name",
			reply: `{"id":"x","result":{"type":"ok"}}`,
			call: func(c *Client) error {
				return c.AgentPrompt(context.Background(), AgentPromptParams{
					Target: "tkb-25", Text: "go",
				})
			},
			method: "agent.prompt",
			params: map[string]any{"target": "tkb-25", "text": "go"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, recorded := pinRequest(t, tc.reply)
			if err := tc.call(c); err != nil {
				t.Fatal(err)
			}
			method, p := params(t, recorded())
			if method != tc.method {
				t.Errorf("method = %q, want %q", method, tc.method)
			}
			if !reflect.DeepEqual(p, tc.params) {
				t.Errorf("params = %v, want exactly %v", p, tc.params)
			}
		})
	}
}

// The replies decode into the fields a dispatch actually reads: the pane to
// start an agent in, and the worktree that was made.
func TestWorktreeCreateDecodesTheRootPane(t *testing.T) {
	c := pipeClient(t, func(map[string]any) string {
		return compact(t, `{"id":"x","result":{"type":"worktree_created",
			"workspace":{"workspace_id":"wE","label":"tkb-25","number":5,"focused":false,
			  "pane_count":1,"tab_count":1,"active_tab_id":"wE:t1","agent_status":"unknown"},
			"tab":{"tab_id":"wE:t1","workspace_id":"wE","number":1,"label":"tkb-25",
			  "focused":false,"pane_count":1,"agent_status":"unknown"},
			"root_pane":{"pane_id":"wE:p1","terminal_id":"term_1","workspace_id":"wE","tab_id":"wE:t1",
			  "focused":false,"agent_status":"unknown","revision":1,"cwd":"/wt/tkb-25",
			  "agent":null,"title":null,"tokens":null},
			"worktree":{"path":"/wt/tkb-25","branch":"feature/tkb-25-x","label":"tkb-25",
			  "open_workspace_id":"wE","is_bare":false,"is_detached":false,
			  "is_linked_worktree":true,"is_prunable":false}}}`)
	})
	got, err := c.WorktreeCreate(context.Background(), WorktreeCreateParams{Branch: "feature/tkb-25-x"})
	if err != nil {
		t.Fatal(err)
	}
	if got.RootPane.PaneID != "wE:p1" || got.RootPane.Cwd != "/wt/tkb-25" {
		t.Errorf("root pane = %+v", got.RootPane)
	}
	if got.Workspace.WorkspaceID != "wE" || got.Tab.TabID != "wE:t1" {
		t.Errorf("workspace/tab = %+v / %+v", got.Workspace, got.Tab)
	}
	if got.Worktree.Path != "/wt/tkb-25" || got.Worktree.Branch != "feature/tkb-25-x" || !got.Worktree.IsLinkedWorktree {
		t.Errorf("worktree = %+v", got.Worktree)
	}
	if got.AlreadyOpen {
		t.Error("worktree.create must not report already_open")
	}
}

func TestAgentStartDecodesArgvAndSurfacesBusy(t *testing.T) {
	c := pipeClient(t, func(map[string]any) string {
		return `{"id":"x","result":{"type":"agent_started","argv":["claude","--x"],` +
			`"agent":{"pane_id":"wE:p1","agent":"claude","agent_status":"idle"}}}`
	})
	got, err := c.AgentStart(context.Background(), AgentStartParams{Name: "tkb-25", Kind: "claude", PaneID: "wE:p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Argv) != 2 || got.Argv[0] != "claude" {
		t.Errorf("argv = %v", got.Argv)
	}
	if got.Agent.PaneID != "wE:p1" || got.Agent.Status != StatusIdle {
		t.Errorf("agent = %+v", got.Agent)
	}

	// The expected failure right after a worktree is created: the pane exists
	// but its shell is not at a prompt yet. It must arrive as a code a caller
	// can branch on, not as an opaque string.
	busy := pipeClient(t, func(map[string]any) string {
		return `{"id":"x","error":{"code":"agent_pane_busy","message":"pane is not at a shell prompt"}}`
	})
	_, err = busy.AgentStart(context.Background(), AgentStartParams{Name: "tkb-25", Kind: "claude", PaneID: "wE:p1"})
	if code := ErrorCode(err); code != CodeAgentPaneBusy {
		t.Fatalf("error code = %q, want %q", code, CodeAgentPaneBusy)
	}
}

// The timeout herdr will accept is bounded on both sides; a zero value must
// be left off the wire so herdr uses its own default rather than rejecting 0.
func TestAgentStartOmitsAZeroTimeout(t *testing.T) {
	c, recorded := pinRequest(t, `{"id":"x","result":{"type":"agent_started","argv":[],`+
		`"agent":{"pane_id":"wE:p1","agent_status":"idle"}}}`)
	if _, err := c.AgentStart(context.Background(), AgentStartParams{
		Name: "tkb-25", Kind: "claude", PaneID: "wE:p1",
	}); err != nil {
		t.Fatal(err)
	}
	_, p := params(t, recorded())
	if _, present := p["timeout_ms"]; present {
		t.Fatalf("params = %v, want no timeout_ms", p)
	}
	if _, present := p["args"]; present {
		t.Fatalf("params = %v, want no args when there are none", p)
	}
}
