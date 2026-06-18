package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olddognewflex/tktban/internal/tkt"
)

// $EDITOR integration (TKB-10): edit a whole ticket as one markdown file.
//
// Flow: a prep command writes the starting markdown to a temp file (the create
// template, or the existing ticket's markdown) and emits editorPrepMsg; the
// model then suspends the TUI with tea.ExecProcess to run $EDITOR; on close,
// onEditorClosed compares the buffer and either cancels (unchanged/empty) or
// runs `tkt apply`. On any failure the temp buffer is kept so work isn't lost.

type editorPrepMsg struct {
	path  string // temp markdown file to open
	key   string // target ticket; "" when creating
	isNew bool
	orig  string // starting content, to detect "no changes"
	err   error
}

type editorClosedMsg struct {
	path  string
	key   string
	isNew bool
	orig  string
	err   error // editor process error (non-zero exit, spawn failure)
}

type applyDoneMsg struct {
	key   string
	isNew bool
	path  string
	err   error
}

// resolveEditor honours $VISUAL then $EDITOR, falling back to vi. The value may
// carry args (e.g. "code -w"), so it is split into command + args.
func resolveEditor() []string {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			if fields := strings.Fields(v); len(fields) > 0 {
				return fields
			}
		}
	}
	return []string{"vi"}
}

func writeTempMarkdown(content string) (string, error) {
	f, err := os.CreateTemp("", "tktban-*.md")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// prepEditorCreateCmd writes the create template to a temp file for a new ticket.
func prepEditorCreateCmd(tk *tkt.Tkt) tea.Cmd {
	return func() tea.Msg {
		tmpl, err := tk.ApplyTemplate()
		if err != nil {
			return editorPrepMsg{err: err}
		}
		path, err := writeTempMarkdown(tmpl)
		if err != nil {
			return editorPrepMsg{err: err}
		}
		return editorPrepMsg{path: path, isNew: true, orig: tmpl}
	}
}

// prepEditorEditCmd copies an existing ticket's markdown to a temp file. The
// source is the ticket's backing file (the markdown provider's `url`).
func prepEditorEditCmd(tk *tkt.Tkt, key string) tea.Cmd {
	return func() tea.Msg {
		t, err := tk.View(key)
		if err != nil {
			return editorPrepMsg{err: err}
		}
		// The markdown backend exposes the ticket's file as `url`. Other backends
		// (jira/github) put a web URL there, which isn't a local editable file.
		src, _ := t["url"].(string)
		if src == "" || strings.Contains(src, "://") {
			return editorPrepMsg{err: fmt.Errorf("editing %s in $EDITOR needs a local markdown backend (url %q)", key, src)}
		}
		data, rerr := os.ReadFile(src)
		if rerr != nil {
			return editorPrepMsg{err: fmt.Errorf("can't read ticket markdown for %s: %w", key, rerr)}
		}
		path, werr := writeTempMarkdown(string(data))
		if werr != nil {
			return editorPrepMsg{err: werr}
		}
		return editorPrepMsg{path: path, key: key, orig: string(data)}
	}
}

// launchEditorCmd suspends the TUI and runs $EDITOR on the prepared file.
func launchEditorCmd(p editorPrepMsg) tea.Cmd {
	ed := resolveEditor()
	args := append(ed[1:], p.path)
	cmd := exec.Command(ed[0], args...) // #nosec G204 — editor from user's own env
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorClosedMsg{path: p.path, key: p.key, isNew: p.isNew, orig: p.orig, err: err}
	})
}

func applyEditorCmd(tk *tkt.Tkt, key string, isNew bool, path string) tea.Cmd {
	return func() tea.Msg {
		k, err := tk.Apply(key, isNew, path)
		return applyDoneMsg{key: k, isNew: isNew, path: path, err: err}
	}
}

// onEditorClosed runs after $EDITOR exits: surface a process error (keeping the
// buffer), cancel on an unchanged/empty buffer, or apply the edited markdown.
func (m Model) onEditorClosed(msg editorClosedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.setStatus(fmt.Sprintf("Editor exited with error: %v (buffer kept at %s)", msg.err, msg.path), "error")
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		return m, m.setStatus("Couldn't read the edited buffer: "+err.Error(), "error")
	}
	content := string(data)
	// Compare ignoring a trailing-newline difference: many editors add or strip a
	// final newline, which must not count as a change (else an unmodified create
	// template would be shipped as a junk ticket).
	if strings.TrimSpace(content) == "" || strings.TrimRight(content, "\n") == strings.TrimRight(msg.orig, "\n") {
		os.Remove(msg.path)
		return m, m.setStatus("No changes — nothing saved", "")
	}
	return m, applyEditorCmd(m.tkt, msg.key, msg.isNew, msg.path)
}

// onApplyDone reports the apply result. On failure the buffer is kept for retry;
// on success it is removed and the board refreshes (selection is preserved by
// the refresh path).
func (m Model) onApplyDone(msg applyDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.setStatus(fmt.Sprintf("Save failed: %v (buffer kept at %s)", msg.err, msg.path), "error")
	}
	os.Remove(msg.path)
	verb := "Updated "
	if msg.isNew {
		verb = "Created "
	}
	return m, tea.Batch(m.setStatus(verb+msg.key, ""), refreshCmd(m.tkt, m.filter))
}
