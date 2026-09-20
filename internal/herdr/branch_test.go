package herdr

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

var bg = context.Background()

func TestKeysFromBranch(t *testing.T) {
	cases := []struct {
		branch string
		want   []string
	}{
		{"feature/tkb-22-live", []string{"TKB-22"}},
		{"FEATURE/TKB-22-X", []string{"TKB-22"}},
		{"hotfix/tkb-7-fix", []string{"TKB-7"}},
		{"feature/tkb-22", []string{"TKB-22"}},
		{"tkb-22-no-prefix", []string{"TKB-22"}},
		{"feature/tkb-22-abc-3", []string{"TKB-22"}}, // one key per segment
		{"revert-45-feature/tkb-22-x", []string{"REVERT-45", "TKB-22"}},
		{"feature/tkb-22/tkb-22-x", []string{"TKB-22"}}, // deduped
		{"feature/my_proj-12-x", []string{"MY_PROJ-12"}},
		{"main", nil},
		{"feature/no-key", nil},
		{"feature/2026-release", nil}, // keys start with a letter
		{"feature/tkb-22x-y", nil},    // not a whole key
		{"", nil},
	}
	for _, c := range cases {
		if got := KeysFromBranch(c.branch); !slices.Equal(got, c.want) {
			t.Errorf("KeysFromBranch(%q) = %q, want %q", c.branch, got, c.want)
		}
	}
}

// A reftable repo's HEAD file is a stub; only then does GitBranch ask git.
func TestGitBranchReftableAsksGit(t *testing.T) {
	var asked []string
	orig := symbolicRef
	symbolicRef = func(_ context.Context, dir string) string { asked = append(asked, dir); return "feature/tkb-22-live" }
	t.Cleanup(func() { symbolicRef = orig })

	repo := t.TempDir()
	write(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/.invalid\n")
	if got := GitBranch(bg, repo); got != "feature/tkb-22-live" || len(asked) != 1 || asked[0] != repo {
		t.Fatalf("reftable: GitBranch = %q, asked %q", got, asked)
	}

	plain := t.TempDir()
	write(t, filepath.Join(plain, ".git", "HEAD"), "ref: refs/heads/main\n")
	if GitBranch(bg, plain); len(asked) != 1 {
		t.Fatal("ran git for a plain HEAD file")
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGitBranchMainCheckout(t *testing.T) {
	repo := t.TempDir()
	write(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/feature/tkb-22-live\n")
	if got := GitBranch(bg, repo); got != "feature/tkb-22-live" {
		t.Fatalf("GitBranch = %q", got)
	}
	if got := KeysForDir(bg, repo); len(got) != 1 || got[0] != "TKB-22" {
		t.Fatalf("KeysForDir = %q", got)
	}
}

func TestGitBranchWalksUp(t *testing.T) {
	repo := t.TempDir()
	write(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	deep := mkdir(t, filepath.Join(repo, "internal", "herdr"))
	if got := GitBranch(bg, deep); got != "main" {
		t.Fatalf("GitBranch from subdir = %q, want main", got)
	}
	t.Chdir(deep)
	if got := GitBranch(bg, "."); got != "main" {
		t.Fatalf("GitBranch from relative dir = %q, want main", got)
	}
}

// A linked worktree has a .git file pointing at <main>/.git/worktrees/<name>,
// which holds that worktree's own HEAD.
func TestGitBranchLinkedWorktree(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	write(t, filepath.Join(main, ".git", "HEAD"), "ref: refs/heads/main\n")
	wtGit := filepath.Join(main, ".git", "worktrees", "tkb-22")
	write(t, filepath.Join(wtGit, "HEAD"), "ref: refs/heads/feature/tkb-22-live\n")

	abs := filepath.Join(base, "wt-abs")
	write(t, filepath.Join(abs, ".git"), "gitdir: "+wtGit+"\n")
	if got := GitBranch(bg, filepath.Join(mkdir(t, filepath.Join(abs, "sub")))); got != "feature/tkb-22-live" {
		t.Fatalf("absolute gitdir: GitBranch = %q", got)
	}

	rel := filepath.Join(base, "wt-rel")
	write(t, filepath.Join(rel, ".git"), "gitdir: ../main/.git/worktrees/tkb-22\n")
	if got := GitBranch(bg, rel); got != "feature/tkb-22-live" {
		t.Fatalf("relative gitdir: GitBranch = %q", got)
	}

	// The main checkout is unaffected by its worktree.
	if got := GitBranch(bg, main); got != "main" {
		t.Fatalf("main checkout: GitBranch = %q", got)
	}
}

func TestGitBranchDetachedHead(t *testing.T) {
	repo := t.TempDir()
	write(t, filepath.Join(repo, ".git", "HEAD"), "dccbcd2c0ffee0000000000000000000000000000\n")
	if got := GitBranch(bg, repo); got != "" {
		t.Fatalf("detached HEAD: GitBranch = %q, want empty", got)
	}
}

func TestGitBranchNoRepo(t *testing.T) {
	dir := t.TempDir()
	if findGitDir(dir) != "" {
		t.Skip("temp dir is inside a git repo")
	}
	if got := GitBranch(bg, dir); got != "" {
		t.Fatalf("no repo: GitBranch = %q, want empty", got)
	}
	if got := GitBranch(bg, ""); got != "" {
		t.Fatalf("empty dir: GitBranch = %q, want empty", got)
	}
	bad := t.TempDir()
	write(t, filepath.Join(bad, ".git"), "not a gitdir line\n")
	if got := GitBranch(bg, bad); got != "" {
		t.Fatalf("malformed .git file: GitBranch = %q, want empty", got)
	}
}
