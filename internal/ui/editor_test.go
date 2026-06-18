package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olddognewflex/tktban/internal/tkt"
)

func TestResolveEditor(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	if got := resolveEditor(); len(got) != 1 || got[0] != "vi" {
		t.Fatalf("fallback = %v, want [vi]", got)
	}
	t.Setenv("EDITOR", "nano")
	if got := resolveEditor(); got[0] != "nano" {
		t.Fatalf("EDITOR = %v, want nano", got)
	}
	// VISUAL wins over EDITOR, and args are split off.
	t.Setenv("VISUAL", "code -w")
	if got := resolveEditor(); len(got) != 2 || got[0] != "code" || got[1] != "-w" {
		t.Fatalf("VISUAL = %v, want [code -w]", got)
	}
}

func TestTktApplyArgv(t *testing.T) {
	cr := &captureRunner{}
	tk := tkt.New("", "tkt").WithRunner(cr.run)
	if k, err := tk.Apply("", true, "/tmp/x.md"); err != nil || k != "TKB-99" {
		t.Fatalf("apply --new = %q err=%v", k, err)
	}
	if got := cr.last("apply"); !eq(got, "apply", "--new", "--file", "/tmp/x.md", "--json") {
		t.Fatalf("create argv = %v", got)
	}
	if _, err := tk.Apply("TKB-1", false, "/tmp/x.md"); err != nil {
		t.Fatalf("apply update err=%v", err)
	}
	if got := cr.last("apply"); !eq(got, "apply", "TKB-1", "--file", "/tmp/x.md", "--json") {
		t.Fatalf("update argv = %v", got)
	}
}

// helper: a temp markdown file with given content.
func tempMD(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "buf.md")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Unchanged or empty buffer cancels with no write, and cleans up the temp file.
func TestEditorClosedCancelsOnNoChange(t *testing.T) {
	m, _ := testModel(t)
	for _, tc := range []struct{ name, orig, content string }{
		{"unchanged", "same", "same"},
		{"empty", "x", "   \n"},
	} {
		path := tempMD(t, tc.content)
		nm, _ := m.onEditorClosed(editorClosedMsg{path: path, orig: tc.orig})
		if got := nm.(Model).status; !strings.Contains(got, "No changes") {
			t.Fatalf("%s: status = %q", tc.name, got)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s: buffer not cleaned up", tc.name)
		}
	}
}

// A trailing-newline difference (editors add/strip one) is not a change.
func TestEditorClosedTrailingNewlineIsNoChange(t *testing.T) {
	m, _ := testModel(t)
	for _, tc := range []struct{ orig, content string }{
		{"# x\nbody", "# x\nbody\n"}, // editor added a final newline
		{"# x\nbody\n", "# x\nbody"}, // editor stripped it
	} {
		path := tempMD(t, tc.content)
		nm, _ := m.onEditorClosed(editorClosedMsg{path: path, key: "TKB-1", orig: tc.orig})
		if got := nm.(Model).status; !strings.Contains(got, "No changes") {
			t.Fatalf("orig=%q content=%q: status = %q", tc.orig, tc.content, got)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("buffer not cleaned up on newline-only change")
		}
	}
}

// A web-URL backend (no local file) is rejected with a clear message.
func TestPrepEditorEditRejectsNonLocalURL(t *testing.T) {
	cr := &captureRunner{}
	runner := func(bin string, args, env []string) ([]byte, []byte, int, error) {
		if len(args) >= 1 && args[0] == "view" {
			return []byte(`{"key":"TKB-1","url":"https://example.com/TKB-1"}`), nil, 0, nil
		}
		return cr.run(bin, args, env)
	}
	tk := tkt.New("", "tkt").WithRunner(runner)
	msg := prepEditorEditCmd(tk, "TKB-1")().(editorPrepMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "local markdown backend") {
		t.Fatalf("expected non-local-url error, got %+v", msg)
	}
}

// A changed buffer applies via tkt and reports success, then cleans up.
func TestEditorClosedAppliesChange(t *testing.T) {
	m, cr := testModel(t)
	path := tempMD(t, "# new content\n")
	_, cmd := m.onEditorClosed(editorClosedMsg{path: path, key: "TKB-1", orig: "# old\n"})
	if cmd == nil {
		t.Fatal("expected an apply command")
	}
	msg, ok := cmd().(applyDoneMsg)
	if !ok || msg.err != nil {
		t.Fatalf("apply did not run cleanly: %+v", cmd())
	}
	if got := cr.last("apply"); !eq(got, "apply", "TKB-1", "--file", path, "--json") {
		t.Fatalf("apply argv = %v", got)
	}
	// Buffer is removed only once the apply succeeds.
	nm, _ := m.onApplyDone(msg)
	if !strings.Contains(nm.(Model).status, "Updated") {
		t.Fatalf("status = %q", nm.(Model).status)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("buffer not cleaned up after successful apply")
	}
}

// Editor process error keeps the buffer and reports it.
func TestEditorClosedProcessErrorKeepsBuffer(t *testing.T) {
	m, _ := testModel(t)
	path := tempMD(t, "work in progress")
	nm, _ := m.onEditorClosed(editorClosedMsg{path: path, err: os.ErrPermission})
	if st := nm.(Model).status; !strings.Contains(st, "Editor exited") || !strings.Contains(st, path) {
		t.Fatalf("status = %q", st)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("buffer should be kept after an editor error")
	}
}

// A failed apply (tkt error) keeps the buffer for retry.
func TestApplyDoneErrorKeepsBuffer(t *testing.T) {
	m, _ := testModel(t)
	path := tempMD(t, "draft")
	nm, _ := m.onApplyDone(applyDoneMsg{key: "TKB-1", path: path, err: os.ErrInvalid})
	if st := nm.(Model).status; !strings.Contains(st, "Save failed") || !strings.Contains(st, path) {
		t.Fatalf("status = %q", st)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("buffer should be kept after a failed apply")
	}
}

// prepEditorCreateCmd writes the template to a temp file marked new.
func TestPrepEditorCreate(t *testing.T) {
	m, _ := testModel(t)
	msg := prepEditorCreateCmd(m.tkt)().(editorPrepMsg)
	if msg.err != nil || !msg.isNew {
		t.Fatalf("prep create: err=%v isNew=%v", msg.err, msg.isNew)
	}
	defer os.Remove(msg.path)
	data, _ := os.ReadFile(msg.path)
	if !strings.Contains(string(data), "type: Story") {
		t.Fatalf("template not written: %q", string(data))
	}
}

// prepEditorEditCmd copies the ticket's backing markdown (its url) to a buffer.
func TestPrepEditorEditReadsTicketFile(t *testing.T) {
	src := tempMD(t, "---\ntype: Story\n---\n# existing\n")
	cr := &captureRunner{}
	runner := func(bin string, args, env []string) ([]byte, []byte, int, error) {
		if len(args) >= 1 && args[0] == "view" {
			return []byte(`{"key":"TKB-1","url":"` + src + `"}`), nil, 0, nil
		}
		return cr.run(bin, args, env)
	}
	tk := tkt.New("", "tkt").WithRunner(runner)
	msg := prepEditorEditCmd(tk, "TKB-1")().(editorPrepMsg)
	if msg.err != nil || msg.key != "TKB-1" || msg.isNew {
		t.Fatalf("prep edit: %+v", msg)
	}
	defer os.Remove(msg.path)
	if !strings.Contains(msg.orig, "# existing") {
		t.Fatalf("orig content not loaded: %q", msg.orig)
	}
}
