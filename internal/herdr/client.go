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
	// StateChangeSeq is herdr's state-change counter for this pane as of
	// this reply. Two hook processes that re-read the same transition see
	// the same number, which is what the notify hook dedupes on. It is
	// stamped per pane and not persisted: after a herdr restart it starts
	// again low while the pane id may be the same, so it only orders events
	// over a short window (see seenWindow).
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

// ---- error codes ----
//
// herdr names every failure with a stable `code` in its error reply. These are
// the ones tktban branches on; they were read out of herdr's own binary and
// its protocol-22 schema (see docs/herdr-events.md, "Verified in TKB-25").
// Anything else falls through to the generic wording, so an unknown code is
// reported rather than mistaken for a known one.
const (
	CodePaneNotFound = "pane_not_found"

	// Worktree failures. not_git_worktree and linked_worktree_source are the
	// two a dispatch must refuse outright rather than retry: the first means
	// the directory is not a checkout at all, the second that the checkout is
	// itself a linked worktree, which herdr will not branch a worktree from.
	CodeNotGitWorktree              = "not_git_worktree"
	CodeLinkedWorktreeSource        = "linked_worktree_source"
	CodeWorktreeOperationInProgress = "worktree_operation_in_progress"
	CodeWorktreeCreateFailed        = "worktree_create_failed"
	CodeStaleWorktreeOperation      = "stale_worktree_operation"
	CodeAmbiguousWorktreeBranch     = "ambiguous_worktree_branch"
	CodeWorktreeNotFound            = "worktree_not_found"

	// Agent failures. agent_pane_busy is the expected one right after a
	// worktree is created: the pane exists but its shell is not at a prompt
	// yet, so the start is retried rather than failed.
	CodeAgentPaneBusy        = "agent_pane_busy"
	CodeAgentNameTaken       = "agent_name_taken"
	CodeAgentBlocked         = "agent_blocked"
	CodeInvalidAgentName     = "invalid_agent_name"
	CodeInvalidAgentArgument = "invalid_agent_argument"
)

// ErrorCode returns the herdr error code err carries, or "" when it is not a
// herdr error reply at all (a dial failure, a timeout, a bad decode).
func ErrorCode(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

// ---- worktrees ----

// WorktreeInfo is one git worktree as herdr reports it. A null branch (a
// detached or bare checkout) decodes to "", as does an absent
// open_workspace_id (the worktree is on disk but no herdr workspace has it
// open).
type WorktreeInfo struct {
	Path             string `json:"path"`
	Branch           string `json:"branch"`
	Label            string `json:"label"`
	OpenWorkspaceID  string `json:"open_workspace_id"`
	IsBare           bool   `json:"is_bare"`
	IsDetached       bool   `json:"is_detached"`
	IsLinkedWorktree bool   `json:"is_linked_worktree"`
	IsPrunable       bool   `json:"is_prunable"`
}

// WorktreeSource is the repository a worktree.list was answered about: the
// checkout herdr resolved from the request's cwd, and the repo it belongs to.
type WorktreeSource struct {
	RepoKey            string `json:"repo_key"`
	RepoName           string `json:"repo_name"`
	RepoRoot           string `json:"repo_root"`
	SourceCheckoutPath string `json:"source_checkout_path"`
	SourceWorkspaceID  string `json:"source_workspace_id"`
}

// WorktreeListParams asks about the repository containing cwd. trust_repository
// is deliberately omitted while it is false: it is a write (it records the
// repository as trusted), and listing must stay a read.
type WorktreeListParams struct {
	Cwd             string `json:"cwd,omitempty"`
	WorkspaceID     string `json:"workspace_id,omitempty"`
	TrustRepository bool   `json:"trust_repository,omitempty"`
}

// WorktreeListResult is herdr's worktree_list reply.
type WorktreeListResult struct {
	Source    WorktreeSource `json:"source"`
	Worktrees []WorktreeInfo `json:"worktrees"`
}

// WorktreeList lists the worktrees of the repository containing cwd. It is the
// one herdr call a dry-run dispatch makes: it answers "is this a checkout at
// all", "which repo is it", and "is this branch already checked out
// somewhere", without creating anything.
func (c *Client) WorktreeList(ctx context.Context, p WorktreeListParams) (WorktreeListResult, error) {
	var r WorktreeListResult
	err := c.Call(ctx, "worktree.list", p, &r)
	return r, err
}

// WorktreeCreateParams creates a worktree on a new branch. branch is
// effectively required — herdr answers "branch is required" without it — and
// path is deliberately left empty so the worktree lands under herdr's own
// [worktrees] directory (default ~/.herdr/worktrees), next to the ones the
// person makes by hand. focus is sent even when false, because false is a
// decision: the board dispatches without stealing the screen.
type WorktreeCreateParams struct {
	WorkspaceID     string `json:"workspace_id,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
	Branch          string `json:"branch,omitempty"`
	Base            string `json:"base,omitempty"`
	Path            string `json:"path,omitempty"`
	Label           string `json:"label,omitempty"`
	Focus           bool   `json:"focus"`
	TrustRepository bool   `json:"trust_repository,omitempty"`
}

// WorktreeOpenParams opens a worktree that already exists. It is
// WorktreeCreateParams minus base: there is no branch to cut.
type WorktreeOpenParams struct {
	WorkspaceID     string `json:"workspace_id,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
	Branch          string `json:"branch,omitempty"`
	Path            string `json:"path,omitempty"`
	Label           string `json:"label,omitempty"`
	Focus           bool   `json:"focus"`
	TrustRepository bool   `json:"trust_repository,omitempty"`
}

// WorkspaceInfo / TabInfo / PaneInfo are the subsets of herdr's own structs a
// worktree reply carries. RootPane is the pane a dispatch would start an agent
// in: herdr always opens it at a shell, never at a command of our choosing.
type WorkspaceInfo struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number"`
}

type TabInfo struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

type PaneInfo struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	TerminalID  string `json:"terminal_id"`
	Cwd         string `json:"cwd"`
	Focused     bool   `json:"focused"`
}

// WorktreeResult is the reply to worktree.create and worktree.open: the
// workspace, tab and root pane herdr made for the worktree, and the worktree
// itself. AlreadyOpen is set only by worktree.open.
type WorktreeResult struct {
	Workspace   WorkspaceInfo `json:"workspace"`
	Tab         TabInfo       `json:"tab"`
	RootPane    PaneInfo      `json:"root_pane"`
	Worktree    WorktreeInfo  `json:"worktree"`
	AlreadyOpen bool          `json:"already_open"`
}

// WorktreeCreate cuts a branch and opens a worktree for it.
//
// Nothing in tktban calls it yet: the D key is a dry run that shows what this
// call would be given and then makes no call at all. It exists now so the wire
// shape is pinned against herdr protocol 22 by a test rather than written from
// memory later.
func (c *Client) WorktreeCreate(ctx context.Context, p WorktreeCreateParams) (WorktreeResult, error) {
	var r WorktreeResult
	err := c.Call(ctx, "worktree.create", p, &r)
	return r, err
}

// WorktreeOpen opens the worktree of a branch that already has one. Same
// standing as WorktreeCreate: typed now, called later.
func (c *Client) WorktreeOpen(ctx context.Context, p WorktreeOpenParams) (WorktreeResult, error) {
	var r WorktreeResult
	err := c.Call(ctx, "worktree.open", p, &r)
	return r, err
}

// ---- agents ----

// AgentStartParams starts an agent in a pane that already exists.
//
// This is the reason a dispatch is two steps rather than one: no herdr
// creation method takes a command or an argv. worktree.create opens a pane at
// a shell, and the agent is started into that pane afterwards — which is also
// why it can answer agent_pane_busy while the shell is still coming up.
//
// TimeoutMS is herdr's own startup budget: greater than 3000 and at most
// 300000, omitted to take herdr's default.
type AgentStartParams struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	PaneID    string   `json:"pane_id"`
	Args      []string `json:"args,omitempty"`
	TimeoutMS int      `json:"timeout_ms,omitempty"`
}

// AgentStartResult is herdr's agent_started reply: the agent it registered and
// the argv it actually ran.
type AgentStartResult struct {
	Agent Agent    `json:"agent"`
	Argv  []string `json:"argv"`
}

// AgentStart registers an agent in an existing pane. Typed now, called later.
func (c *Client) AgentStart(ctx context.Context, p AgentStartParams) (AgentStartResult, error) {
	var r AgentStartResult
	err := c.Call(ctx, "agent.start", p, &r)
	return r, err
}

// AgentPromptParams sends text to a started agent. Target is a pane id or an
// agent name. herdr also accepts a `wait` object ({until, timeout_ms}); it is
// left off, because a board must not block on an agent reaching a status.
type AgentPromptParams struct {
	Target string `json:"target"`
	Text   string `json:"text"`
}

// AgentPrompt types a prompt into a started agent. Typed now, called later.
func (c *Client) AgentPrompt(ctx context.Context, p AgentPromptParams) error {
	return c.Call(ctx, "agent.prompt", p, nil)
}
