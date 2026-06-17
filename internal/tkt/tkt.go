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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
type Runner func(bin string, args, env []string) (stdout, stderr []byte, code int, runErr error)

// Tkt wraps the tkt CLI.
type Tkt struct {
	Config string // explicit config path, or "" to let tkt auto-discover
	Binary string
	run    Runner
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

func defaultRunner(bin string, args, env []string) ([]byte, []byte, int, error) {
	cmd := exec.Command(bin, args...)
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
	stdout, stderr, code, runErr := t.run(t.Binary, args, t.env())
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
