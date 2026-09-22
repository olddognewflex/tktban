package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/olddognewflex/tktban/internal/settings"
)

// The notify hook: herdr runs `tktban herdr-hook` for every
// pane.agent_status_changed, as a separate short-lived process, and this is
// what it does. It toasts "<KEY> needs you" when an agent on a ticket branch
// goes blocked and "<KEY> finished" when it goes done, and nothing else.
//
// Everything here treats the event as a signal, not as state (see
// docs/herdr-events.md): the payload carries no sequence number and hooks can
// overlap, so the hook waits a moment, re-reads the pane with agent.get, and
// acts only if the pane still says what the event said. Overlapping hooks for
// one transition are collapsed by claiming herdr's state_change_seq in the
// plugin state dir before sending.

// StatusEventName is the hook event the notifier handles, as herdr passes it
// in HERDR_PLUGIN_EVENT.
const StatusEventName = "pane.agent_status_changed"

// statusEventKind is the same event in the JSON envelope, which uses
// underscores.
const statusEventKind = "pane_agent_status_changed"

// settleDelay is how long the hook waits before re-reading the pane. It
// mirrors herdr's own delay before its background-agent toasts and absorbs a
// status that flips straight back.
const settleDelay = time.Second

// rateLimitRetry is the wait before retrying a toast herdr turned away as
// rate_limited or busy: just over herdr's one global 1 s API rate limit.
const rateLimitRetry = 1100 * time.Millisecond

// maxShowAttempts is the first try plus two retries.
const maxShowAttempts = 3

// Limits herdr applies to notification.show after sanitising.
const (
	maxTitle = 80
	maxBody  = 240
)

// StatusEvent is the part of a pane.agent_status_changed payload the hook
// reads.
type StatusEvent struct {
	PaneID      string
	WorkspaceID string
	Status      Status
}

// ParseStatusEvent reads a hook's HERDR_PLUGIN_EVENT / HERDR_PLUGIN_EVENT_JSON.
// herdr 0.9.0 sends the underscore envelope
//
//	{"event":"pane_agent_status_changed","data":{"type":"pane_agent_status_changed",
//	 "pane_id":"wD:p1","workspace_id":"wD","agent_status":"blocked","agent":"claude"}}
//
// with display_agent, title and state_labels also allowed in data; extra
// fields are ignored. Anything else — another event, a payload missing
// pane_id or agent_status — is not a status event.
func ParseStatusEvent(eventName, eventJSON string) (StatusEvent, bool) {
	if eventName != StatusEventName {
		return StatusEvent{}, false
	}
	var env struct {
		Event string `json:"event"`
		Data  *struct {
			Type        string `json:"type"`
			PaneID      string `json:"pane_id"`
			WorkspaceID string `json:"workspace_id"`
			Status      Status `json:"agent_status"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(eventJSON), &env) != nil || env.Data == nil {
		return StatusEvent{}, false
	}
	if env.Event != statusEventKind && env.Event != StatusEventName {
		return StatusEvent{}, false
	}
	d := env.Data
	if d.Type != "" && d.Type != statusEventKind {
		return StatusEvent{}, false
	}
	if d.PaneID == "" || d.Status == "" {
		return StatusEvent{}, false
	}
	return StatusEvent{PaneID: d.PaneID, WorkspaceID: d.WorkspaceID, Status: d.Status}, true
}

// Toast is one notification the hook would show.
type Toast struct {
	Title string
	Body  string
	Sound string
}

// attention reports whether a status is one the hook toasts at all.
func attention(s Status) bool {
	return s == StatusBlocked || s == StatusDone
}

// Decide builds the toast for a pane on keys going to status, and false for a
// status that toasts nothing (idle, working, unknown) or no keys. agent is
// herdr's agent name for the pane, "" when it has none.
func Decide(keys []string, status Status, agent string) (Toast, bool) {
	if len(keys) == 0 {
		return Toast{}, false
	}
	who := agent
	if who == "" {
		who = "The agent"
	}
	label := strings.Join(keys, ",")
	var t Toast
	switch status {
	case StatusBlocked:
		// herdr cannot tell a permission prompt from a question.
		t = Toast{Title: label + " needs you", Body: who + " is waiting on a permission prompt or a question.", Sound: SoundRequest}
	case StatusDone:
		t = Toast{Title: label + " finished", Body: who + " finished its turn.", Sound: SoundDone}
	default:
		return Toast{}, false
	}
	t.Title = clip(t.Title, maxTitle)
	t.Body = clip(t.Body, maxBody)
	return t, true
}

// clip shortens s to at most n runes, marking the cut with an ellipsis.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// HookClient is the slice of the herdr socket the hook uses; *Client has it.
type HookClient interface {
	AgentGet(ctx context.Context, target string) (Agent, error)
	ShowNotification(ctx context.Context, n Notification) (bool, string, error)
}

// HookDeps is everything RunHook touches, injected so it tests without a
// herdr, a clock or a real wait.
type HookDeps struct {
	Event     string // HERDR_PLUGIN_EVENT
	EventJSON string // HERDR_PLUGIN_EVENT_JSON
	StateDir  string // HERDR_PLUGIN_STATE_DIR: settings and dedupe state
	Client    HookClient
	// Now and Sleep default to the wall clock; Sleep must return early with
	// ctx's error when ctx ends.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	// KeysForDir defaults to KeysForDir, FindConfig to FindConfig over os.Stat.
	KeysForDir func(ctx context.Context, dir string) []string
	FindConfig func(dir string) string
}

// SleepCtx waits d or until ctx ends, whichever is first.
func SleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (d *HookDeps) fill() {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = SleepCtx
	}
	if d.KeysForDir == nil {
		d.KeysForDir = KeysForDir
	}
	if d.FindConfig == nil {
		d.FindConfig = func(dir string) string { return FindConfig(dir, os.Stat) }
	}
}

// RunHook handles one status event and returns a one-line account of what it
// did, which the command prints: herdr keeps hook stdout, so `herdr plugin
// log list --plugin odnf.tktban` shows every decision. ctx bounds all of it.
//
// The order is chosen so the common case costs nothing: most events are
// working/idle, and those return before any file or socket is touched.
func RunHook(ctx context.Context, d HookDeps) string {
	d.fill()
	ev, ok := ParseStatusEvent(d.Event, d.EventJSON)
	if !ok {
		return "skip: not a status event"
	}
	if !attention(ev.Status) {
		return "skip: status " + string(ev.Status)
	}
	if !notifyOn(d.StateDir) {
		return "skip: notify off"
	}
	if err := d.Sleep(ctx, settleDelay); err != nil {
		return "skip: " + oneLine(err)
	}

	a, err := d.Client.AgentGet(ctx, ev.PaneID)
	if err != nil {
		return "skip: agent.get: " + oneLine(err)
	}
	if a.Status != ev.Status {
		return fmt.Sprintf("skip: status now %s, was %s", a.Status, ev.Status)
	}
	if a.Focused {
		return "skip: pane focused"
	}

	dir := a.ForegroundCwd
	if dir == "" {
		dir = a.Cwd
	}
	keys, linked, err := ticketFor(ctx, d, dir)
	if err != nil {
		return "skip: " + oneLine(err)
	}
	if len(keys) == 0 {
		return "skip: no ticket on this branch"
	}
	if !linked {
		return "skip: no .sdlc/config.toml for " + strings.Join(keys, ",")
	}
	agent := ""
	if a.Agent != nil {
		agent = *a.Agent
	}
	toast, ok := Decide(keys, ev.Status, agent)
	if !ok {
		return "skip: status " + string(ev.Status)
	}

	c, err := claimNotify(ctx, d.StateDir, ev.PaneID, a.StateChangeSeq, ev.Status, d.Now())
	switch {
	case err != nil:
		return "skip: state: " + oneLine(err)
	case c == claimSeen:
		return fmt.Sprintf("skip: seq %d already handled", a.StateChangeSeq)
	case c == claimFlap:
		return fmt.Sprintf("skip: %s toasted under %s ago", ev.Status, notifyCooldown)
	}

	label := strings.Join(keys, ",") + " " + string(ev.Status)
	n := Notification{Title: toast.Title, Body: toast.Body, Sound: toast.Sound}
	for attempt := 1; ; attempt++ {
		shown, reason, err := d.Client.ShowNotification(ctx, n)
		if err != nil {
			return "error: notification.show: " + oneLine(err)
		}
		if shown {
			return "shown " + label
		}
		retry := reason == ReasonRateLimited || reason == ReasonBusy
		if !retry {
			return fmt.Sprintf("not shown: %s (%s)", reason, label)
		}
		if attempt >= maxShowAttempts {
			return fmt.Sprintf("gave up: %s after %d tries (%s)", reason, attempt, label)
		}
		if err := d.Sleep(ctx, rateLimitRetry); err != nil {
			return fmt.Sprintf("gave up: %s, %s (%s)", reason, oneLine(err), label)
		}
	}
}

// notifyOn reads the notify setting from the plugin settings file — the same
// file the board keeps in the state dir, symlink refused as the board does.
// Missing or unreadable settings mean the default, on.
func notifyOn(stateDir string) bool {
	path := SafeSettingsPath(SettingsPath(Env{InHerdr: true, StateDir: stateDir}), os.Lstat)
	if path == "" {
		return true
	}
	v, ok := settings.Load(path)["notify"].(bool)
	return !ok || v
}

// ticketFor is the pane's ticket keys and whether a tkt config covers its
// directory. Both touch the filesystem, and a hung mount must not keep the
// hook running past ctx, so they run on their own goroutine and ctx wins.
func ticketFor(ctx context.Context, d HookDeps, dir string) ([]string, bool, error) {
	if dir == "" {
		return nil, false, nil
	}
	type result struct {
		keys   []string
		linked bool
	}
	done := make(chan result, 1)
	go func() {
		keys := d.KeysForDir(ctx, dir)
		linked := len(keys) > 0 && d.FindConfig(dir) != ""
		done <- result{keys, linked}
	}()
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case r := <-done:
		return r.keys, r.linked, nil
	}
}

// oneLine keeps an error on the single decision line.
func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}
