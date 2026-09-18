package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"
)

// pipeClient returns a Client whose every dial gets a fresh net.Pipe served by
// reply: it reads the one request line, hands the decoded request to reply, and
// writes back whatever reply returns (nothing when it returns "").
func pipeClient(t *testing.T, reply func(req map[string]any) string) *Client {
	t.Helper()
	return &Client{dial: func(context.Context) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			line, err := bufio.NewReader(server).ReadBytes('\n')
			if err != nil {
				return
			}
			var req map[string]any
			if json.Unmarshal(line, &req) != nil {
				return
			}
			if out := reply(req); out != "" {
				server.Write([]byte(out + "\n"))
				return
			}
			// Hang until the client gives up (timeout test).
			server.Read(make([]byte, 1))
		}()
		return client, nil
	}}
}

// agentListReply is trimmed from a real herdr 0.9.0 `agent.list`, extra fields
// and nulls included, so the decoder is tested against what herdr sends.
const agentListReply = `{"id":"tktban:1","result":{"type":"agent_list","agents":[
{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"6861977f"},"agent_status":"working","cwd":"/src/tktban","focused":true,"foreground_cwd":"/src/tktban-wt","pane_id":"wC:p1","revision":11,"state_change_seq":507,"tab_id":"wC:t1","terminal_id":"term_65bb","terminal_title":"✳ Claude Code","terminal_title_stripped":"Claude Code","workspace_id":"wC","tokens":null,"state_labels":["x"]},
{"agent":null,"agent_status":"unknown","cwd":null,"focused":false,"foreground_cwd":null,"pane_id":"wD:p2","revision":1,"tab_id":"wD:t1","terminal_id":"term_2","workspace_id":"wD"}]}}`

func TestAgentListDecodesRealReply(t *testing.T) {
	var method string
	c := pipeClient(t, func(req map[string]any) string {
		method, _ = req["method"].(string)
		if _, ok := req["params"].(map[string]any); !ok {
			t.Errorf("params must be an object, got %v", req["params"])
		}
		return compact(t, agentListReply)
	})
	agents, err := c.AgentList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if method != "agent.list" {
		t.Fatalf("method = %q", method)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}
	a := agents[0]
	if a.PaneID != "wC:p1" || a.WorkspaceID != "wC" || a.TabID != "wC:t1" || a.Status != StatusWorking ||
		a.Cwd != "/src/tktban" || a.ForegroundCwd != "/src/tktban-wt" || !a.Focused || a.Agent == nil || *a.Agent != "claude" {
		t.Fatalf("agent 0 decoded wrong: %+v", a)
	}
	if b := agents[1]; b.Agent != nil || b.Cwd != "" || b.ForegroundCwd != "" || b.Status != StatusUnknown {
		t.Fatalf("nulls decoded wrong: %+v", b)
	}
}

func compact(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func TestCallReturnsAPIError(t *testing.T) {
	c := pipeClient(t, func(map[string]any) string {
		return `{"id":"","error":{"code":"agent_not_found","message":"agent target zz not found"}}`
	})
	err := c.Call(context.Background(), "agent.get", map[string]string{"target": "zz"}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "agent_not_found" {
		t.Fatalf("want APIError agent_not_found, got %v", err)
	}
}

func TestCallRejectsGarbage(t *testing.T) {
	c := pipeClient(t, func(map[string]any) string { return "not json" })
	if _, err := c.AgentList(context.Background()); err == nil {
		t.Fatal("garbage reply must be an error")
	}
}

func TestPingReadsProtocol(t *testing.T) {
	c := pipeClient(t, func(req map[string]any) string {
		if req["method"] != "ping" {
			t.Errorf("method = %v", req["method"])
		}
		return `{"id":"x","result":{"type":"pong","version":"0.9.0","protocol":22,"capabilities":{"live_handoff":true}}}`
	})
	p, err := c.Ping(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != "0.9.0" || p.Protocol != 22 {
		t.Fatalf("pong = %+v", p)
	}
}

func TestCallTimesOut(t *testing.T) {
	c := pipeClient(t, func(map[string]any) string { return "" }) // never replies
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.AgentList(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want deadline exceeded, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call outlived its context")
	}
}

func TestCallDialError(t *testing.T) {
	c := NewClient("/nonexistent/herdr.sock")
	if _, err := c.Ping(context.Background()); err == nil {
		t.Fatal("dialing a missing socket must fail")
	}
}
