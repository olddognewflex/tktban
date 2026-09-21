// QA Author — adversarial tests for the ticket pane-metadata token.
// Break-It dimensions covered: the exact wire shape of pane.report_metadata
// (over a real unix socket, not net.Pipe), clearing a token with JSON null,
// a herdr error reply, and the publish diff itself: republishing nothing when
// nothing changed, a changed branch, a pane that vanishes, a pane on several
// tickets, a failed call not failing the poll (and being retried), the
// opt-out, and a context that ends mid-publish.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

// reportServer answers one pane.report_metadata over a real unix socket and
// hands the decoded request back on a channel (a channel, not a shared
// variable: the socket itself is no happens-before edge for the race
// detector).
func reportServer(t *testing.T, reply string) (*Client, <-chan map[string]any) {
	t.Helper()
	reqs := make(chan map[string]any, 1)
	c := realSocket(t, func(conn net.Conn) {
		reqs <- readReq(t, conn)
		conn.Write([]byte(reply + "\n"))
	})
	return c, reqs
}

func TestReportPaneTokensRequest(t *testing.T) {
	c, reqs := reportServer(t, `{"id":"x","result":{"type":"ok"}}`)
	value := "TKB-23"
	if err := c.ReportPaneTokens(context.Background(), "wC:p1", map[string]*string{TicketToken: &value}); err != nil {
		t.Fatalf("report: %v", err)
	}

	req := <-reqs
	if req["method"] != "pane.report_metadata" {
		t.Fatalf("method = %v, want pane.report_metadata", req["method"])
	}
	params, ok := req["params"].(map[string]any)
	if !ok {
		t.Fatalf("params = %v, want an object", req["params"])
	}
	if params["pane_id"] != "wC:p1" {
		t.Errorf("pane_id = %v", params["pane_id"])
	}
	if params["source"] != "odnf.tktban" {
		t.Errorf("source = %v, want odnf.tktban", params["source"])
	}
	tokens, ok := params["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("tokens = %v, want an object", params["tokens"])
	}
	if len(tokens) != 1 || tokens["ticket"] != "TKB-23" {
		t.Errorf("tokens = %v, want one ticket=TKB-23", tokens)
	}
	// herdr expires metadata with a ttl; the sidebar has to keep the key after
	// the board (usually a popup) is gone, so tktban must not send one. Nor
	// may it touch the fields the title-owning plugin writes.
	for _, k := range []string{"ttl_ms", "title", "display_agent", "state_labels", "seq"} {
		if _, ok := params[k]; ok {
			t.Errorf("params carries %q = %v; tokens are all tktban may report", k, params[k])
		}
	}
}

// A cleared token is a present key with a JSON null, not an absent key: an
// absent key would leave the stale ticket on the pane forever.
func TestReportPaneTokensClear(t *testing.T) {
	c, reqs := reportServer(t, `{"id":"x","result":{"type":"ok"}}`)
	if err := c.ReportPaneTokens(context.Background(), "wC:p1", map[string]*string{TicketToken: nil}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	req := <-reqs
	tokens, ok := req["params"].(map[string]any)["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("tokens missing from %v", req["params"])
	}
	v, present := tokens["ticket"]
	if !present {
		t.Fatal("clearing dropped the ticket key instead of sending null")
	}
	if v != nil {
		t.Fatalf("ticket = %v, want null", v)
	}
	// And the same again at the byte level, since a decoded map cannot tell a
	// null apart from a missing key on its own.
	line, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(line), `"ticket":null`) {
		t.Fatalf("wire form has no null ticket: %s", line)
	}
}

func TestReportPaneTokensAPIError(t *testing.T) {
	c := pipeClient(t, func(map[string]any) string {
		return `{"id":"x","error":{"code":"pane_not_found","message":"no pane zz"}}`
	})
	value := "TKB-23"
	err := c.ReportPaneTokens(context.Background(), "zz", map[string]*string{TicketToken: &value})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "pane_not_found" {
		t.Fatalf("want APIError pane_not_found, got %v", err)
	}
}

// fakeHerdr answers agent.list from agents and records every
// pane.report_metadata as "pane=value" (or "pane=clear").
type fakeHerdr struct {
	agents string          // the panes agent.list reports, as pane id -> dir
	calls  []string        // report_metadata calls, in order
	fail   map[string]bool // panes whose report_metadata answers an error
	onCall func()          // run on each report_metadata, before replying
	dials  int             // calls *attempted*, whether or not they got that far
}

func (f *fakeHerdr) client(t *testing.T) *Client {
	t.Helper()
	c := pipeClient(t, func(req map[string]any) string {
		switch req["method"] {
		case "agent.list":
			return compact(t, f.agents)
		case "pane.report_metadata":
			params, _ := req["params"].(map[string]any)
			if params["source"] != MetadataSource {
				t.Errorf("source = %v, want %q", params["source"], MetadataSource)
			}
			pane, _ := params["pane_id"].(string)
			tokens, _ := params["tokens"].(map[string]any)
			value, present := tokens[TicketToken]
			switch {
			case !present:
				t.Errorf("%s: no ticket token in %v", pane, tokens)
			case value == nil:
				f.calls = append(f.calls, pane+"=clear")
			default:
				f.calls = append(f.calls, fmt.Sprintf("%s=%v", pane, value))
			}
			if f.onCall != nil {
				f.onCall()
			}
			if f.fail[pane] {
				return `{"id":"x","error":{"code":"pane_not_found","message":"gone"}}`
			}
			return `{"id":"x","result":{"type":"ok"}}`
		default:
			t.Errorf("unexpected method %v", req["method"])
			return `{"id":"x","result":{}}`
		}
	})
	// Count attempts at the dial, which happens on the caller's goroutine
	// before anything is written: a call skipped for a dead context shows up
	// as a dial that never happened, where f.calls could not tell the
	// difference between "skipped" and "written but never read".
	inner := c.dial
	c.dial = func(ctx context.Context) (net.Conn, error) { f.dials++; return inner(ctx) }
	return c
}

// agentsOn renders an agent.list reply: one working claude pane per entry,
// given as "paneID=dir" pairs.
func agentsOn(panes ...string) string {
	var b strings.Builder
	b.WriteString(`{"id":"x","result":{"type":"agent_list","agents":[`)
	for i, p := range panes {
		pane, dir, _ := strings.Cut(p, "=")
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"agent":"claude","agent_status":"working","cwd":%q,"foreground_cwd":%q,`+
			`"pane_id":%q,"tab_id":"w1:t1","workspace_id":"w1","focused":false}`, dir, dir, pane)
	}
	b.WriteString(`]}}`)
	return b.String()
}

// tokenSource wires a SocketSource with tokens on to f, resolving dirs through
// the (mutable) keys map.
func tokenSource(t *testing.T, f *fakeHerdr, keys map[string][]string) *SocketSource {
	t.Helper()
	return &SocketSource{
		Client:     f.client(t),
		Tokens:     true,
		KeysForDir: func(_ context.Context, dir string) []string { return keys[dir] },
	}
}

func TestPublishTokensOnlyOnChange(t *testing.T) {
	f := &fakeHerdr{agents: agentsOn("p1=/a", "p2=/b")}
	keys := map[string][]string{"/a": {"TKB-1"}, "/b": {"TKB-2"}}
	s := tokenSource(t, f, keys)

	if _, err := s.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"p1=TKB-1", "p2=TKB-2"}
	if got := strings.Join(f.calls, " "); got != strings.Join(want, " ") {
		t.Fatalf("first poll published %q, want %q", got, want)
	}

	// Nothing changed: herdr must not be written to again.
	if _, err := s.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("an unchanged poll republished: %q", f.calls)
	}

	// A checkout inside p1 moves it to another ticket: one call, for p1 only.
	keys["/a"] = []string{"TKB-9"}
	if _, err := s.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 || f.calls[2] != "p1=TKB-9" {
		t.Fatalf("branch change published %q", f.calls)
	}

	// p2 closes: its token is cleared exactly once, and never again.
	f.agents = agentsOn("p1=/a")
	for range 2 {
		if _, err := s.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.calls) != 4 || f.calls[3] != "p2=clear" {
		t.Fatalf("pane drop-out published %q, want a single p2=clear", f.calls)
	}
}

// A pane whose branch names several tickets gets all of them, in a stable
// order, so an unchanged set never looks like a change.
func TestPublishTokensMultiKeyPaneSorted(t *testing.T) {
	f := &fakeHerdr{agents: agentsOn("p1=/revert")}
	keys := map[string][]string{"/revert": {"TKB-22", "REVERT-45"}}
	s := tokenSource(t, f, keys)
	if _, err := s.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0] != "p1=REVERT-45,TKB-22" {
		t.Fatalf("published %q, want p1=REVERT-45,TKB-22", f.calls)
	}
	// The same keys the other way round are the same token: no republish.
	keys["/revert"] = []string{"REVERT-45", "TKB-22"}
	if _, err := s.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("key order alone republished: %q", f.calls)
	}
}

// Publishing is a nicety; the badges are the job. A pane herdr refuses must
// not cost the poll its result — and must be retried, not assumed published.
func TestPublishTokensFailureDoesNotFailPoll(t *testing.T) {
	f := &fakeHerdr{agents: agentsOn("p1=/a"), fail: map[string]bool{"p1": true}}
	s := tokenSource(t, f, map[string][]string{"/a": {"TKB-1"}})

	got, err := s.Poll(context.Background())
	if err != nil {
		t.Fatalf("a failed report_metadata failed the poll: %v", err)
	}
	if len(got) != 1 || got["TKB-1"].Status != StatusWorking || len(got["TKB-1"].Panes) != 1 {
		t.Fatalf("badges lost to the failed publish: %+v", got)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %q", f.calls)
	}

	// herdr comes back: the pane it never accepted is published again.
	f.fail = nil
	if _, err := s.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || f.calls[1] != "p1=TKB-1" {
		t.Fatalf("a failed publish was not retried: %q", f.calls)
	}
}

func TestPublishTokensDisabled(t *testing.T) {
	f := &fakeHerdr{agents: agentsOn("p1=/a")}
	s := tokenSource(t, f, map[string][]string{"/a": {"TKB-1"}})
	s.Tokens = false

	got, err := s.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["TKB-1"].Status != StatusWorking {
		t.Fatalf("poll = %+v", got)
	}
	if len(f.calls) != 0 {
		t.Fatalf("--no-herdr-tokens still wrote to herdr: %q", f.calls)
	}
}

func TestPublishTokensRespectsContext(t *testing.T) {
	// A context that is already done buys no calls at all.
	f := &fakeHerdr{agents: agentsOn("p1=/a", "p2=/b")}
	keys := map[string][]string{"/a": {"TKB-1"}, "/b": {"TKB-2"}}
	s := tokenSource(t, f, keys)
	done, cancel := context.WithCancel(context.Background())
	cancel()
	s.publishTokens(done, map[string]Live{
		"TKB-1": {Panes: []PaneRef{{PaneID: "p1"}}},
		"TKB-2": {Panes: []PaneRef{{PaneID: "p2"}}},
	})
	if f.dials != 0 || len(f.calls) != 0 {
		t.Fatalf("published under a dead context: %d dials, %q", f.dials, f.calls)
	}

	// A context that ends during the first call stops the second from
	// starting, rather than running the whole backlog past the deadline.
	f2 := &fakeHerdr{agents: agentsOn("p1=/a", "p2=/b")}
	ctx, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	f2.onCall = cancel2
	s2 := tokenSource(t, f2, keys)
	s2.publishTokens(ctx, map[string]Live{
		"TKB-1": {Panes: []PaneRef{{PaneID: "p1"}}},
		"TKB-2": {Panes: []PaneRef{{PaneID: "p2"}}},
	})
	if f2.dials != 1 || len(f2.calls) != 1 || f2.calls[0] != "p1=TKB-1" {
		t.Fatalf("%d dials, calls = %q; want only the one in flight when the context ended", f2.dials, f2.calls)
	}
}
