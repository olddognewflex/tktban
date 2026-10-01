package ui

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/model"
	"github.com/olddognewflex/tktban/internal/tkt"
)

// Write-back (TKB-29): the board's live poll already knows when an agent pane
// closes or stops on a prompt, which a skill that wrote `processing` cannot
// learn for itself. With reconcile_agent_status on, the board writes that back
// to the ticket's frontmatter, so `tkt view` and every other reader agree with
// the badge instead of reading a `processing` nobody is doing.
//
// It is deliberately narrow. Three writes, and nothing else:
//
//   - a pane seen this session has gone, and the ticket still says a
//     processing stamped before the pane was last seen → idle
//   - herdr says the pane is blocked, and the ticket says processing → waiting
//   - herdr says it is working again, and the ticket still holds a waiting this
//     board wrote → processing
//
// done is never written: the board cannot tell a finished agent from one that
// was killed. A key the board has never seen a pane for is never written,
// since nothing on the board knows anything about it. And a waiting a skill
// wrote is never cleared — only a waiting this board wrote itself, recognised
// by its agent_status_at, is treated like processing.
//
// Everything is decided on a successful live poll and nothing else. A failed
// poll, the fallback to frontmatter badges, a protocol mismatch and running
// outside herdr all leave src nil or the result unseen, so none of them can
// look like a closed pane.
//
// "Gone" is decided per pane, not per key. A ticket key comes from the pane's
// branch, so a live agent drops off its key whenever that branch stops naming
// it: a detached HEAD mid-rebase, a checkout inside the worktree, a cd out of
// it. Each key therefore remembers the panes it was last seen with, and only
// counts as absent once none of them is in the poll's set of live agent panes
// (herdr.Snapshot.AgentPanes) — the pane closed, or herdr released its agent.
// A pane that is alive but on no key, or on another key, holds the key where
// it is: no write, and no progress towards one. That holds while the key is
// still present through another pane too: a pane that drifts off stays
// tracked for as long as it is alive, so the other pane closing is not taken
// for the agent leaving the ticket.

// Frontmatter agent_status values the write-back reads and writes.
const (
	agentProcessing = "processing"
	agentWaiting    = "waiting"
	agentIdle       = "idle"
)

// reconcileGonePolls is how many consecutive good polls must miss every pane a
// key was last seen with before the key counts as gone. One is not enough: an
// agent herdr has just re-registered can drop out of a single agent.list. (A
// pane whose branch merely stopped naming the key never counts; see above.)
// There is no cooldown for a pane that flaps in and out for longer than this:
// each return starts a new episode, and each later close may write once more.
const reconcileGonePolls = 2

// reconcileGoneFor is the least time between the last poll that saw a key's
// pane and the poll that may write its idle. The poll count alone shrinks
// with the poll interval, and two quick polls can both land inside one herdr
// hiccup; this keeps the debounce a span of time as well as a count.
const reconcileGoneFor = 3 * time.Second

// reconcileCallTimeout bounds each tkt call a write-back batch makes. The
// board latches recon.inFlight until the batch reports back, so a wedged tkt
// has to be killed or no write-back would run again this session. Writes here
// are frontmatter edits, local to the markdown backend, so it is shorter than
// archiveCallTimeout.
const reconcileCallTimeout = 5 * time.Second

// reconcileRepoTimeout bounds the one `tkt cfg vcs` read at startup, the same
// budget the dispatch guards give a config read.
const reconcileRepoTimeout = 2 * time.Second

// reconcileState is the write-back's memory for one board session. keys holds
// a pointer per ticket so the copies bubbletea makes of Model share it, the
// same way swept is shared.
type reconcileState struct {
	enabled bool // reconcile_agent_status, read once in New
	// boardRepo is the board's [vcs].repo, lowercased. "" until the startup
	// read lands, and for good when the board has none: with no repo to
	// compare panes against there is no way to tell this board's agents from
	// another repo's that happen to share a ticket key, so nothing is written.
	boardRepo string
	keys      map[string]*reconKey // uppercase keys seen with a pane this session
	inFlight  bool                 // one batch at a time
}

// reconKey is what the board knows about one ticket's agent panes.
//
// Each *Tried flag makes a write once per episode: it is set when a write is
// planned, whatever comes of it, and only cleared by herdr moving on — the pane
// reappearing re-arms idle, leaving blocked re-arms waiting, going blocked
// again re-arms the restore. So a write that fails, or that a re-read talks it
// out of, is not retried on every poll.
type reconKey struct {
	present     bool         // on this key at the last good poll
	absentPolls int          // consecutive good polls with none of panes alive
	status      herdr.Status // herdr's status at the last poll it was present
	repos       []string     // each pane's repo at that poll, lowercased
	panes       []string     // each pane's id at that poll
	// lastPresent is when the last good poll that had a pane on this key
	// started. An idle is only written over a processing stamped before it
	// (see reconcileCmd), so a processing written after the pane closed — by
	// another machine, or an agent outside herdr — is never taken for the
	// closed pane's. The poll's start, not its end: a slow poll may have read
	// herdr before a processing stamped while it ran.
	lastPresent time.Time

	idleTried, waitTried, restoreTried bool

	// wroteWaitingAt is the agent_status_at of a waiting this board wrote and
	// has not since replaced; "" means none. A waiting with any other stamp
	// was written by someone else, and is left alone.
	//
	// The stamp is a weak owner mark. tkt restamps agent_status_at only when
	// the value changes (markdown adapter's edit), so a skill that sets
	// waiting over the board's waiting leaves the stamp as it was, and its
	// waiting is then taken for the board's. Likewise a processing→waiting
	// round trip by someone else inside one stamp second. The window is
	// narrow, and the cost is an idle or processing written over a waiting
	// that agrees with the board's own reading of the pane anyway.
	wroteWaitingAt string
	// wroteProcessingAt is the agent_status_at of a processing this board
	// restored and has not since replaced; "" means none. The restore lands
	// in the same second as the poll that saw the pane working, so if that
	// was the pane's last sighting stampedBefore would refuse the idle and
	// the board's own processing would stick. A processing still holding this
	// stamp is the board's, and passes. The same weak owner mark as above.
	wroteProcessingAt string
}

// reconcileItem is one planned write.
type reconcileItem struct {
	key  string
	want string // the agent_status to write
	// boardAt is the stamp of the waiting this board wrote, carried so the
	// command can tell it from a waiting written since; "" when none.
	boardAt string
	// boardProcAt is the same for a processing this board restored, for an
	// idle; "" when none.
	boardProcAt string
	// lastPresent is the key's reconKey.lastPresent, for an idle.
	lastPresent time.Time
}

// reconcileResult is how one planned write went. at is the agent_status_at
// after a write; ours reports, for a skipped item, whether the ticket still
// holds the waiting this board wrote (false lets the board forget it).
type reconcileResult struct {
	key   string
	want  string
	wrote bool
	at    string
	ours  bool
}

// reconcileMsg is a finished write-back batch. failed carries per-key error
// text, as archiveMsg does.
type reconcileMsg struct {
	results []reconcileResult
	failed  map[string]string
}

// reconcileRepoMsg is the board's [vcs].repo, read once at startup.
type reconcileRepoMsg struct{ repo string }

// reconcileEnabled reads the opt-in setting. Only a real TOML true turns it
// on, like dispatch: a typo'd "true" string must not start writing tickets.
func reconcileEnabled(s map[string]any) bool {
	on, _ := s["reconcile_agent_status"].(bool)
	return on
}

// reconcileInit reads the board's repo for the repo guard, off the UI loop.
// Nil when write-back is off or there is no live source, since nothing would
// ever be written.
func (m Model) reconcileInit() tea.Cmd {
	if !m.recon.enabled || m.live.src == nil {
		return nil
	}
	tk := m.tkt
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), reconcileRepoTimeout)
		defer cancel()
		return reconcileRepoMsg{repo: tk.WithContext(ctx).VCS().Repo}
	}
}

func (m Model) onReconcileRepo(msg reconcileRepoMsg) (tea.Model, tea.Cmd) {
	m.recon.boardRepo = strings.ToLower(strings.TrimSpace(msg.repo))
	return m, nil
}

// observe folds one good poll into the per-key memory. It runs on every good
// poll, write-back on or off, ready or not, so that "seen this session" and
// the absent-poll count are true whenever writing starts. agentPanes is the
// poll's set of live agent pane ids (herdr.Snapshot.AgentPanes); nil means
// the poll could not say, and then no key moves towards gone. at is when the
// poll started.
func (r *reconcileState) observe(byKey map[string]herdr.Live, agentPanes map[string]bool, at time.Time) {
	if r.keys == nil {
		r.keys = map[string]*reconKey{}
	}
	seen := make(map[string]bool, len(byKey)) // uppercase keys this poll
	prev := map[string][]string{}             // each seen key's panes before this poll
	for key, l := range byKey {
		key = strings.ToUpper(key)
		k := r.keys[key]
		if k == nil {
			k = &reconKey{}
			r.keys[key] = k
		}
		if !k.present {
			k.idleTried = false // a new episode: the pane is back
		}
		if !seen[key] { // the first spelling of key this poll replaces the last poll's panes
			prev[key] = k.panes
			k.repos, k.panes = nil, nil
		}
		seen[key] = true
		k.present, k.absentPolls, k.status, k.lastPresent = true, 0, l.Status, at
		for _, p := range l.Panes {
			k.repos = append(k.repos, strings.ToLower(p.Repo))
			if !slices.Contains(k.panes, p.PaneID) {
				k.panes = append(k.panes, p.PaneID)
			}
		}
		if l.Status != herdr.StatusBlocked {
			k.waitTried = false
		} else {
			k.restoreTried = false
		}
	}
	// A pane tracked last poll that has drifted off the key but is still a
	// live agent stays tracked: it may still be working the ticket. Only
	// panes, not repos, carry over — the repo guard reads the panes on the
	// key now.
	for key, old := range prev {
		k := r.keys[key]
		for _, id := range old {
			if agentPanes[id] && !slices.Contains(k.panes, id) {
				k.panes = append(k.panes, id)
			}
		}
	}
	for key, k := range r.keys {
		if seen[key] {
			continue
		}
		k.present = false
		if agentPanes == nil {
			continue // no pane set this poll: no evidence either way
		}
		if slices.ContainsFunc(k.panes, func(id string) bool { return agentPanes[id] }) {
			// The agent is still there, its branch just names no key or
			// another one. Not gone: the count starts over.
			k.absentPolls = 0
			continue
		}
		k.absentPolls++
	}
}

// reconcile is the write-back step of a good poll: it records the poll, then
// starts a batch if one is due and allowed. Only onLive's success branch
// calls it.
func (m *Model) reconcile() tea.Cmd {
	m.recon.observe(m.live.byKey, m.live.agentPanes, m.live.polledAt)
	r := &m.recon
	if !r.enabled || m.live.src == nil || !m.live.on || r.boardRepo == "" || r.inFlight {
		return nil
	}
	items := planReconcile(r.keys, m.frontStatuses(), r.boardRepo, m.live.polledAt)
	if len(items) == 0 {
		return nil
	}
	for _, it := range items {
		k := r.keys[it.key]
		switch it.want {
		case agentIdle:
			k.idleTried = true
		case agentWaiting:
			k.waitTried = true
		case agentProcessing:
			k.restoreTried = true
		}
	}
	r.inFlight = true
	return reconcileCmd(m.tkt, items)
}

// frontStatuses is each loaded card's frontmatter agent_status, by uppercase
// key. It is the cheap prefilter: the command re-reads every ticket before it
// writes, so a stale card costs at most one skipped write.
func (m Model) frontStatuses() map[string]string {
	out := map[string]string{}
	for _, col := range m.allColumns {
		for _, c := range col.Cards {
			out[strings.ToUpper(c.Key)] = c.AgentStatus
		}
	}
	return out
}

// planReconcile picks the writes due, in key order. It is pure: the caller
// marks what it returns as tried. A key that does not qualify is not marked,
// so it is looked at again next poll — the card may simply not have refreshed
// yet. polledAt is when this poll started; an idle needs reconcileGoneFor
// between it and the key's last sighting.
func planReconcile(keys map[string]*reconKey, front map[string]string, boardRepo string, polledAt time.Time) []reconcileItem {
	var out []reconcileItem
	for key, k := range keys {
		f := front[key]
		boardWaiting := f == agentWaiting && k.wroteWaitingAt != ""
		var want string
		switch {
		case !k.present && k.absentPolls >= reconcileGonePolls && !k.idleTried &&
			!k.lastPresent.IsZero() && polledAt.Sub(k.lastPresent) >= reconcileGoneFor &&
			(f == agentProcessing || boardWaiting):
			want = agentIdle
		case k.present && k.status == herdr.StatusBlocked && !k.waitTried && f == agentProcessing:
			want = agentWaiting
		case k.present && k.status == herdr.StatusWorking && !k.restoreTried && boardWaiting:
			want = agentProcessing
		default:
			continue
		}
		if !reposMatch(k.repos, boardRepo) {
			continue
		}
		out = append(out, reconcileItem{key: key, want: want, boardAt: k.wroteWaitingAt,
			boardProcAt: k.wroteProcessingAt, lastPresent: k.lastPresent})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// reposMatch is the repo guard: every pane on the key is in the board's repo.
// A pane whose repo is unknown fails it, as does a key with no panes recorded.
// Ticket keys are only unique within a board, so an agent in another repo on
// a branch that happens to name TKB-29 must not move this board's TKB-29.
func reposMatch(repos []string, boardRepo string) bool {
	if boardRepo == "" || len(repos) == 0 {
		return false
	}
	for _, r := range repos {
		if r == "" || !strings.EqualFold(r, boardRepo) {
			return false
		}
	}
	return true
}

// reconcileCmd makes the planned writes, one at a time, off the UI loop.
//
// Each ticket is re-read first and written only if it still holds what the
// plan assumed: processing, or the very waiting this board wrote (same
// agent_status_at). Anything else means a skill or a person wrote it since,
// and their value wins. An idle over processing also needs that processing
// stamped before the pane was last seen (stampedBefore): one written later is
// someone else's, however long after the close it lands — unless it is the
// very processing this board restored (same agent_status_at), which may share
// the pane's last second. An idle is also
// skipped while `tkt agents` says a headless run is running or stalled on the
// ticket: that driver owns its status, and a closed herdr pane says nothing
// about it. A tkt without the agents verb (exit 64) has no headless runs; any
// other failure to read them skips the idles rather than guess.
func reconcileCmd(tk *tkt.Tkt, items []reconcileItem) tea.Cmd {
	return func() tea.Msg {
		var out reconcileMsg
		fail := func(key string, err error) {
			if out.failed == nil {
				out.failed = map[string]string{}
			}
			out.failed[key] = err.Error()
		}
		// A fresh bounded copy per call, as archiveCmd does.
		call := func(f func(*tkt.Tkt) error) error {
			ctx, cancel := context.WithTimeout(context.Background(), reconcileCallTimeout)
			defer cancel()
			return f(tk.WithContext(ctx))
		}
		var runs map[string]tkt.AgentRun
		var runsErr error
		for _, it := range items {
			if it.want == agentIdle {
				runsErr = call(func(b *tkt.Tkt) (err error) { runs, err = b.Agents(); return err })
				if te, ok := errors.AsType[*tkt.Error](runsErr); ok && te.ExitCode == 64 {
					runs, runsErr = nil, nil // an older tkt: no headless runs to defer to
				}
				break
			}
		}
		for _, it := range items {
			res := reconcileResult{key: it.key, want: it.want}
			var t model.Ticket
			if err := call(func(b *tkt.Tkt) (err error) { t, err = b.View(it.key); return err }); err != nil {
				fail(it.key, err)
				res.ours = it.boardAt != "" // unknown: keep remembering it
				out.results = append(out.results, res)
				continue
			}
			cur, _ := t["agent_status"].(string)
			at, _ := t["agent_status_at"].(string)
			ours := it.boardAt != "" && cur == agentWaiting && at == it.boardAt
			res.ours = ours
			allowed := false
			switch it.want {
			case agentIdle:
				allowed = ours || cur == agentProcessing &&
					(stampedBefore(at, it.lastPresent) || it.boardProcAt != "" && at == it.boardProcAt)
			case agentWaiting:
				allowed = cur == agentProcessing
			case agentProcessing:
				allowed = ours
			}
			if !allowed {
				out.results = append(out.results, res)
				continue
			}
			if it.want == agentIdle {
				if runsErr != nil {
					fail(it.key, errors.New("tkt agents unreadable: "+runsErr.Error()))
					out.results = append(out.results, res)
					continue
				}
				if s := runs[it.key].State; s == "running" || s == "stalled" {
					out.results = append(out.results, res) // the headless driver owns it
					continue
				}
			}
			want := it.want
			var written model.Ticket
			if err := call(func(b *tkt.Tkt) (err error) {
				written, err = b.Edit(it.key, tkt.EditOpts{AgentStatus: &want})
				return err
			}); err != nil {
				fail(it.key, err)
				out.results = append(out.results, res)
				continue
			}
			res.wrote, res.ours = true, false
			res.at, _ = written["agent_status_at"].(string)
			if res.at == "" && want != agentIdle {
				// Only a waiting or a restored processing needs its stamp;
				// re-read for a tkt whose edit
				// reply leaves it out. Still none means the board cannot
				// recognise it later, so it is not remembered as its own.
				_ = call(func(b *tkt.Tkt) error {
					v, err := b.View(it.key)
					if err == nil {
						res.at, _ = v["agent_status_at"].(string)
					}
					return err
				})
			}
			out.results = append(out.results, res)
		}
		return out
	}
}

// stampedBefore reports whether an agent_status_at certainly predates t.
// tkt stamps whole seconds, rounded down, so a stamp in t's own second may
// be later than t and does not count; neither does a stamp that is missing or
// unparseable, nor a zero t. Wrong in the safe direction only: an idle it
// refuses is a stale processing left on the ticket, not a false write. (A
// writer whose clock runs behind can still slip under it; no stamp can say.)
func stampedBefore(stamp string, t time.Time) bool {
	if t.IsZero() {
		return false
	}
	s, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return false
	}
	return s.Before(t.Truncate(time.Second))
}

// onReconcile records a finished batch. A write remembers or forgets the
// board's own waiting and processing; a skip forgets the waiting once the
// ticket no longer holds it (a stale processing stamp is harmless: the
// command only honours it while the ticket still carries it).
// Tried flags stay set either way (see reconKey). Any write refreshes the
// board so the cards show what the tickets now say; failures warn, since the
// board itself is fine.
func (m Model) onReconcile(msg reconcileMsg) (tea.Model, tea.Cmd) {
	m.recon.inFlight = false
	wrote := false
	for _, res := range msg.results {
		k := m.recon.keys[res.key]
		if k == nil {
			continue
		}
		switch {
		case res.wrote:
			k.wroteWaitingAt, k.wroteProcessingAt = "", ""
			switch res.want {
			case agentWaiting:
				k.wroteWaitingAt = res.at
			case agentProcessing:
				k.wroteProcessingAt = res.at
			}
			wrote = true
		case !res.ours:
			k.wroteWaitingAt = ""
		}
	}
	var refresh, warn tea.Cmd
	if wrote {
		refresh = m.refreshCmd()
	}
	if len(msg.failed) > 0 {
		warn = m.setStatus("agent status write-back failed: "+joinErrors(msg.failed), "warn")
	}
	return m, tea.Batch(warn, refresh)
}
