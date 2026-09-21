// QA Author — adversarial tests
// Break-It dimensions covered: open-board.sh's bash-only context reader
// (ctx_field), exercised over odd HERDR_PLUGIN_CONTEXT_JSON payloads — missing
// fields, empty, junk, and values holding the quote/backslash characters the
// regex cannot see past. python3 is deliberately hidden from PATH (by
// building a PATH with none of its directories) so the script falls back to
// ctx_field instead of the python3 branch it prefers; a stub herdr binary
// stands in for HERDR_BIN_PATH so nothing here touches a real herdr socket
// and simply echoes back the argv it was invoked with, which is how each
// case's resulting --cwd (or its absence) is observed.
//
// This is isolable without a live herdr because open-board.sh's own
// documented contract ("herdr sets HERDR_PLUGIN_CONTEXT_JSON ... a plain-bash
// reader takes over rather than letting the pane start in the plugin's own
// install directory") only depends on env vars and an external command it
// already indirects through HERDR_BIN_PATH.
package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// noPythonPATH returns a PATH with every directory that resolves "python3"
// removed, so `command -v python3` inside the script genuinely fails. bash
// itself and the fake herdr binary are always invoked by absolute path, so
// neither needs to be on PATH; only builtins run under this PATH.
func noPythonPATH(t *testing.T) string {
	t.Helper()
	var kept []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "python3")); err == nil {
			continue // this dir is how python3 would be found; drop it
		}
		kept = append(kept, dir)
	}
	return strings.Join(kept, string(os.PathListSeparator))
}

// fakeHerdrBin writes a stub herdr executable that reports each argv element
// on its own line, so a test can see exactly what open-board.sh built for
// --cwd (or that it built no such flag at all) without any real herdr.
func fakeHerdrBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-herdr")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf 'ARG:%s\\n' \"$a\"; done\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// runOpenBoard runs open-board.sh with python3 hidden from PATH and the fake
// herdr binary standing in for the real one, returning its stdout.
func runOpenBoard(t *testing.T, ctxJSON string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("open-board.sh is a bash script")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash on PATH")
	}
	script, err := filepath.Abs("open-board.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("open-board.sh not found at %s: %v", script, err)
	}

	env := []string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + noPythonPATH(t),
		"HERDR_BIN_PATH=" + fakeHerdrBin(t),
		"HERDR_PLUGIN_ID=test.tktban",
	}
	if ctxJSON != "" {
		env = append(env, "HERDR_PLUGIN_CONTEXT_JSON="+ctxJSON)
	}

	cmd := exec.Command(bash, script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("open-board.sh failed: %v\noutput:\n%s", err, out)
	}
	return string(out)
}

// argValue extracts the value passed to a --flag from the ARG: lines the fake
// herdr binary printed, or "", false when the flag was never passed at all.
func argValue(out, flag string) (string, bool) {
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if strings.TrimPrefix(l, "ARG:") == flag {
			if i+1 < len(lines) {
				return strings.TrimPrefix(lines[i+1], "ARG:"), true
			}
			return "", true
		}
	}
	return "", false
}

func TestOpenBoardCtxFieldFallback(t *testing.T) {
	cases := []struct {
		name       string
		ctx        string
		wantCwd    string
		wantNoCwd  bool
		wantEnvSet bool
	}{
		{
			name:       "spaces in path",
			ctx:        `{"focused_pane_cwd":"/a/b c","workspace_cwd":"/x"}`,
			wantCwd:    "/a/b c",
			wantEnvSet: true,
		},
		{
			name:       "workspace_cwd fallback when focused pane names none",
			ctx:        `{"workspace_cwd":"/ws only"}`,
			wantCwd:    "/ws only",
			wantEnvSet: true,
		},
		{
			name:       "missing fields entirely",
			ctx:        `{"workspace_id":"w1"}`,
			wantNoCwd:  true,
			wantEnvSet: true,
		},
		{
			name:      "empty context (unset)",
			ctx:       "",
			wantNoCwd: true,
		},
		{
			name:       "junk, not json at all",
			ctx:        `not json at all`,
			wantNoCwd:  true,
			wantEnvSet: true, // ctx is non-empty, so it is still forwarded raw
		},
		{
			name:       "embedded backslash reads as empty, not a wrong path",
			ctx:        `{"focused_pane_cwd":"C:\Users\x","other":"y"}`,
			wantNoCwd:  true,
			wantEnvSet: true,
		},
		{
			// A path containing a literal quote is valid JSON only escaped
			// as \", i.e. a backslash immediately before the quote in the
			// raw text — the same character class the regex excludes, so
			// this hits the identical failure mode as a bare backslash.
			name:       "embedded (escaped) quote reads as empty, not a wrong path",
			ctx:        `{"focused_pane_cwd":"a\"quoted\"b","other":"y"}`,
			wantNoCwd:  true,
			wantEnvSet: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := runOpenBoard(t, c.ctx)
			cwd, hasCwd := argValue(out, "--cwd")
			if c.wantNoCwd && hasCwd {
				t.Fatalf("ctx %q: got --cwd %q, want no --cwd flag at all\nfull output:\n%s", c.ctx, cwd, out)
			}
			if c.wantCwd != "" && (!hasCwd || cwd != c.wantCwd) {
				t.Fatalf("ctx %q: --cwd = %q (present=%v), want %q\nfull output:\n%s", c.ctx, cwd, hasCwd, c.wantCwd, out)
			}
			_, hasEnv := argValue(out, "--env")
			if hasEnv != c.wantEnvSet {
				t.Fatalf("ctx %q: --env present=%v, want %v\nfull output:\n%s", c.ctx, hasEnv, c.wantEnvSet, out)
			}
		})
	}
}

// A context this odd must still leave --env carrying the exact raw text
// (never reinterpreted or re-escaped), since herdr's own JSON parser is the
// one that ultimately makes sense of it downstream, not this launcher.
func TestOpenBoardForwardsRawContextVerbatim(t *testing.T) {
	ctx := `{"focused_pane_cwd":"/a/b c","weird":"has \"quote\" and \\backslash"}`
	out := runOpenBoard(t, ctx)
	envFlag := regexp.MustCompile(`ARG:--env\nARG:HERDR_PLUGIN_CONTEXT_JSON=(.*)`)
	m := envFlag.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no --env HERDR_PLUGIN_CONTEXT_JSON= arg found:\n%s", out)
	}
	if m[1] != ctx {
		t.Fatalf("context forwarded as %q, want the exact original %q", m[1], ctx)
	}
}
