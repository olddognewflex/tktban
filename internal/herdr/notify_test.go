package herdr

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// hookHerdr is a HookClient that records every call. With forbid set any call
// fails the test: that is how a test proves the hook never reached the socket.
type hookHerdr struct {
	t      *testing.T
	forbid bool

	mu      sync.Mutex
	agent   Agent
	getErr  error
	reasons []string // reply to each show in turn, the last one repeating; none means shown
	gets    []string
	shows   []Notification
}

func (f *hookHerdr) AgentGet(_ context.Context, target string) (Agent, error) {
	if f.forbid {
		f.t.Errorf("agent.get(%q) called", target)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, target)
	return f.agent, f.getErr
}

func (f *hookHerdr) ShowNotification(_ context.Context, n Notification) (bool, string, error) {
	if f.forbid {
		f.t.Errorf("notification.show(%+v) called", n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shows = append(f.shows, n)
	reason := ReasonShown
	if len(f.reasons) > 0 {
		reason = f.reasons[min(len(f.shows)-1, len(f.reasons)-1)]
	}
	return reason == ReasonShown, reason, nil
}

func (f *hookHerdr) showCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.shows)
}

// hookFixture is one hook run against a fake herdr, a fake clock and a fake
// sleep, over a real temp repo and state dir.
type hookFixture struct {
	deps   HookDeps
	fake   *hookHerdr
	now    time.Time
	sleeps []time.Duration
}

const hookPane = "wD:p1"

func strp(s string) *string { return &s }

// statusPayload is the envelope herdr 0.9.0 puts in HERDR_PLUGIN_EVENT_JSON,
// as recorded by the TKB-20 spike (docs/herdr-events.md).
func statusPayload(pane string, s Status) string {
	return `{"event":"pane_agent_status_changed","data":{"type":"pane_agent_status_changed","pane_id":"` +
		pane + `","workspace_id":"wD","agent_status":"` + string(s) + `","agent":"claude"}}`
}

// ticketRepo makes a checkout on branch, with a tkt config when linked is set.
func ticketRepo(t *testing.T, base, name, branch string, linked bool) string {
	t.Helper()
	dir := filepath.Join(base, name)
	write(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/"+branch+"\n")
	if linked {
		write(t, filepath.Join(dir, ".sdlc", "config.toml"), "")
	}
	return dir
}

// newHook sets up an event for status on a linked TKB-24 pane that herdr,
// re-read, still reports as status.
func newHook(t *testing.T, status Status) *hookFixture {
	t.Helper()
	base := t.TempDir()
	repo := ticketRepo(t, base, "repo", "feature/tkb-24-herdr-notify-hook", true)
	f := &hookFixture{now: time.Unix(1_800_000_000, 0)}
	f.fake = &hookHerdr{t: t, agent: Agent{
		PaneID: hookPane, WorkspaceID: "wD", Agent: strp("claude"), Status: status,
		Cwd: base, ForegroundCwd: repo, StateChangeSeq: 722,
	}}
	f.deps = HookDeps{
		Event:     StatusEventName,
		EventJSON: statusPayload(hookPane, status),
		StateDir:  filepath.Join(base, "state"),
		Client:    f.fake,
		Now:       func() time.Time { return f.now },
		Sleep: func(ctx context.Context, d time.Duration) error {
			f.sleeps = append(f.sleeps, d)
			return ctx.Err()
		},
	}
	return f
}

func (f *hookFixture) run() string { return RunHook(context.Background(), f.deps) }

func TestParseStatusEventEnvelope(t *testing.T) {
	cases := []struct{ name, payload string }{
		// Verbatim from the TKB-20 spike's recorded hook payload.
		{"spike sample", `{"event":"pane_agent_status_changed","data":{"type":"pane_agent_status_changed","pane_id":"wD:p1","workspace_id":"wD","agent_status":"blocked","agent":"claude"}}`},
		// Every optional field the schema allows, plus fields it does not.
		{"extra fields", `{"event":"pane_agent_status_changed","seq":9,"data":{"type":"pane_agent_status_changed","pane_id":"wD:p1","workspace_id":"wD","agent_status":"blocked","agent":null,"display_agent":"Claude","title":"t","state_labels":{"k":"v"},"future":[1,2]}}`},
		// A per-pane socket subscription uses the dotted name and no data.type.
		{"dotted, no type", `{"event":"pane.agent_status_changed","data":{"pane_id":"wD:p1","workspace_id":"wD","agent_status":"blocked"}}`},
	}
	for _, c := range cases {
		ev, ok := ParseStatusEvent(StatusEventName, c.payload)
		if !ok {
			t.Errorf("%s: not parsed", c.name)
			continue
		}
		if ev != (StatusEvent{PaneID: "wD:p1", WorkspaceID: "wD", Status: StatusBlocked}) {
			t.Errorf("%s: parsed %+v", c.name, ev)
		}
	}
}

func TestParseStatusEventRejectsOtherEvents(t *testing.T) {
	good := statusPayload("wD:p1", StatusBlocked)
	cases := []struct{ name, event, payload string }{
		{"other hook event", "pane.created", good},
		{"no event name", "", good},
		{"other envelope event", StatusEventName, `{"event":"pane_created","data":{"type":"pane_agent_status_changed","pane_id":"p","agent_status":"blocked"}}`},
		{"data.type mismatch", StatusEventName, `{"event":"pane_agent_status_changed","data":{"type":"pane_created","pane_id":"p","agent_status":"blocked"}}`},
		{"flat, no envelope", StatusEventName, `{"pane_id":"p","agent_status":"blocked"}`},
		{"no pane", StatusEventName, `{"event":"pane_agent_status_changed","data":{"agent_status":"blocked"}}`},
		{"no status", StatusEventName, `{"event":"pane_agent_status_changed","data":{"pane_id":"p"}}`},
		{"null data", StatusEventName, `{"event":"pane_agent_status_changed","data":null}`},
		{"garbage", StatusEventName, `not json`},
		{"empty", StatusEventName, ``},
	}
	for _, c := range cases {
		if ev, ok := ParseStatusEvent(c.event, c.payload); ok {
			t.Errorf("%s: parsed %+v, want rejected", c.name, ev)
		}
	}
}

// Most status events are working or idle. Those must cost nothing: no socket,
// no sleep, no filesystem — not even the settings file or the state dir.
func TestRunHookSkipsNonAttentionStatusesWithoutSocket(t *testing.T) {
	for _, s := range []Status{StatusWorking, StatusIdle, StatusUnknown} {
		f := newHook(t, s)
		f.fake.forbid = true
		f.deps.Sleep = func(context.Context, time.Duration) error {
			t.Errorf("%s: slept", s)
			return nil
		}
		f.deps.KeysForDir = func(context.Context, string) []string {
			t.Errorf("%s: read a branch", s)
			return nil
		}
		f.deps.FindConfig = func(string) string {
			t.Errorf("%s: looked for a config", s)
			return ""
		}
		// A settings file that would silence the hook if it were read: the
		// output must still name the status, proving it returned first.
		write(t, filepath.Join(f.deps.StateDir, "settings.toml"), "notify = false\n")
		if got, want := f.run(), "skip: status "+string(s); got != want {
			t.Errorf("%s: got %q, want %q", s, got, want)
		}
		if _, err := os.Stat(filepath.Join(f.deps.StateDir, NotifyStateName)); !os.IsNotExist(err) {
			t.Errorf("%s: state file touched (err=%v)", s, err)
		}
	}
}

// AC1: a permission prompt in a linked pane is one toast with sound request.
func TestRunHookBlockedShowsRequestSound(t *testing.T) {
	f := newHook(t, StatusBlocked)
	if got := f.run(); got != "shown TKB-24 blocked" {
		t.Fatalf("decision = %q", got)
	}
	if len(f.fake.shows) != 1 {
		t.Fatalf("shows = %d, want 1", len(f.fake.shows))
	}
	n := f.fake.shows[0]
	if n.Title != "TKB-24 needs you" || n.Sound != SoundRequest || !strings.Contains(n.Body, "claude") {
		t.Fatalf("toast = %+v", n)
	}
	if len(f.fake.gets) != 1 || f.fake.gets[0] != hookPane {
		t.Fatalf("agent.get targets = %v", f.fake.gets)
	}
	if len(f.sleeps) != 1 || f.sleeps[0] != time.Second {
		t.Fatalf("sleeps = %v, want one 1s settle", f.sleeps)
	}
}

func TestRunHookDoneShowsDoneSound(t *testing.T) {
	f := newHook(t, StatusDone)
	if got := f.run(); got != "shown TKB-24 done" {
		t.Fatalf("decision = %q", got)
	}
	if len(f.fake.shows) != 1 || f.fake.shows[0].Title != "TKB-24 finished" || f.fake.shows[0].Sound != SoundDone {
		t.Fatalf("shows = %+v", f.fake.shows)
	}
}

func TestRunHookNotifyOffSilences(t *testing.T) {
	f := newHook(t, StatusBlocked)
	f.fake.forbid = true
	write(t, filepath.Join(f.deps.StateDir, "settings.toml"), "theme = \"textual-dark\"\nnotify = false\n")
	if got := f.run(); got != "skip: notify off" {
		t.Fatalf("decision = %q", got)
	}
	if len(f.sleeps) != 0 {
		t.Fatalf("slept %v with notify off", f.sleeps)
	}

	// notify = true, or a settings file without the key, toasts.
	for _, body := range []string{"notify = true\n", "theme = \"x\"\n"} {
		g := newHook(t, StatusBlocked)
		write(t, filepath.Join(g.deps.StateDir, "settings.toml"), body)
		if got := g.run(); got != "shown TKB-24 blocked" {
			t.Errorf("%q: decision = %q", body, got)
		}
	}
}

// The event is a signal: if the pane has moved on by the time it is re-read,
// the event is stale and nothing shows.
func TestRunHookStaleStatusSkips(t *testing.T) {
	f := newHook(t, StatusBlocked)
	f.fake.agent.Status = StatusWorking
	if got := f.run(); !strings.HasPrefix(got, "skip: status now working") {
		t.Fatalf("decision = %q", got)
	}
	if len(f.fake.shows) != 0 {
		t.Fatalf("shows = %+v", f.fake.shows)
	}
}

func TestRunHookFocusedPaneSkips(t *testing.T) {
	f := newHook(t, StatusBlocked)
	f.fake.agent.Focused = true
	if got := f.run(); got != "skip: pane focused" {
		t.Fatalf("decision = %q", got)
	}
	if len(f.fake.shows) != 0 {
		t.Fatalf("shows = %+v", f.fake.shows)
	}
}

func TestRunHookUnlinkedPaneSkips(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		name, dir, want string
	}{
		{"branch with no key", ticketRepo(t, base, "main", "main", true), "skip: no ticket on this branch"},
		{"key but no tkt config", ticketRepo(t, base, "bare", "feature/tkb-24-x", false), "skip: no .sdlc/config.toml for TKB-24"},
		{"no directory at all", "", "skip: no ticket on this branch"},
	}
	for _, c := range cases {
		f := newHook(t, StatusBlocked)
		f.fake.agent.ForegroundCwd, f.fake.agent.Cwd = c.dir, ""
		if got := f.run(); got != c.want {
			t.Errorf("%s: decision = %q, want %q", c.name, got, c.want)
		}
		if len(f.fake.shows) != 0 {
			t.Errorf("%s: shows = %+v", c.name, f.fake.shows)
		}
	}

	// cwd is the fallback when herdr has no foreground cwd.
	f := newHook(t, StatusBlocked)
	f.fake.agent.Cwd, f.fake.agent.ForegroundCwd = f.fake.agent.ForegroundCwd, ""
	if got := f.run(); got != "shown TKB-24 blocked" {
		t.Fatalf("cwd fallback: decision = %q", got)
	}
}

func TestRunHookSameSeqToastsOnce(t *testing.T) {
	f := newHook(t, StatusBlocked)
	if got := f.run(); got != "shown TKB-24 blocked" {
		t.Fatalf("first: %q", got)
	}
	// Past the cooldown but inside the seen window: only the seq dedupes.
	f.now = f.now.Add(40 * time.Second)
	if got := f.run(); got != "skip: seq 722 already handled" {
		t.Fatalf("second: %q", got)
	}
	if len(f.fake.shows) != 1 {
		t.Fatalf("shows = %d, want 1", len(f.fake.shows))
	}
}

// herdr can run several hooks for one transition at once. Over a real state
// dir, exactly one of them toasts.
func TestRunHookConcurrentHooksToastOnce(t *testing.T) {
	t.Parallel() // waits on its own deadline; nothing shared
	f := newHook(t, StatusBlocked)
	f.deps.Sleep = func(context.Context, time.Duration) error { return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const n = 8
	out := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { out[i] = RunHook(ctx, f.deps) })
	}
	wg.Wait()
	if got := f.fake.showCount(); got != 1 {
		t.Fatalf("shows = %d, want exactly 1; decisions %q", got, out)
	}
	// The rest either found a run already settling this pane and status,
	// or arrived after it and found the seq handled.
	shown := 0
	for _, o := range out {
		switch o {
		case "shown TKB-24 blocked":
			shown++
		case "skip: already settling blocked", "skip: seq 722 already handled":
		default:
			t.Errorf("unexpected decision %q", o)
		}
	}
	if shown != 1 {
		t.Fatalf("decisions %q", out)
	}
}

// A prompt that flaps blocked -> working -> blocked inside the cooldown toasts
// once, even though each blocked is a new seq.
func TestRunHookFlapWithinCooldownSilent(t *testing.T) {
	f := newHook(t, StatusBlocked)
	if got := f.run(); got != "shown TKB-24 blocked" {
		t.Fatalf("first: %q", got)
	}
	f.now = f.now.Add(10 * time.Second)
	f.fake.agent.StateChangeSeq = 730
	if got := f.run(); !strings.HasPrefix(got, "skip: blocked toasted under 30s ago") {
		t.Fatalf("flap: %q", got)
	}
	// The flap's seq was recorded, so a straggler hook for it stays quiet
	// even once the cooldown is over.
	f.now = f.now.Add(25 * time.Second)
	if got := f.run(); got != "skip: seq 730 already handled" {
		t.Fatalf("straggler: %q", got)
	}
	if len(f.fake.shows) != 1 {
		t.Fatalf("shows = %d, want 1", len(f.fake.shows))
	}
}

func TestRunHookNewPromptAfterCooldownToasts(t *testing.T) {
	f := newHook(t, StatusBlocked)
	if got := f.run(); got != "shown TKB-24 blocked" {
		t.Fatalf("first: %q", got)
	}
	f.now = f.now.Add(31 * time.Second)
	f.fake.agent.StateChangeSeq = 740
	if got := f.run(); got != "shown TKB-24 blocked" {
		t.Fatalf("after cooldown: %q", got)
	}
	// A different status is not a flap: done right after blocked toasts.
	f.now = f.now.Add(time.Second)
	f.fake.agent.StateChangeSeq = 741
	f.fake.agent.Status = StatusDone
	f.deps.EventJSON = statusPayload(hookPane, StatusDone)
	if got := f.run(); got != "shown TKB-24 done" {
		t.Fatalf("done after blocked: %q", got)
	}
	if len(f.fake.shows) != 3 {
		t.Fatalf("shows = %d, want 3", len(f.fake.shows))
	}
}

func TestRunHookRateLimitedRetriesThenGivesUp(t *testing.T) {
	f := newHook(t, StatusBlocked)
	f.fake.reasons = []string{ReasonRateLimited}
	if got := f.run(); !strings.HasPrefix(got, "gave up: rate_limited after 3 tries") {
		t.Fatalf("decision = %q", got)
	}
	if len(f.fake.shows) != 3 {
		t.Fatalf("shows = %d, want 3 (one try, two retries)", len(f.fake.shows))
	}
	want := []time.Duration{time.Second, 1100 * time.Millisecond, 1100 * time.Millisecond}
	if len(f.sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", f.sleeps, want)
	}
	for i := range want {
		if f.sleeps[i] != want[i] {
			t.Fatalf("sleeps = %v, want %v", f.sleeps, want)
		}
	}

	// busy, then through on the retry.
	g := newHook(t, StatusBlocked)
	g.fake.reasons = []string{ReasonBusy, ReasonShown}
	if got := g.run(); got != "shown TKB-24 blocked" || len(g.fake.shows) != 2 {
		t.Fatalf("busy then shown: %q after %d shows", got, len(g.fake.shows))
	}
}

func TestRunHookDisabledNoRetry(t *testing.T) {
	for _, reason := range []string{ReasonDisabled, ReasonNoForegroundClient} {
		f := newHook(t, StatusBlocked)
		f.fake.reasons = []string{reason}
		if got := f.run(); !strings.HasPrefix(got, "not shown: "+reason) {
			t.Errorf("%s: decision = %q", reason, got)
		}
		if len(f.fake.shows) != 1 || len(f.sleeps) != 1 {
			t.Errorf("%s: %d shows, sleeps %v; want one try, no retry wait", reason, len(f.fake.shows), f.sleeps)
		}
	}
}

// A herdr that accepts the connection and never answers must not keep the
// hook alive past its context.
func TestRunHookHungSocketBounded(t *testing.T) {
	t.Parallel() // waits on its own deadline; nothing shared
	block := make(chan struct{})
	c := realSocket(t, func(conn net.Conn) {
		readReq(t, conn)
		<-block
	})
	t.Cleanup(func() { close(block) })
	f := newHook(t, StatusBlocked)
	f.deps.Client = c

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan string, 1)
	go func() { done <- RunHook(ctx, f.deps) }()
	select {
	case got := <-done:
		if !strings.HasPrefix(got, "skip: agent.get: context deadline exceeded") {
			t.Fatalf("decision = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook outlived its context on a hung socket")
	}
}

// Same for a filesystem that hangs while the branch is read.
func TestRunHookHungKeysForDirBounded(t *testing.T) {
	t.Parallel() // waits on its own deadline; nothing shared
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	f := newHook(t, StatusBlocked)
	f.deps.KeysForDir = func(context.Context, string) []string {
		<-block
		return []string{"TKB-24"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan string, 1)
	go func() { done <- RunHook(ctx, f.deps) }()
	select {
	case got := <-done:
		if got != "skip: context deadline exceeded" {
			t.Fatalf("decision = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook outlived its context on a hung branch read")
	}
	if f.fake.showCount() != 0 {
		t.Fatal("toasted without a ticket")
	}
}

func TestDecideTitleClippedTo80(t *testing.T) {
	keys := []string{strings.Repeat("A", 60) + "-1", "ÉÉÉÉÉÉÉÉÉÉÉÉÉÉÉÉÉÉÉÉ-2"}
	toast, ok := Decide(keys, StatusBlocked, "")
	if !ok {
		t.Fatal("no toast")
	}
	if n := utf8.RuneCountInString(toast.Title); n != 80 {
		t.Fatalf("title is %d runes, want 80: %q", n, toast.Title)
	}
	if !utf8.ValidString(toast.Title) || !strings.HasSuffix(toast.Title, "…") {
		t.Fatalf("title cut badly: %q", toast.Title)
	}
	if n := utf8.RuneCountInString(toast.Body); n > 240 {
		t.Fatalf("body is %d runes", n)
	}
	// A short title is left alone.
	if short, _ := Decide([]string{"TKB-24"}, StatusDone, "claude"); short.Title != "TKB-24 finished" {
		t.Fatalf("short title = %q", short.Title)
	}
}

func TestDecideMultipleKeysJoined(t *testing.T) {
	toast, ok := Decide([]string{"REVERT-45", "TKB-22"}, StatusBlocked, "claude")
	if !ok || toast.Title != "REVERT-45,TKB-22 needs you" || toast.Sound != SoundRequest {
		t.Fatalf("toast = %+v, ok=%v", toast, ok)
	}
	for _, s := range []Status{StatusIdle, StatusWorking, StatusUnknown, ""} {
		if toast, ok := Decide([]string{"TKB-22"}, s, "claude"); ok {
			t.Errorf("%q toasted %+v", s, toast)
		}
	}
	if toast, ok := Decide(nil, StatusBlocked, "claude"); ok {
		t.Errorf("no keys toasted %+v", toast)
	}
}

// agent.get failing (the pane closed in the settle delay) is a quiet skip.
func TestRunHookAgentGone(t *testing.T) {
	f := newHook(t, StatusBlocked)
	f.fake.getErr = &APIError{Code: "agent_not_found", Message: "agent target wD:p1\nnot found"}
	if got := f.run(); got != "skip: agent.get: herdr: agent_not_found: agent target wD:p1 not found" {
		t.Fatalf("decision = %q", got)
	}
	f.fake.getErr = ErrNoAgent
	if got := f.run(); !strings.HasPrefix(got, "skip: agent.get:") || !errors.Is(f.fake.getErr, ErrNoAgent) {
		t.Fatalf("decision = %q", got)
	}
	if len(f.fake.shows) != 0 {
		t.Fatalf("shows = %+v", f.fake.shows)
	}
}

// state reads the fixture's state file as the hook left it.
func (f *hookFixture) state() map[string]notifyEntry {
	return loadNotifyState(filepath.Join(f.deps.StateDir, NotifyStateName), f.now)
}

// B1: herdr's per-pane seq starts again after a restart while pane ids
// survive it. A pane whose stored seq is higher than its new one must toast
// once the old entry is past the seen window, not stay silent for days.
func TestRunHookSeqResetAfterRestartToasts(t *testing.T) {
	f := newHook(t, StatusBlocked)
	f.fake.agent.StateChangeSeq = 730
	if got := f.run(); got != "shown TKB-24 blocked" {
		t.Fatalf("before restart: %q", got)
	}
	f.now = f.now.Add(2 * time.Hour) // herdr restarted in between
	f.fake.agent.StateChangeSeq = 5
	if got := f.run(); got != "shown TKB-24 blocked" {
		t.Fatalf("after restart: %q", got)
	}
	if got := f.state()[hookPane].Seq; got != 5 {
		t.Fatalf("stored seq = %d, want the new counter's 5", got)
	}
}

// W2: the flap guard is per status. blocked, then done, then blocked again
// inside the cooldown: the second blocked stays quiet.
func TestRunHookFlapGuardPerStatus(t *testing.T) {
	f := newHook(t, StatusBlocked)
	step := func(after time.Duration, s Status, seq uint64) string {
		f.now = f.now.Add(after)
		f.fake.agent.Status, f.fake.agent.StateChangeSeq = s, seq
		f.deps.EventJSON = statusPayload(hookPane, s)
		return f.run()
	}
	if got := step(0, StatusBlocked, 722); got != "shown TKB-24 blocked" {
		t.Fatalf("blocked@0: %q", got)
	}
	if got := step(5*time.Second, StatusDone, 723); got != "shown TKB-24 done" {
		t.Fatalf("done@5: %q", got)
	}
	if got := step(5*time.Second, StatusBlocked, 724); !strings.HasPrefix(got, "skip: blocked toasted under 30s ago") {
		t.Fatalf("blocked@10: %q", got)
	}
	if len(f.fake.shows) != 2 {
		t.Fatalf("shows = %d, want 2", len(f.fake.shows))
	}
}

// W3: a send that failed for a reason a later hook could get past hands its
// claim back, so a straggler for the same prompt can still toast.
func TestRunHookGaveUpReleasesClaim(t *testing.T) {
	f := newHook(t, StatusBlocked)
	f.fake.reasons = []string{ReasonRateLimited}
	if got := f.run(); !strings.HasPrefix(got, "gave up: rate_limited") || !strings.HasSuffix(got, "; claim released") {
		t.Fatalf("decision = %q", got)
	}
	if st := f.state(); len(st) != 0 {
		t.Fatalf("state after give-up = %+v, want the claim gone", st)
	}
	f.fake.reasons = nil // herdr lets the straggler through
	if got := f.run(); got != "shown TKB-24 blocked" {
		t.Fatalf("straggler: %q", got)
	}

	// A socket error releases too, back to the entry before the claim.
	g := newHook(t, StatusBlocked)
	if got := g.run(); got != "shown TKB-24 blocked" {
		t.Fatalf("first: %q", got)
	}
	before := g.state()[hookPane]
	g.now = g.now.Add(time.Hour)
	g.fake.agent.StateChangeSeq = 800
	g.deps.Client = errShow{g.fake}
	if got := g.run(); !strings.HasPrefix(got, "error: show:") || !strings.HasSuffix(got, "; claim released") {
		t.Fatalf("socket error: %q", got)
	}
	if after := g.state()[hookPane]; after.Seq != before.Seq || after.At[StatusBlocked] != before.At[StatusBlocked] {
		t.Fatalf("entry after release = %+v, want %+v", after, before)
	}
}

// errShow answers agent.get from the fake and fails every notification.show.
type errShow struct{ *hookHerdr }

func (errShow) ShowNotification(context.Context, Notification) (bool, string, error) {
	return false, "", errors.New("broken pipe")
}

// W3: disabled and no_foreground_client keep the claim — no retry can help,
// so a straggler for the same prompt stays quiet.
func TestRunHookNotShownKeepsClaim(t *testing.T) {
	for _, reason := range []string{ReasonDisabled, ReasonNoForegroundClient} {
		f := newHook(t, StatusBlocked)
		f.fake.reasons = []string{reason}
		if got := f.run(); strings.Contains(got, "released") {
			t.Errorf("%s: %q", reason, got)
		}
		if st := f.state()[hookPane]; st.Seq != 722 {
			t.Errorf("%s: state = %+v, want the claim kept", reason, st)
		}
		if got := f.run(); got != "skip: seq 722 already handled" {
			t.Errorf("%s: straggler = %q", reason, got)
		}
	}
}

// N2: at most one run per pane and status sleeps through the settle delay.
func TestRunHookOneSettlerPerPaneStatus(t *testing.T) {
	f := newHook(t, StatusBlocked)
	seed := func(s Status, until time.Time) {
		t.Helper()
		if err := saveNotifyState(filepath.Join(f.deps.StateDir, NotifyStateName), map[string]notifyEntry{
			hookPane: {Settle: map[Status]int64{s: until.UnixMilli()}},
		}, f.now); err != nil {
			t.Fatal(err)
		}
	}
	mkdir(t, f.deps.StateDir)

	seed(StatusBlocked, f.now.Add(2*time.Second))
	if got := f.run(); got != "skip: already settling blocked" {
		t.Fatalf("held: %q", got)
	}
	if len(f.sleeps) != 0 || len(f.fake.gets) != 0 {
		t.Fatalf("a second settler slept %v and read %v", f.sleeps, f.fake.gets)
	}

	// Another status, an expired marker and one further ahead than any
	// marker could be (a clock stepped back) do not hold this run.
	for _, c := range []struct {
		name   string
		status Status
		until  time.Duration
	}{
		{"other status", StatusDone, 2 * time.Second},
		{"expired", StatusBlocked, -time.Second},
		{"too far", StatusBlocked, time.Hour},
	} {
		seed(c.status, f.now.Add(c.until))
		f.fake.agent.StateChangeSeq++
		if got := f.run(); got != "shown TKB-24 blocked" {
			t.Errorf("%s: %q", c.name, got)
		}
		f.now = f.now.Add(time.Minute) // clear the flap guard
	}

	// A run that ends early clears its own marker.
	f.fake.agent.Status = StatusWorking
	if got := f.run(); !strings.HasPrefix(got, "skip: status now working") {
		t.Fatalf("stale: %q", got)
	}
	if held, ok := f.state()[hookPane].Settle[StatusBlocked]; ok {
		t.Fatalf("marker left behind: %d", held)
	}
}
