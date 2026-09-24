package herdr

import (
	"context"
	"errors"
	"net"
	"testing"
)

// agent.get takes "target", not "pane_id" (docs/herdr-events.md, TKB-22).
func TestAgentGetSendsTarget(t *testing.T) {
	var method string
	var params map[string]any
	c := pipeClient(t, func(req map[string]any) string {
		method, _ = req["method"].(string)
		params, _ = req["params"].(map[string]any)
		return `{"id":"x","result":{"type":"agent_info","agent":{"pane_id":"wD:p1","agent_status":"blocked","focused":false}}}`
	})
	a, err := c.AgentGet(context.Background(), "wD:p1")
	if err != nil {
		t.Fatal(err)
	}
	if method != "agent.get" || params["target"] != "wD:p1" || len(params) != 1 {
		t.Fatalf("sent %s %v, want agent.get {target}", method, params)
	}
	if a.PaneID != "wD:p1" || a.Status != StatusBlocked {
		t.Fatalf("agent = %+v", a)
	}
}

func TestAgentGetDecodesStateChangeSeq(t *testing.T) {
	c := realSocket(t, func(conn net.Conn) {
		readReq(t, conn)
		// Trimmed from a real herdr 0.9.0 agent.get reply.
		conn.Write([]byte(`{"id":"x","result":{"type":"agent_info","agent":{"agent":"claude","agent_status":"blocked","cwd":"/src","focused":true,"foreground_cwd":"/src/wt","pane_id":"wD:p1","revision":4,"state_change_seq":18446744073709551615,"tab_id":"wD:t1","terminal_id":"term_1","workspace_id":"wD","tokens":{"ticket":"TKB-24"}}}}` + "\n"))
	})
	a, err := c.AgentGet(context.Background(), "wD:p1")
	if err != nil {
		t.Fatal(err)
	}
	if a.StateChangeSeq != 18446744073709551615 || !a.Focused || a.ForegroundCwd != "/src/wt" || a.Agent == nil || *a.Agent != "claude" {
		t.Fatalf("agent = %+v", a)
	}
}

func TestAgentGetErrors(t *testing.T) {
	c := pipeClient(t, func(map[string]any) string {
		return `{"id":"","error":{"code":"agent_not_found","message":"agent target wD:p9 not found"}}`
	})
	var apiErr *APIError
	if _, err := c.AgentGet(context.Background(), "wD:p9"); !errors.As(err, &apiErr) || apiErr.Code != "agent_not_found" {
		t.Fatalf("want agent_not_found, got %v", err)
	}
	c = pipeClient(t, func(map[string]any) string { return `{"id":"x","result":{"type":"agent_info"}}` })
	if _, err := c.AgentGet(context.Background(), "wD:p1"); !errors.Is(err, ErrNoAgent) {
		t.Fatalf("want ErrNoAgent, got %v", err)
	}
}

func TestShowNotificationRequestShape(t *testing.T) {
	var method string
	var params map[string]any
	c := pipeClient(t, func(req map[string]any) string {
		method, _ = req["method"].(string)
		params, _ = req["params"].(map[string]any)
		return `{"id":"x","result":{"type":"notification_show","shown":true,"reason":"shown"}}`
	})
	shown, reason, err := c.ShowNotification(context.Background(), Notification{Title: "TKB-24 needs you", Body: "b", Sound: SoundRequest})
	if err != nil || !shown || reason != ReasonShown {
		t.Fatalf("shown=%v reason=%q err=%v", shown, reason, err)
	}
	if method != "notification.show" || params["title"] != "TKB-24 needs you" || params["body"] != "b" || params["sound"] != "request" || len(params) != 3 {
		t.Fatalf("sent %s %v", method, params)
	}

	// No body is sent as absent, not as "".
	if _, _, err := c.ShowNotification(context.Background(), Notification{Title: "t", Sound: SoundDone}); err != nil {
		t.Fatal(err)
	}
	if _, has := params["body"]; has || params["sound"] != "done" {
		t.Fatalf("sent %v", params)
	}
}

func TestShowNotificationDecodesReason(t *testing.T) {
	c := realSocket(t, func(conn net.Conn) {
		readReq(t, conn)
		conn.Write([]byte(`{"id":"x","result":{"type":"notification_show","shown":false,"reason":"rate_limited"}}` + "\n"))
	})
	shown, reason, err := c.ShowNotification(context.Background(), Notification{Title: "t"})
	if err != nil || shown || reason != ReasonRateLimited {
		t.Fatalf("shown=%v reason=%q err=%v", shown, reason, err)
	}
}
