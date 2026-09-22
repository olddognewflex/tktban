package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync/atomic"
)

// SupportedProtocol is the herdr socket protocol tktban is built against
// (herdr 0.9.0). Live status turns itself off on any other version rather than
// guess at a changed schema.
const SupportedProtocol = 22

// maxReply caps one reply line. agent.list for a dozen panes is a few KB, so
// this only guards against a runaway peer.
const maxReply = 4 << 20

// APIError is an error reply from herdr: {"error":{"code":...,"message":...}}.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return "herdr: " + e.Code + ": " + e.Message
}

// Client speaks herdr's socket API: one NDJSON request line per connection,
// one reply line back, then herdr closes. There is no connection to keep, so a
// Client is just an address and is safe for concurrent use.
type Client struct {
	SocketPath string
	// dial opens the connection; nil means a unix dial of SocketPath. Tests
	// swap in a net.Pipe.
	dial func(ctx context.Context) (net.Conn, error)
}

// NewClient returns a client for the herdr socket at path.
func NewClient(path string) *Client {
	return &Client{SocketPath: path}
}

var reqSeq atomic.Uint64

type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *APIError       `json:"error"`
}

// Call sends one request and decodes the reply's result into out (skipped when
// out is nil). A herdr error reply comes back as *APIError. The context bounds
// the whole exchange: its deadline is set on the connection, and cancelling it
// closes the connection.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	if params == nil {
		params = struct{}{} // herdr wants "params":{} even when there are none
	}
	line, err := json.Marshal(request{
		ID:     fmt.Sprintf("tktban:%d", reqSeq.Add(1)),
		Method: method,
		Params: params,
	})
	if err != nil {
		return err
	}

	conn, err := c.dialer()(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return err
		}
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	if _, err := conn.Write(append(line, '\n')); err != nil {
		return ctxErr(ctx, err)
	}
	reply, err := bufio.NewReader(io.LimitReader(conn, maxReply)).ReadBytes('\n')
	// herdr closes right after the reply, so a final line without its newline
	// is still a whole reply.
	if err != nil && !(errors.Is(err, io.EOF) && len(reply) > 0) {
		return ctxErr(ctx, err)
	}

	var resp response
	if err := json.Unmarshal(reply, &resp); err != nil {
		return fmt.Errorf("herdr: bad %s reply: %w", method, err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("herdr: bad %s result: %w", method, err)
	}
	return nil
}

// ctxErr prefers the context's own error when it ended the call, so a timeout
// reads as a timeout rather than as whatever the closed connection returned.
// The conn deadline and the context timer fire independently, so a conn
// timeout can land a moment before ctx.Err is set; it is the same deadline.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}

func (c *Client) dialer() func(context.Context) (net.Conn, error) {
	if c.dial != nil {
		return c.dial
	}
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", c.SocketPath)
	}
}

// Pong is herdr's reply to ping.
type Pong struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

// Ping asks herdr for its version and socket protocol.
func (c *Client) Ping(ctx context.Context) (Pong, error) {
	var p Pong
	err := c.Call(ctx, "ping", nil, &p)
	return p, err
}

// Agent is the subset of herdr's AgentInfo tktban reads. herdr lists only
// panes that run an agent, and a closed pane simply drops out of the list.
// Unknown fields are ignored; nullable strings decode to "".
type Agent struct {
	PaneID        string  `json:"pane_id"`
	WorkspaceID   string  `json:"workspace_id"`
	TabID         string  `json:"tab_id"`
	Agent         *string `json:"agent"` // nil once the agent is released
	Status        Status  `json:"agent_status"`
	Cwd           string  `json:"cwd"`
	ForegroundCwd string  `json:"foreground_cwd"` // where the agent actually runs
	Focused       bool    `json:"focused"`
	// StateChangeSeq is herdr's state-change counter as of this reply. It is
	// server-wide and only grows, so two hook processes that re-read the same
	// transition see the same number: that is what the notify hook dedupes on.
	StateChangeSeq uint64 `json:"state_change_seq"`
	// Tokens is the pane metadata herdr currently holds, merged across every
	// source that reported any. Reading it back is what lets tktban publish
	// without keeping state of its own: see SocketSource.publishTokens.
	Tokens map[string]string `json:"tokens"`
}

// AgentList returns every agent pane herdr knows about.
func (c *Client) AgentList(ctx context.Context) ([]Agent, error) {
	var r struct {
		Agents []Agent `json:"agents"`
	}
	if err := c.Call(ctx, "agent.list", nil, &r); err != nil {
		return nil, err
	}
	return r.Agents, nil
}

// FocusPane brings one pane to the front: its workspace, its tab and the pane
// itself, in one call. herdr's pane.focus takes {"pane_id": ...} — agent.focus
// is the one that takes a target — and answers an id it no longer knows with
// code pane_not_found, which callers branch on.
func (c *Client) FocusPane(ctx context.Context, paneID string) error {
	return c.Call(ctx, "pane.focus", struct {
		PaneID string `json:"pane_id"`
	}{PaneID: paneID}, nil)
}

// MetadataSource identifies tktban as the writer of the pane metadata it
// reports. herdr keeps each source's metadata apart, so this must not collide
// with another plugin's id; it is tktban's plugin id from herdr-plugin.toml.
const MetadataSource = "odnf.tktban"

// TicketToken is the pane-metadata token tktban publishes: the ticket key(s)
// the pane's branch names. herdr renders it as $ticket in a configured
// [ui.sidebar.agents] row.
const TicketToken = "ticket"

// ReportPaneTokens publishes display-only pane metadata tokens for one pane
// under MetadataSource. A nil value clears that token; herdr accepts up to 16
// tokens whose names match ^[A-Za-z0-9_-]{1,32}$.
//
// Only tokens are sent. title and display_agent are deliberately left alone:
// pane titles belong to whatever plugin owns them (herdr.auto-title renames
// tabs), and tokens are a separate namespace we do not have to fight over.
//
// No ttl_ms either. The token has to outlive this process — the board is
// usually a popup that closes seconds after publishing — so an expiry would
// blank the sidebar exactly when the board is gone. The flip side is that a
// key only changes while a board is running; see README.
func (c *Client) ReportPaneTokens(ctx context.Context, paneID string, tokens map[string]*string) error {
	return c.Call(ctx, "pane.report_metadata", struct {
		PaneID string             `json:"pane_id"`
		Source string             `json:"source"`
		Tokens map[string]*string `json:"tokens"`
	}{PaneID: paneID, Source: MetadataSource, Tokens: tokens}, nil)
}

// ErrNoAgent means agent.get answered without an agent in its reply.
var ErrNoAgent = errors.New("herdr: agent.get returned no agent")

// AgentGet re-reads one agent pane. herdr's agent.get takes {"target": ...}
// (a pane id or agent name), not pane_id, and replies
// {"type":"agent_info","agent":{...}}. A pane herdr no longer knows is an
// *APIError with code agent_not_found.
func (c *Client) AgentGet(ctx context.Context, target string) (Agent, error) {
	var r struct {
		Agent *Agent `json:"agent"`
	}
	if err := c.Call(ctx, "agent.get", struct {
		Target string `json:"target"`
	}{Target: target}, &r); err != nil {
		return Agent{}, err
	}
	if r.Agent == nil {
		return Agent{}, ErrNoAgent
	}
	return *r.Agent, nil
}

// Notification sounds herdr's notification.show accepts.
const (
	SoundNone    = "none"
	SoundDone    = "done"
	SoundRequest = "request"
)

// Reasons notification.show gives back with shown. Only ReasonShown means a
// toast appeared; rate_limited and busy are worth one more try, the others
// are not.
const (
	ReasonShown              = "shown"
	ReasonDisabled           = "disabled"             // ui.toast.delivery = "off"
	ReasonRateLimited        = "rate_limited"         // herdr's one global 1 s limit
	ReasonNoForegroundClient = "no_foreground_client" // no herdr client attached
	ReasonBusy               = "busy"
)

// Notification is one notification.show request. herdr sanitises and clips
// title to 80 characters and body to 240; an empty body is sent as absent.
// There is no pane target, click action or dedupe key in the API.
type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Sound string `json:"sound,omitempty"`
}

// ShowNotification asks herdr to toast. herdr delivers it however
// ui.toast.delivery says (in-app, terminal or system), and reports whether it
// did: {"type":"notification_show","shown":bool,"reason":...}.
func (c *Client) ShowNotification(ctx context.Context, n Notification) (bool, string, error) {
	var r struct {
		Shown  bool   `json:"shown"`
		Reason string `json:"reason"`
	}
	if err := c.Call(ctx, "notification.show", n, &r); err != nil {
		return false, "", err
	}
	return r.Shown, r.Reason, nil
}
