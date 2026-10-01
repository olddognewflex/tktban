//go:build unix

// Tests for open-board.sh's rebuild of a stale ./bin/tktban. The script is
// copied into a temp plugin root (it finds the root from its own location),
// and a fake `go` on PATH stands in for the compiler: it records each call
// and writes the -o target, or fails when told to. A fake herdr echoes its
// argv, so every case also checks the board still opens.
package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pluginRoot lays out a temp plugin root holding a copy of open-board.sh,
// one Go source file, and (when bin is non-empty) a bin/tktban with that
// content. It returns the root.
func pluginRoot(t *testing.T, bin string) string {
	t.Helper()
	root := t.TempDir()
	src, err := os.ReadFile("open-board.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"scripts", "cmd/tktban", "internal", "bin"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"scripts/open-board.sh": string(src),
		"cmd/tktban/main.go":    "package main\n",
		"go.mod":                "module x\n",
		"go.sum":                "",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if bin != "" {
		if err := os.WriteFile(filepath.Join(root, "bin/tktban"), []byte(bin), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// seeded is every source file pluginRoot writes.
var seeded = []string{"cmd/tktban/main.go", "go.mod", "go.sum"}

// touch sets path's mtime to now+offset.
func touch(t *testing.T, path string, offset time.Duration) {
	t.Helper()
	when := time.Now().Add(offset)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// fakeGo writes a stub `go` into a fresh dir and returns that dir. Each call
// appends a line to log and prints GO-STDOUT; with fail set it exits 1, else
// it writes "NEW" to the path after -o.
func fakeGo(t *testing.T, log string, fail bool) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho call >> '" + log + "'\necho GO-STDOUT\n"
	if fail {
		script += "echo 'compile error' >&2\nexit 1\n"
	} else {
		script += "while [ $# -gt 0 ]; do if [ \"$1\" = -o ]; then printf NEW > \"$2\"; fi; shift; done\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runAutobuild runs the copied script with goDir first on PATH and returns
// its stdout and stderr, failing the test if the script fails or the board
// was not opened. Only the fake herdr's ARG lines may reach stdout: herdr
// reads the action's output, so the build must not write there.
func runAutobuild(t *testing.T, root, goDir string, extraEnv ...string) (stdout, stderr string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("open-board.sh is a bash script")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash on PATH")
	}
	cmd := exec.Command(bash, filepath.Join(root, "scripts/open-board.sh"))
	cmd.Env = append([]string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + goDir + string(os.PathListSeparator) + "/usr/bin:/bin",
		"HERDR_BIN_PATH=" + fakeHerdrBin(t),
		"HERDR_PLUGIN_ID=test.tktban",
	}, extraEnv...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("open-board.sh failed: %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "ARG:--entrypoint") {
		t.Fatalf("board was not opened\nstdout:\n%s\nstderr:\n%s", out.String(), errb.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !strings.HasPrefix(line, "ARG:") {
			t.Fatalf("stdout has a non-herdr line %q\nstdout:\n%s", line, out.String())
		}
	}
	return out.String(), errb.String()
}

func calls(t *testing.T, log string) int {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "call")
}

func binContent(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "bin/tktban"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOpenBoardRebuildsStaleBinary(t *testing.T) {
	cases := map[string]string{
		"main.go":         "cmd/tktban/main.go",
		"nested internal": "internal/ui/view.go",
		"go.mod":          "go.mod",
		"go.sum":          "go.sum",
	}
	for name, newer := range cases {
		t.Run(name, func(t *testing.T) {
			root := pluginRoot(t, "OLD")
			path := filepath.Join(root, newer)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// Only the file under test may be newer than the binary.
			for _, f := range seeded {
				touch(t, filepath.Join(root, f), -2*time.Hour)
			}
			touch(t, filepath.Join(root, "bin/tktban"), -time.Hour)
			touch(t, path, 0)
			log := filepath.Join(t.TempDir(), "go.log")
			runAutobuild(t, root, fakeGo(t, log, false))
			if n := calls(t, log); n != 1 {
				t.Fatalf("go called %d times, want 1", n)
			}
			if got := binContent(t, root); got != "NEW" {
				t.Fatalf("bin/tktban = %q, want the rebuilt one", got)
			}
		})
	}
}

func TestOpenBoardBuildsMissingBinary(t *testing.T) {
	root := pluginRoot(t, "")
	log := filepath.Join(t.TempDir(), "go.log")
	runAutobuild(t, root, fakeGo(t, log, false))
	if got := binContent(t, root); got != "NEW" {
		t.Fatalf("bin/tktban = %q, want it built", got)
	}
}

func TestOpenBoardSkipsFreshBinary(t *testing.T) {
	root := pluginRoot(t, "OLD")
	for _, f := range seeded {
		touch(t, filepath.Join(root, f), -time.Hour)
	}
	// A newer non-Go file does not count.
	if err := os.WriteFile(filepath.Join(root, "internal/notes.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "go.log")
	runAutobuild(t, root, fakeGo(t, log, false))
	if n := calls(t, log); n != 0 {
		t.Fatalf("go called %d times for a fresh binary, want 0", n)
	}
	if got := binContent(t, root); got != "OLD" {
		t.Fatalf("bin/tktban = %q, want it untouched", got)
	}
}

func TestOpenBoardKeepsBinaryWhenBuildFails(t *testing.T) {
	root := pluginRoot(t, "OLD")
	touch(t, filepath.Join(root, "bin/tktban"), -time.Hour)
	log := filepath.Join(t.TempDir(), "go.log")
	_, stderr := runAutobuild(t, root, fakeGo(t, log, true))
	if got := binContent(t, root); got != "OLD" {
		t.Fatalf("bin/tktban = %q, want the old binary kept", got)
	}
	if !strings.Contains(stderr, "rebuild failed") {
		t.Fatalf("no failure message\nstderr:\n%s", stderr)
	}
	leftovers, _ := filepath.Glob(filepath.Join(root, "bin/tktban.build.*"))
	if len(leftovers) != 0 {
		t.Fatalf("temp build files left behind: %v", leftovers)
	}
}

func TestOpenBoardAutobuildOptOut(t *testing.T) {
	root := pluginRoot(t, "OLD")
	touch(t, filepath.Join(root, "bin/tktban"), -time.Hour)
	log := filepath.Join(t.TempDir(), "go.log")
	runAutobuild(t, root, fakeGo(t, log, false), "TKTBAN_NO_AUTOBUILD=1")
	if n := calls(t, log); n != 0 {
		t.Fatalf("go called %d times with TKTBAN_NO_AUTOBUILD=1, want 0", n)
	}
}

func TestOpenBoardWarnsWhenGoMissing(t *testing.T) {
	if _, err := os.Stat("/usr/bin/go"); err == nil {
		t.Skip("go is in /usr/bin, so it cannot be hidden from PATH")
	}
	root := pluginRoot(t, "OLD")
	touch(t, filepath.Join(root, "bin/tktban"), -time.Hour)
	_, stderr := runAutobuild(t, root, t.TempDir()) // an empty dir: no go
	if !strings.Contains(stderr, "go is not on PATH") {
		t.Fatalf("no missing-go warning\nstderr:\n%s", stderr)
	}
	if got := binContent(t, root); got != "OLD" {
		t.Fatalf("bin/tktban = %q, want it untouched", got)
	}
}

// A press while the board is open is the toggle's close: it must not sit
// through a build first. The test holds board.lock the way a running board
// does.
func TestOpenBoardSkipsBuildWhileBoardRuns(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("the lock probe needs python3")
	}
	state := t.TempDir()
	f, err := os.Create(filepath.Join(state, "board.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Skipf("cannot flock: %v", err)
	}
	root := pluginRoot(t, "OLD")
	touch(t, filepath.Join(root, "bin/tktban"), -time.Hour)
	log := filepath.Join(t.TempDir(), "go.log")
	runAutobuild(t, root, fakeGo(t, log, false), "HERDR_PLUGIN_STATE_DIR="+state)
	if n := calls(t, log); n != 0 {
		t.Fatalf("go called %d times while a board holds the lock, want 0", n)
	}

	// Released: the next press opens, so it builds.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	runAutobuild(t, root, fakeGo(t, log, false), "HERDR_PLUGIN_STATE_DIR="+state)
	if n := calls(t, log); n != 1 {
		t.Fatalf("go called %d times after the board closed, want 1", n)
	}
}
