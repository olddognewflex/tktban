// QA Author — adversarial tests for the ticket pane-metadata token.
// Break-It dimensions covered: the exact wire shape of pane.report_metadata
// (over a real unix socket, not net.Pipe), clearing a token with JSON null,
// a herdr error reply, and the publish diff itself, against a fake herdr that
// remembers what it was told: a token herdr already holds (including one this
// process never published), a stale key cleared by a board that published
// nothing, a changed branch, a released agent, a pane that closes, a pane on
// several tickets, a refused set and a refused clear both retried without
// failing the poll, the opt-out, a context that ends mid-publish, and a jump
// overlapping a poll on another goroutine.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
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

// fakePane is one entry in a fake agent.list reply: a pane, the directory its
// agent runs in, and the ticket token herdr *already holds* for it — the state
// the publisher diffs against.
type fakePane struct {
	id, dir  string
	token    string // "" means herdr holds no ticket token for this pane
	released bool   // herdr still lists the pane, but its agent is gone
}

// fakeHerdr is a herdr that remembers what it was told: report_metadata
// updates the pane's token, so a second poll sees the first poll's writes
// exactly as the real one would. It records every call as "pane=value" (or
// "pane=clear").
type fakeHerdr struct {
	panes   []fakePane
	calls   []string
	fail    map[string]bool // panes whose report_metadata answers an error
	noApply bool            // accept writes but never remember them
	onCall  func()          // run on each report_metadata, before replying
	dials   atomic.Int64    // calls *attempted*, whether or not they got that far
}

// list renders the agent.list reply for the current panes.
func (f *fakeHerdr) list() string {
	var b strings.Builder
	b.WriteString(`{"id":"x","result":{"type":"agent_list","agents":[`)
	for i, p := range f.panes {
		if i > 0 {
			b.WriteString(",")
		}
		agent := `"claude"`
		if p.released {
			agent = "null"
		}
		tokens := "null"
		if p.token != "" {
			tokens = fmt.Sprintf(`{"ticket":%q}`, p.token)
		}
		fmt.Fprintf(&b, `{"agent":%s,"agent_status":"working","cwd":%q,"foreground_cwd":%q,`+
			`"pane_id":%q,"tab_id":"w1:t1","workspace_id":"w1","focused":false,"tokens":%s}`,
			agent, p.dir, p.dir, p.id, tokens)
	}
	b.WriteString(`]}}`)
	return b.String()
}

// apply is herdr accepting a metadata write.
func (f *fakeHerdr) apply(pane, value string) {
	for i := range f.panes {
		if f.panes[i].id == pane {
			f.panes[i].token = value
		}
	}
}

func (f *fakeHerdr) client(t *testing.T) *Client {
	t.Helper()
	c := pipeClient(t, func(req map[string]any) string {
		switch req["method"] {
		case "agent.list":
			return compact(t, f.list())
		case "pane.focus":
			return `{"id":"x","result":{"type":"ok"}}`
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
			if !f.noApply {
				s, _ := value.(string)
				f.apply(pane, s)
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
	c.dial = func(ctx context.Context) (net.Conn, error) { f.dials.Add(1); return inner(ctx) }
	return c
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

func poll(t *testing.T, s *SocketSource) map[string]Live {
	t.Helper()
	got, err := s.Poll(context.Background())
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	return got
}

func TestPublishTokensOnlyOnChange(t *testing.T) {
	f := &fakeHerdr{panes: []fakePane{{id: "p1", dir: "/a"}, {id: "p2", dir: "/b"}}}
	keys := map[string][]string{"/a": {"TKB-1"}, "/b": {"TKB-2"}}
	s := tokenSource(t, f, keys)

	poll(t, s)
	want := "p1=TKB-1 p2=TKB-2"
	if got := strings.Join(f.calls, " "); got != want {
		t.Fatalf("first poll published %q, want %q", got, want)
	}

	// herdr now holds what we would report: nothing more is written, ever.
	for range 3 {
		poll(t, s)
	}
	if len(f.calls) != 2 {
		t.Fatalf("a steady-state poll republished: %q", f.calls)
	}

	// A checkout inside p1 moves it to another ticket: one call, for p1 only.
	keys["/a"] = []string{"TKB-9"}
	poll(t, s)
	poll(t, s)
	if len(f.calls) != 3 || f.calls[2] != "p1=TKB-9" {
		t.Fatalf("branch change published %q", f.calls)
	}

	// p2's agent is released. herdr still lists the pane, so its now-wrong
	// key is cleared — exactly once.
	f.panes[1].released = true
	poll(t, s)
	poll(t, s)
	if len(f.calls) != 4 || f.calls[3] != "p2=clear" {
		t.Fatalf("released agent published %q, want a single p2=clear", f.calls)
	}

	// p2 closes altogether. It is gone from agent.list, its metadata went
	// with it, and there is no pane left to report to: no call at all.
	f.panes = f.panes[:1]
	poll(t, s)
	if len(f.calls) != 4 {
		t.Fatalf("a closed pane was reported to: %q", f.calls)
	}
}

// The diff is against herdr's own state, not this process's memory: a board
// that has published nothing yet still gets both cases right. Without this,
// a pane that changed branch while no board ran keeps its stale key forever.
func TestPublishTokensDiffsAgainstHerdrState(t *testing.T) {
	// (a) herdr already holds the right key: a fresh board writes nothing.
	f := &fakeHerdr{panes: []fakePane{{id: "p1", dir: "/a", token: "TKB-1"}}}
	s := tokenSource(t, f, map[string][]string{"/a": {"TKB-1"}})
	poll(t, s)
	if len(f.calls) != 0 {
		t.Fatalf("republished a token herdr already held: %q", f.calls)
	}
	if f.dials.Load() != 1 {
		t.Fatalf("%d calls for a steady-state poll, want just the agent.list", f.dials.Load())
	}

	// (b) herdr holds a key left by an earlier board, and the pane has since
	// moved to a branch that names no ticket: this board clears it, although
	// it never published anything itself.
	f2 := &fakeHerdr{panes: []fakePane{{id: "p1", dir: "/main", token: "TKB-1"}}}
	s2 := tokenSource(t, f2, map[string][]string{"/a": {"TKB-1"}})
	poll(t, s2)
	if len(f2.calls) != 1 || f2.calls[0] != "p1=clear" {
		t.Fatalf("stale key published %q, want a single p1=clear", f2.calls)
	}
	// And it stays cleared.
	poll(t, s2)
	if len(f2.calls) != 1 {
		t.Fatalf("clear was repeated: %q", f2.calls)
	}
}

// A pane whose branch names several tickets gets all of them, in a stable
// order, so an unchanged set never looks like a change.
func TestPublishTokensMultiKeyPaneSorted(t *testing.T) {
	f := &fakeHerdr{panes: []fakePane{{id: "p1", dir: "/revert"}}}
	keys := map[string][]string{"/revert": {"TKB-22", "REVERT-45"}}
	s := tokenSource(t, f, keys)
	poll(t, s)
	if len(f.calls) != 1 || f.calls[0] != "p1=REVERT-45,TKB-22" {
		t.Fatalf("published %q, want p1=REVERT-45,TKB-22", f.calls)
	}
	// The same keys the other way round are the same token: no republish.
	keys["/revert"] = []string{"REVERT-45", "TKB-22"}
	poll(t, s)
	if len(f.calls) != 1 {
		t.Fatalf("key order alone republished: %q", f.calls)
	}
}

// Publishing is a nicety; the badges are the job. A pane herdr refuses must
// not cost the poll its result — and must be retried, which falls out of
// diffing against herdr: it still does not hold what we want it to.
func TestPublishTokensFailureDoesNotFailPoll(t *testing.T) {
	f := &fakeHerdr{panes: []fakePane{{id: "p1", dir: "/a"}}, fail: map[string]bool{"p1": true}}
	s := tokenSource(t, f, map[string][]string{"/a": {"TKB-1"}})

	got := poll(t, s)
	if len(got) != 1 || got["TKB-1"].Status != StatusWorking || len(got["TKB-1"].Panes) != 1 {
		t.Fatalf("badges lost to the failed publish: %+v", got)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %q", f.calls)
	}

	// herdr comes back: the pane it never accepted is published again.
	f.fail = nil
	poll(t, s)
	if len(f.calls) != 2 || f.calls[1] != "p1=TKB-1" {
		t.Fatalf("a failed publish was not retried: %q", f.calls)
	}
}

// The same for a refused clear: herdr still holds the stale key, so the next
// poll tries again rather than leaving it there.
func TestPublishTokensClearFailureRetried(t *testing.T) {
	f := &fakeHerdr{
		panes: []fakePane{{id: "p1", dir: "/main", token: "TKB-1"}},
		fail:  map[string]bool{"p1": true},
	}
	s := tokenSource(t, f, map[string][]string{"/a": {"TKB-1"}})

	poll(t, s)
	if len(f.calls) != 1 || f.calls[0] != "p1=clear" {
		t.Fatalf("calls = %q, want p1=clear", f.calls)
	}
	f.fail = nil
	poll(t, s)
	if len(f.calls) != 2 || f.calls[1] != "p1=clear" {
		t.Fatalf("a failed clear was not retried: %q", f.calls)
	}
	// Accepted this time, so it stops.
	poll(t, s)
	if len(f.calls) != 2 {
		t.Fatalf("clear repeated after herdr accepted it: %q", f.calls)
	}
}

func TestPublishTokensDisabled(t *testing.T) {
	f := &fakeHerdr{panes: []fakePane{{id: "p1", dir: "/a"}}}
	s := tokenSource(t, f, map[string][]string{"/a": {"TKB-1"}})
	s.Tokens = false

	if got := poll(t, s); got["TKB-1"].Status != StatusWorking {
		t.Fatalf("poll = %+v", got)
	}
	if len(f.calls) != 0 {
		t.Fatalf("--no-herdr-tokens still wrote to herdr: %q", f.calls)
	}
}

func TestPublishTokensRespectsContext(t *testing.T) {
	agents := []Agent{{PaneID: "p1"}, {PaneID: "p2"}}
	byKey := map[string]Live{
		"TKB-1": {Panes: []PaneRef{{PaneID: "p1"}}},
		"TKB-2": {Panes: []PaneRef{{PaneID: "p2"}}},
	}

	// A context that is already done buys no calls at all.
	f := &fakeHerdr{panes: []fakePane{{id: "p1", dir: "/a"}, {id: "p2", dir: "/b"}}}
	s := tokenSource(t, f, nil)
	done, cancel := context.WithCancel(context.Background())
	cancel()
	s.publishTokens(done, agents, byKey)
	if f.dials.Load() != 0 || len(f.calls) != 0 {
		t.Fatalf("published under a dead context: %d dials, %q", f.dials.Load(), f.calls)
	}

	// A context that ends during the first call stops the second from
	// starting, rather than running the whole backlog past the deadline.
	f2 := &fakeHerdr{panes: []fakePane{{id: "p1", dir: "/a"}, {id: "p2", dir: "/b"}}}
	ctx, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	f2.onCall = cancel2
	s2 := tokenSource(t, f2, nil)
	s2.publishTokens(ctx, agents, byKey)
	if f2.dials.Load() != 1 || len(f2.calls) != 1 || f2.calls[0] != "p1=TKB-1" {
		t.Fatalf("%d dials, calls = %q; want only the one in flight when the context ended",
			f2.dials.Load(), f2.calls)
	}
}

// The board jumps to a pane on its own goroutine (ui.jumpCmd) while a poll may
// be in flight, so a SocketSource has to tolerate that — which it does by
// keeping no mutable state at all. Run under -race this is the test that says
// so: polls (publish phase included, since this herdr never accepts a write,
// so every poll reports again) and jumps run against one SocketSource from two
// goroutines released together, with nothing ordering them.
func TestPollAndFocusOverlap(t *testing.T) {
	f := &fakeHerdr{panes: []fakePane{{id: "p1", dir: "/a"}}, noApply: true}
	s := &SocketSource{
		Client:     f.client(t),
		Tokens:     true,
		KeysForDir: func(context.Context, string) []string { return []string{"TKB-1"} },
	}

	const rounds = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for range rounds {
			got, err := s.Poll(context.Background())
			if err != nil {
				t.Errorf("poll during jumps: %v", err)
				return
			}
			if got["TKB-1"].Status != StatusWorking {
				t.Errorf("poll during jumps = %+v", got)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range rounds {
			if err := s.FocusPane(context.Background(), "p1"); err != nil {
				t.Errorf("focus during polls: %v", err)
				return
			}
		}
	}()
	close(start)
	wg.Wait()
}
