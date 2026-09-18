// QA Author — adversarial tests
// Break-It dimensions covered: real external dependency (git binary) driving
// the reftable fallback and linked-worktree resolution end to end, isolated
// from the user's own git config so it cannot read real credentials/aliases.
package herdr

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitEnv isolates a git invocation from the user's real git config: no
// global/system config, no user template hooks, HOME pointed at a throwaway
// dir so nothing on the test machine (SSH signing, aliases, credential
// helpers) can leak in or be touched.
func gitEnv(home string) []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+home,
	)
}

// runGit runs git -C dir <args...> under gitEnv, failing the test on error.
func runGit(t *testing.T, dir, home string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv(home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// TestGitBranchReftableEndToEnd exercises the real `git symbolic-ref`
// fallback (branch.go's symbolicRef, unmocked) against an actual reftable
// repository and an actual linked worktree, rather than the mocked
// symbolicRef used by TestGitBranchReftableAsksGit. Skips cleanly when git
// is missing or too old to support reftable.
func TestGitBranchReftableEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	base := t.TempDir()
	main := filepath.Join(base, "main")

	initCmd := exec.Command("git", "-c", "init.templateDir=", "init", "-q",
		"--ref-format=reftable", "-b", "main", main)
	initCmd.Env = gitEnv(home)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git does not support --ref-format=reftable: %v: %s", err, out)
	}

	runGit(t, main, home, "-c", "user.email=qa@example.com", "-c", "user.name=qa",
		"commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, main, home, "checkout", "-q", "-b", "feature/tkb-22-live")

	// HEAD in a reftable repo is the ".invalid" stub; only the real git
	// fallback (not the raw file read) can name the branch.
	head, err := os.ReadFile(filepath.Join(main, ".git", "HEAD"))
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if got := string(head); got != "ref: refs/heads/.invalid\n" {
		t.Fatalf("setup assumption broken: reftable HEAD = %q", got)
	}

	if got := GitBranch(bg, main); got != "feature/tkb-22-live" {
		t.Fatalf("reftable main checkout: GitBranch = %q", got)
	}
	if got := KeysForDir(bg, main); len(got) != 1 || got[0] != "TKB-22" {
		t.Fatalf("reftable main checkout: KeysForDir = %q", got)
	}

	wt := filepath.Join(base, "wt")
	runGit(t, main, home, "worktree", "add", "-q", wt, "-b", "feature/tkb-9-other")

	if got := GitBranch(bg, wt); got != "feature/tkb-9-other" {
		t.Fatalf("reftable linked worktree: GitBranch = %q", got)
	}
	if got := KeysForDir(bg, wt); len(got) != 1 || got[0] != "TKB-9" {
		t.Fatalf("reftable linked worktree: KeysForDir = %q", got)
	}

	// The main checkout must be unaffected by the worktree it spawned.
	if got := GitBranch(bg, main); got != "feature/tkb-22-live" {
		t.Fatalf("reftable main after worktree add: GitBranch = %q", got)
	}
}
