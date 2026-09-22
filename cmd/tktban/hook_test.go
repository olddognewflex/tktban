package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olddognewflex/tktban/internal/herdr"
	"github.com/olddognewflex/tktban/internal/tkt"
	"github.com/olddognewflex/tktban/internal/ui"
)

const blockedPayload = `{"event":"pane_agent_status_changed","data":{"type":"pane_agent_status_changed","pane_id":"wD:p1","workspace_id":"wD","agent_status":"blocked","agent":"claude"}}`

// hookEnv is a complete hook environment over a temp state dir; tests delete
// from it to take one requirement away.
func hookEnv(t *testing.T) (map[string]string, string) {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	return map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_SOCKET_PATH":       "/nonexistent/herdr.sock",
		"HERDR_PLUGIN_STATE_DIR":  state,
		"HERDR_PLUGIN_EVENT":      herdr.StatusEventName,
		"HERDR_PLUGIN_EVENT_JSON": blockedPayload,
	}, state
}

// stubHookSleep takes the hook's settle delay off the wall clock.
func stubHookSleep(t *testing.T) {
	orig := hookSleep
	t.Cleanup(func() { hookSleep = orig })
	hookSleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
}

// AC2: no toasts outside herdr. Without HERDR_ENV=1 the hook does nothing at
// all: no output, no socket, no state dir.
func TestHerdrHookOutsideHerdrNoops(t *testing.T) {
	stubHookSleep(t)
	for _, v := range []string{"", "0", "true"} {
		env, state := hookEnv(t)
		env["HERDR_ENV"] = v
		var out bytes.Buffer
		if code := herdrHook(envOf(env), &out); code != 0 {
			t.Errorf("HERDR_ENV=%q: exit %d", v, code)
		}
		if out.Len() != 0 {
			t.Errorf("HERDR_ENV=%q: printed %q", v, out.String())
		}
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Errorf("HERDR_ENV=%q: state dir touched (err=%v)", v, err)
		}
	}
}

func TestHerdrHookNoSocketNoops(t *testing.T) {
	stubHookSleep(t)
	for _, missing := range []string{"HERDR_SOCKET_PATH", "HERDR_PLUGIN_STATE_DIR", "HERDR_PLUGIN_EVENT", "HERDR_PLUGIN_EVENT_JSON"} {
		env, state := hookEnv(t)
		delete(env, missing)
		var out bytes.Buffer
		if code := herdrHook(envOf(env), &out); code != 0 || out.Len() != 0 {
			t.Errorf("no %s: exit %d, printed %q", missing, code, out.String())
		}
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Errorf("no %s: state dir touched (err=%v)", missing, err)
		}
	}
	env, _ := hookEnv(t)
	env["HERDR_PLUGIN_EVENT"] = "pane.created"
	var out bytes.Buffer
	if code := herdrHook(envOf(env), &out); code != 0 || out.Len() != 0 {
		t.Errorf("other event: exit %d, printed %q", code, out.String())
	}
}

// `tktban herdr-hook` reaches the hook and nothing else: not the board, not
// doctor, not the settings seed, not the board lock.
func TestHerdrHookRouted(t *testing.T) {
	origBoard, origDoctor, origHook, origOut := board, doctor, herdrHook, hookOut
	t.Cleanup(func() { board, doctor, herdrHook, hookOut = origBoard, origDoctor, origHook, origOut })
	board = func(*tkt.Tkt, float64, bool, string, ui.LiveSource, string, func() string, bool) int {
		t.Error("herdr-hook ran the board")
		return 1
	}
	doctor = func(*tkt.Tkt) int {
		t.Error("herdr-hook ran doctor")
		return 1
	}
	for _, argv := range [][]string{{"herdr-hook"}, {"--herdr", "herdr-hook"}} {
		ran := false
		herdrHook = func(func(string) string, io.Writer) int { ran = true; return 0 }
		if code := run(argv); code != 0 || !ran {
			t.Fatalf("%v: exit %d, hook ran=%v", argv, code, ran)
		}
	}

	// The real hook, end to end through run, inside a herdr whose socket is
	// gone: it decides, prints one line, exits 0, and writes nothing a board
	// would.
	herdrHook = origHook
	stubHookSleep(t)
	env, state := hookEnv(t)
	for k, v := range env {
		t.Setenv(k, v)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "tktban"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "tktban", "settings.toml"), []byte("theme = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TKT_BIN", "/nonexistent/tkt")
	var out bytes.Buffer
	hookOut = &out
	if code := run([]string{"--herdr", "herdr-hook"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := out.String(); !strings.HasPrefix(got, "skip: agent.get:") || strings.Count(got, "\n") != 1 {
		t.Fatalf("output = %q, want one skip line", got)
	}
	for _, name := range []string{"settings.toml", herdr.LockName} {
		if _, err := os.Stat(filepath.Join(state, name)); !os.IsNotExist(err) {
			t.Errorf("hook created %s (err=%v)", name, err)
		}
	}
}

// Only the herdr-hook path can toast. Checked on the source: outside
// internal/herdr, the one reference to herdr.RunHook is inside herdrHook, and
// nothing outside internal/herdr names ShowNotification or notification.show.
// So the board, standalone or in herdr, has no way to notify.
func TestOnlyHerdrHookCanNotify(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	runHookRefs := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "bin" || d.Name() == ".claude") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		inHerdr := filepath.Dir(rel) == filepath.Join("internal", "herdr")
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !inHerdr && strings.Contains(string(src), "notification.show") {
			t.Errorf("%s names notification.show", rel)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		var hookSpan [2]token.Pos
		ast.Inspect(f, func(n ast.Node) bool {
			if vs, ok := n.(*ast.ValueSpec); ok && len(vs.Names) == 1 && vs.Names[0].Name == "herdrHook" {
				hookSpan = [2]token.Pos{vs.Pos(), vs.End()}
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || inHerdr {
				return true
			}
			switch sel.Sel.Name {
			case "ShowNotification":
				t.Errorf("%s: ShowNotification outside internal/herdr", fset.Position(sel.Pos()))
			case "RunHook":
				runHookRefs++
				if rel != filepath.Join("cmd", "tktban", "main.go") || sel.Pos() < hookSpan[0] || sel.Pos() > hookSpan[1] {
					t.Errorf("%s: RunHook outside herdrHook", fset.Position(sel.Pos()))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if runHookRefs != 1 {
		t.Fatalf("found %d RunHook references outside internal/herdr, want exactly 1 (in herdrHook)", runHookRefs)
	}
}
