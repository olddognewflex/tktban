package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
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
	board = func(*tkt.Tkt, float64, bool, string, ui.LiveSource, ui.DispatchOpts, string, func() string, bool) int {
		t.Error("herdr-hook ran the board")
		return 1
	}
	doctor = func(*tkt.Tkt) int {
		t.Error("herdr-hook ran doctor")
		return 1
	}
	pinEnv(t, "") // run() reads the real environment and settings file
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

// Only the herdr-hook path can toast. Checked on the source, test files
// excluded:
//   - the "notification.show" method string appears only inside
//     (*Client).ShowNotification in internal/herdr/client.go;
//   - ShowNotification is called only from internal/herdr's send, and send
//     only from RunHook;
//   - outside internal/herdr the one RunHook reference is inside herdrHook,
//     whether that is declared `var herdrHook = func` or `func herdrHook`.
//
// So the board, standalone or in herdr, has no way to notify.
func TestOnlyHerdrHookCanNotify(t *testing.T) {
	root := filepath.Join("..", "..")
	herdrDir := filepath.Join("internal", "herdr")
	mainGo := filepath.Join("cmd", "tktban", "main.go")
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
		inHerdr := filepath.Dir(rel) == herdrDir
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, fn := range enclosing(f) {
			ast.Inspect(fn.body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.BasicLit:
					if n.Kind == token.STRING && strings.Contains(n.Value, "notification.show") &&
						(rel != filepath.Join(herdrDir, "client.go") || fn.name != "(*Client).ShowNotification") {
						t.Errorf("%s: notification.show named in %s", fset.Position(n.Pos()), fn.name)
					}
				case *ast.SelectorExpr:
					switch n.Sel.Name {
					case "ShowNotification":
						if !inHerdr || fn.name != "send" {
							t.Errorf("%s: ShowNotification called from %s", fset.Position(n.Pos()), fn.name)
						}
					case "RunHook":
						if !inHerdr {
							runHookRefs++
							if rel != mainGo || fn.name != "herdrHook" {
								t.Errorf("%s: RunHook referenced from %s", fset.Position(n.Pos()), fn.name)
							}
						}
					}
				case *ast.CallExpr:
					if id, ok := n.Fun.(*ast.Ident); ok && inHerdr && id.Name == "send" && fn.name != "RunHook" {
						t.Errorf("%s: send called from %s", fset.Position(n.Pos()), fn.name)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if runHookRefs != 1 {
		t.Fatalf("found %d RunHook references outside internal/herdr, want exactly 1 (in herdrHook)", runHookRefs)
	}
}

type namedBody struct {
	name string
	body ast.Node
}

// enclosing splits a file into its top-level declarations, each named by
// the function or variable it declares ("" for anything else), so a check can
// ask which one a reference sits in. A method is named with its receiver
// type, "(*Client).ShowNotification", so it never passes for a function.
func enclosing(f *ast.File) []namedBody {
	var out []namedBody
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) == 1 {
				name = "(" + types.ExprString(d.Recv.List[0].Type) + ")." + name
			}
			out = append(out, namedBody{name, d})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				name := ""
				if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Names) == 1 {
					name = vs.Names[0].Name
				}
				out = append(out, namedBody{name, spec})
			}
		}
	}
	return out
}
