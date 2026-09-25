// Package tkt is the only place in tktban that knows tkt exists.
//
// A thin wrapper around the `tkt` CLI: every method shells out to a verb and
// parses its --json output. tktban never imports tkt internals and never reads
// a backend's storage directly — this module is the entire coupling surface,
// the verb contract and nothing more. Point tkt at any backend and tktban
// follows.
//
// Binary resolution: TKT_BIN env var, else `tkt` on PATH.
// Config: an optional explicit path passed via the TKT_CONFIG env var (tkt's
// global --config placed before the verb is clobbered by an argparse quirk, so
// the env var is used uniformly for every verb).
package tkt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/olddognewflex/tktban/internal/model"
)

// exitLabels maps tkt's typed exit codes (core/errors.py) to human labels.
var exitLabels = map[int]string{
	2:  "config error",
	3:  "provider error",
	4:  "not found",
	64: "usage error",
}

// Error is a failed tkt invocation (nonzero exit, missing binary, or bad JSON).
type Error struct {
	Message  string
	ExitCode int // 0 if not applicable (e.g. missing binary, bad JSON)
	Stderr   string
}

func (e *Error) Error() string { return e.Message }

// Runner executes a command and returns its captured output. code is the
// process exit code (0 on success); runErr is non-nil only when the process
// could not be started at all (e.g. binary not found). It is injectable so
// tests can stand in for a real subprocess.
//
// ctx bounds the subprocess itself: cancelling it kills the process rather
// than only abandoning the wait, which is what lets a caller put a real
// deadline on a tkt read (see WithContext).
type Runner func(ctx context.Context, bin string, args, env []string) (stdout, stderr []byte, code int, runErr error)

// Tkt wraps the tkt CLI.
type Tkt struct {
	Config string // explicit config path, or "" to let tkt auto-discover
	Binary string
	run    Runner
	ctx    context.Context // nil means context.Background()
}

// New builds a Tkt. binary defaults to $TKT_BIN, else "tkt".
func New(config, binary string) *Tkt {
	if binary == "" {
		binary = os.Getenv("TKT_BIN")
	}
	if binary == "" {
		binary = "tkt"
	}
	return &Tkt{Config: config, Binary: binary, run: defaultRunner}
}

// WithRunner overrides the command runner (used in tests).
func (t *Tkt) WithRunner(r Runner) *Tkt {
	t.run = r
	return t
}

// WithContext returns a copy of t whose subprocesses are bounded by ctx.
//
// A copy, not a mutation: the board holds one long-lived *Tkt for every read
// it makes, and one caller putting a two-second budget on a config read must
// not put it on the board's refresh as well. The receiver is untouched.
func (t *Tkt) WithContext(ctx context.Context) *Tkt {
	c := *t
	c.ctx = ctx
	return &c
}

// context is the deadline this Tkt's subprocesses run under.
func (t *Tkt) context() context.Context {
	if t.ctx == nil {
		return context.Background()
	}
	return t.ctx
}

func defaultRunner(ctx context.Context, bin string, args, env []string) ([]byte, []byte, int, error) {
	// CommandContext, not Command: a wedged tkt must die with its deadline,
	// not outlive the board that asked it a question.
	cmd := exec.CommandContext(ctx, bin, args...)
	if env != nil {
		cmd.Env = env
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return out.Bytes(), errb.Bytes(), ee.ExitCode(), nil
		}
		return out.Bytes(), errb.Bytes(), -1, err // could not start
	}
	return out.Bytes(), errb.Bytes(), 0, nil
}

func (t *Tkt) env() []string {
	// Pass config via TKT_CONFIG rather than --config (an argparse parent-parser
	// quirk clobbers --config before the verb). nil means "inherit the parent
	// environment unchanged".
	if t.Config == "" {
		return nil
	}
	return append(os.Environ(), "TKT_CONFIG="+t.Config)
}

// run shells out to a verb. With asJSON, the stdout is returned raw for the
// caller to unmarshal; otherwise the trimmed stdout string is returned.
func (t *Tkt) runArgs(args []string) ([]byte, error) {
	ctx := t.context()
	stdout, stderr, code, runErr := t.run(ctx, t.Binary, args, t.env())
	// A killed process reports an exit code of its own; say what actually
	// happened rather than "exit -1".
	if err := ctx.Err(); err != nil {
		return nil, &Error{Message: fmt.Sprintf(
			"tkt %s did not finish in time (%v)", strings.Join(args, " "), err)}
	}
	if runErr != nil {
		return nil, &Error{Message: fmt.Sprintf(
			"tkt binary not found: %q. Put tkt on PATH or set TKT_BIN.", t.Binary)}
	}
	if code != 0 {
		label, ok := exitLabels[code]
		if !ok {
			label = fmt.Sprintf("exit %d", code)
		}
		se := strings.TrimSpace(string(stderr))
		detail := se
		if detail == "" {
			detail = "(no stderr)"
		}
		return nil, &Error{
			Message:  fmt.Sprintf("tkt %s failed (%s): %s", strings.Join(args, " "), label, detail),
			ExitCode: code,
			Stderr:   se,
		}
	}
	return stdout, nil
}

func (t *Tkt) runJSON(args []string, dst any) error {
	stdout, err := t.runArgs(args)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(stdout, dst); err != nil {
		return &Error{Message: fmt.Sprintf("tkt %s returned invalid JSON: %v", strings.Join(args, " "), err)}
	}
	return nil
}

// ---- read verbs ----

// Roles returns the ordered role→lane map (column order) from
// `tkt cfg board.roles --json`. Order is preserved from the JSON object.
func (t *Tkt) Roles() ([]model.RolePair, error) {
	stdout, err := t.runArgs([]string{"cfg", "board.roles", "--json"})
	if err != nil {
		return nil, err
	}
	pairs, derr := decodeOrderedStringObject(stdout)
	if derr != nil {
		return nil, &Error{Message: fmt.Sprintf("tkt cfg board.roles --json returned invalid JSON: %v", derr)}
	}
	return pairs, nil
}

// ApplyTemplate returns the create-document markdown template
// (`tkt apply --template`) used to pre-fill a new ticket in $EDITOR.
func (t *Tkt) ApplyTemplate() (string, error) {
	out, err := t.runArgs([]string{"apply", "--template"})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Apply ingests a full ticket markdown file via `tkt apply`. isNew creates a new
// ticket (key ignored); otherwise it updates key. Returns the resulting ticket
// key (the backend assigns it on create).
func (t *Tkt) Apply(key string, isNew bool, file string) (string, error) {
	args := []string{"apply"}
	if isNew {
		args = append(args, "--new")
	} else {
		args = append(args, key)
	}
	args = append(args, "--file", file, "--json")
	var out model.Ticket
	if err := t.runJSON(args, &out); err != nil {
		return "", err
	}
	if k, _ := out["key"].(string); k != "" {
		return k, nil
	}
	return key, nil
}

// Priorities returns the configured priority names from `tkt cfg priorities
// --json`, in configured order. Empty on error so the create/edit forms degrade
// to a blank (backend-default) selection rather than failing to open.
func (t *Tkt) Priorities() []string {
	var out []string
	if err := t.runJSON([]string{"cfg", "priorities", "--json"}, &out); err != nil {
		return nil
	}
	return out
}

// BoardHiddenRoles returns the optional `[ui.board] hidden_roles` config
// default — roles hidden on a board's first run. It is best-effort: a missing
// key or any read error yields nil, since this is only a default and must never
// block startup.
func (t *Tkt) BoardHiddenRoles() []string {
	var out []string
	if err := t.runJSON([]string{"cfg", "ui.board.hidden_roles", "--json"}, &out); err != nil {
		return nil
	}
	return out
}

// BoardOwnership returns the `[board] ownership` map from
// `tkt cfg board.ownership --json`: a transition ("todo->in_progress") to who
// owns it ("agent" or "human").
//
// Best-effort, like BoardHiddenRoles: a missing key or any read error yields
// nil. A board with no ownership config simply has no agent-owned transition
// to dispatch into, and the caller says so.
func (t *Tkt) BoardOwnership() map[string]string {
	var out map[string]string
	if err := t.runJSON([]string{"cfg", "board.ownership", "--json"}, &out); err != nil {
		return nil
	}
	return out
}

// ownerAgent is the ownership value that means an agent drives the transition.
const ownerAgent = "agent"

// AgentTarget is the role an agent-owned transition moves fromRole to: the
// lane a ticket lands in when an agent picks it up. ok is false when no
// agent-owned transition leaves fromRole, which is the board saying this
// column is not something to hand to an agent.
//
// order is the board's own role order (from board.roles). It does two jobs.
//
// It decides "first": ownership is a map, so candidates are ranked by their
// target's position on the board, and the answer is the same on every run and
// reads as "the next lane" rather than "whichever the runtime happened to
// hash first".
//
// And it decides acceptability. A target the board does not list is dropped,
// not merely ranked last: it is a role this board cannot show, so moving a
// ticket into it would take the card off the board — a config typo
// ("todo->in_progres") must refuse rather than transition somewhere nobody
// can see. An empty order (a board that has not loaded its roles yet) has
// nothing to check against, so it falls back to ranking by name alone.
//
// No role name is hard-coded; a board that renames its lanes keeps working.
func AgentTarget(ownership map[string]string, fromRole string, order []string) (string, bool) {
	var targets []string
	for transition, owner := range ownership {
		if owner != ownerAgent {
			continue
		}
		from, to, ok := strings.Cut(transition, "->")
		if !ok || strings.TrimSpace(from) != fromRole {
			continue
		}
		to = strings.TrimSpace(to)
		if to == "" {
			continue
		}
		if len(order) > 0 && !slices.Contains(order, to) {
			continue // a lane this board does not have
		}
		targets = append(targets, to)
	}
	if len(targets) == 0 {
		return "", false
	}
	rank := func(role string) int {
		if i := slices.Index(order, role); i >= 0 {
			return i
		}
		return len(order)
	}
	slices.SortFunc(targets, func(a, b string) int {
		if d := rank(a) - rank(b); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	return targets[0], true
}

// VCSConfig is the `[vcs]` block from `tkt cfg vcs --json` — the branch
// convention a dispatch has to follow so the board can find the agent again.
type VCSConfig struct {
	Provider      string `json:"provider"`
	Repo          string `json:"repo"`
	DefaultBranch string `json:"default_branch"`
	BranchFmt     string `json:"branch_fmt"`
	HotfixFmt     string `json:"hotfix_fmt"`
}

// VCS reads the [vcs] config. Best-effort: a zero VCSConfig on any failure,
// which the caller reads as "no branch convention", and refuses to dispatch.
//
// The format is returned unrendered on purpose. `tkt cfg vcs.branch_fmt
// --ticket X` renders it with an empty slug, which yields a branch ending in a
// bare separator, so the caller has to own slugification anyway — and then it
// may as well own the whole substitution (herdr.RenderBranch).
func (t *Tkt) VCS() VCSConfig {
	var out VCSConfig
	if err := t.runJSON([]string{"cfg", "vcs", "--json"}, &out); err != nil {
		return VCSConfig{}
	}
	return out
}

// ListAll returns every ticket on the board. Requires a [queries].all query.
func (t *Tkt) ListAll() ([]model.Ticket, error) {
	var out []model.Ticket
	if err := t.runJSON([]string{"list", "--query", "all", "--json"}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// View returns the full ticket dict for key.
func (t *Tkt) View(key string) (model.Ticket, error) {
	var out model.Ticket
	if err := t.runJSON([]string{"view", key, "--json"}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// IssueTypes returns {"full_sdlc": [...], "deliverable": [...]} — hints the
// create form.
func (t *Tkt) IssueTypes() (map[string]any, error) {
	var out map[string]any
	if err := t.runJSON([]string{"cfg", "issue_types", "--json"}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// LaneTime is the read-only time-in-lane for role, a Worklog-shaped dict.
// Read-only, so it never records a worklog.
//
// Returns nil ONLY for the benign "this ticket has never been in that lane"
// case (no entry in the provider's history). Any other failure is returned as
// an error so the caller can surface it rather than silently blanking cards.
func (t *Tkt) LaneTime(key, role string) (map[string]any, error) {
	out, err := t.LaneTimeBatch([][2]string{{key, role}})
	if err != nil {
		return nil, err
	}
	return out[key], nil
}

// LaneTimeBatch is the batch read-only time-in-lane for all (key, role) pairs.
// Entries for tickets with no history in the requested lane map to nil; genuine
// errors are returned.
func (t *Tkt) LaneTimeBatch(items [][2]string) (map[string]map[string]any, error) {
	if len(items) == 0 {
		return map[string]map[string]any{}, nil
	}
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = it[0] + ":" + it[1]
	}
	pairs := strings.Join(parts, ",")

	var result []map[string]any
	err := t.runJSON([]string{"lane-time", "--keys", pairs, "--read-only", "--json"}, &result)
	if err != nil {
		if te, ok := errors.AsType[*Error](err); ok {
			blob := strings.ToLower(te.Stderr)
			if blob == "" {
				blob = strings.ToLower(te.Message)
			}
			for _, s := range []string{"no entry", "history", "changelog"} {
				if strings.Contains(blob, s) {
					out := make(map[string]map[string]any, len(items))
					for _, it := range items {
						out[it[0]] = nil
					}
					return out, nil
				}
			}
		}
		return nil, err
	}
	if len(result) != len(items) {
		return nil, &Error{Message: fmt.Sprintf(
			"LaneTimeBatch: tkt returned %d entries for %d inputs", len(result), len(items))}
	}
	out := make(map[string]map[string]any, len(items))
	for i, it := range items {
		entry := result[i]
		k := it[0]
		if v, ok := entry["key"].(string); ok && v != "" {
			k = v
		}
		out[k] = entry
	}
	return out, nil
}

// ---- write verbs (mutations go through tkt so history/worklog stay correct) ----

// Transition moves key to role's lane.
func (t *Tkt) Transition(key, role string) error {
	_, err := t.runArgs([]string{"transition", key, role})
	return err
}

// Comment adds body as a comment on key.
func (t *Tkt) Comment(key, body string) error {
	_, err := t.runArgs([]string{"comment", key, body})
	return err
}

// CreateOpts are the optional fields for Create. Empty strings are omitted.
type CreateOpts struct {
	Priority string
	Assignee string
	Body     string
}

// Create makes a ticket of issueType with summary and returns the created
// ticket dict.
func (t *Tkt) Create(issueType, summary string, opts CreateOpts) (model.Ticket, error) {
	args := []string{"create", "--type", issueType, "--summary", summary}
	if opts.Priority != "" {
		args = append(args, "--priority", opts.Priority)
	}
	if opts.Assignee != "" {
		args = append(args, "--assignee", opts.Assignee)
	}
	if opts.Body != "" {
		args = append(args, "--body", opts.Body)
	}
	args = append(args, "--json")
	var out model.Ticket
	if err := t.runJSON(args, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// EditOpts are the editable fields. A nil pointer means "leave unchanged"; a
// pointer to "" is a real value (e.g. clear the assignee).
type EditOpts struct {
	Summary      *string
	Body         *string
	Priority     *string
	Assignee     *string
	AddLabels    []string
	RemoveLabels []string
	// Dates: nil = leave unchanged; a pointer to "" clears the date; otherwise
	// set it ("YYYY-MM-DD").
	Due       *string
	Scheduled *string
	Completed *string
}

// Edit edits content/fields via `tkt edit`. Only set fields are sent.
func (t *Tkt) Edit(key string, opts EditOpts) (model.Ticket, error) {
	args := []string{"edit", key}
	if opts.Summary != nil {
		args = append(args, "--summary", *opts.Summary)
	}
	if opts.Body != nil {
		args = append(args, "--body", *opts.Body)
	}
	if opts.Priority != nil {
		args = append(args, "--priority", *opts.Priority)
	}
	if opts.Assignee != nil {
		args = append(args, "--assignee", *opts.Assignee)
	}
	for _, l := range opts.AddLabels {
		args = append(args, "--add-label", l)
	}
	for _, l := range opts.RemoveLabels {
		args = append(args, "--remove-label", l)
	}
	if opts.Due != nil {
		args = append(args, "--due", *opts.Due)
	}
	if opts.Scheduled != nil {
		args = append(args, "--scheduled", *opts.Scheduled)
	}
	if opts.Completed != nil {
		args = append(args, "--completed", *opts.Completed)
	}
	args = append(args, "--json")
	var out model.Ticket
	if err := t.runJSON(args, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- diagnostics ----

// Check is one doctor result: a named check, whether it passed, and detail.
type Check struct {
	Name   string
	OK     bool
	Detail string
}

// Doctor runs setup checks: binary on PATH, config readable, `all` query.
func (t *Tkt) Doctor() []Check {
	var checks []Check

	found := binaryFound(t.Binary)
	detail := t.Binary
	if !found {
		detail = fmt.Sprintf("%q not found; set TKT_BIN", t.Binary)
	}
	checks = append(checks, Check{"tkt binary", found, detail})
	if !found {
		return checks
	}

	roles, err := t.Roles()
	if err != nil {
		checks = append(checks, Check{"board.roles readable", false, err.Error()})
		return checks
	}
	rdetail := "no roles configured"
	if len(roles) > 0 {
		rdetail = fmt.Sprintf("%d roles", len(roles))
	}
	checks = append(checks, Check{"board.roles readable", len(roles) > 0, rdetail})

	if _, err := t.ListAll(); err != nil {
		checks = append(checks, Check{"'all' query present", false,
			"add to [queries]:  all = 'ORDER BY key ASC'"})
	} else {
		checks = append(checks, Check{"'all' query present", true, "tkt list --query all OK"})
	}
	return checks
}

func binaryFound(binary string) bool {
	if _, err := exec.LookPath(binary); err == nil {
		return true
	}
	if info, err := os.Stat(binary); err == nil && !info.IsDir() {
		return true
	}
	return false
}

// decodeOrderedStringObject decodes a JSON object of string→string preserving
// key order, returning the entries as RolePairs.
func decodeOrderedStringObject(data []byte) ([]model.RolePair, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected JSON object, got %v", tok)
	}
	var pairs []model.RolePair
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key, got %v", keyTok)
		}
		var lane string
		if err := dec.Decode(&lane); err != nil {
			return nil, err
		}
		pairs = append(pairs, model.RolePair{Role: key, Lane: lane})
	}
	if _, err := dec.Token(); err != nil { // consume '}'
		return nil, err
	}
	return pairs, nil
}
