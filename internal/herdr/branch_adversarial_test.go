// QA Author — adversarial tests
// Break-It dimensions covered: boundary/encoding edge cases for
// KeysFromBranch (empty/degenerate input, unicode, very long input, leading
// zeros, digits-only segments), and GitBranch failure modes (garbage .git
// file, gitdir pointing nowhere, CRLF HEAD, symlinked cwd, a cwd that no
// longer exists).
package herdr

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestKeysFromBranchAdversarial(t *testing.T) {
	cases := []struct {
		name   string
		branch string
		want   []string
	}{
		{"only slashes", "///", nil},
		{"already-cut refs/heads prefix", "refs/heads/tkb-22-live", []string{"TKB-22"}},
		{"trailing slash", "feature/tkb-22-live/", []string{"TKB-22"}},
		{"unicode segment", "feature/tkb-22-café", []string{"TKB-22"}},
		{"very long branch", strings.Repeat("x", 5000) + "/tkb-22-done", []string{"TKB-22"}},
		{"leading zeros preserved, not normalized", "feature/tkb-007-fix", []string{"TKB-007"}},
		{"digits-only prefix never matches", "123-45-feature", nil},
		{"digits-only segment alone", "123-45", nil},
		{"dup across segments differs only by case", "feature/TKB-22/tkb-22-x", []string{"TKB-22"}},
		{"underscore-heavy key", "feature/my_proj_2-9-x", []string{"MY_PROJ_2-9"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := KeysFromBranch(c.branch); !slices.Equal(got, c.want) {
				t.Errorf("KeysFromBranch(%q) = %q, want %q", c.branch, got, c.want)
			}
		})
	}
}

// A .git file whose first line is not labeled "gitdir:" must never be
// followed as a pointer, even when the line's text happens to be the
// absolute path of a real, unrelated repo (a decoy here). Treating any
// line as a path, instead of requiring the "gitdir:" label, would leak an
// unrelated checkout's branch into this one.
func TestGitBranchGitFileWrongMarker(t *testing.T) {
	base := t.TempDir()
	decoy := filepath.Join(base, "decoy")
	write(t, filepath.Join(decoy, "HEAD"), "ref: refs/heads/unrelated-decoy-branch\n")

	repo := filepath.Join(base, "repo")
	write(t, filepath.Join(repo, ".git"), decoy+"\n") // no "gitdir:" label
	if got := GitBranch(bg, repo); got != "" {
		t.Fatalf("unlabeled .git file: GitBranch = %q, want empty (must not follow the decoy)", got)
	}
}

// A .git file pointing at a gitdir that does not exist must not crash and
// must not fall back to an ancestor's unrelated repo.
func TestGitBranchGitFileTargetMissing(t *testing.T) {
	repo := t.TempDir()
	write(t, filepath.Join(repo, ".git"), "gitdir: "+filepath.Join(repo, "nonexistent-gitdir")+"\n")
	if got := GitBranch(bg, repo); got != "" {
		t.Fatalf("missing gitdir target: GitBranch = %q, want empty", got)
	}
}

// HEAD written with CRLF (a checkout from a Windows tool, or a mangled
// worktree file) must still resolve to the plain branch name.
func TestGitBranchHeadCRLF(t *testing.T) {
	repo := t.TempDir()
	write(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\r\n")
	if got := GitBranch(bg, repo); got != "main" {
		t.Fatalf("CRLF HEAD: GitBranch = %q, want %q", got, "main")
	}
}

// A symlinked working directory (a common shape for herdr panes that share a
// checkout through a symlinked path) must resolve the same as the real path.
func TestGitBranchSymlinkedCwd(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	write(t, filepath.Join(real, ".git", "HEAD"), "ref: refs/heads/feature/tkb-22-live\n")

	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	if got := GitBranch(bg, link); got != "feature/tkb-22-live" {
		t.Fatalf("symlinked cwd: GitBranch = %q, want %q", got, "feature/tkb-22-live")
	}
}

// A pane whose working directory has been removed (its worktree deleted
// mid-session) must resolve to "" rather than panicking.
func TestGitBranchDeletedCwd(t *testing.T) {
	base := t.TempDir()
	if findGitDir(base) != "" {
		t.Skip("temp dir is inside a git repo")
	}
	gone := filepath.Join(base, "gone")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	got := GitBranch(bg, gone) // must not panic
	if got != "" {
		t.Fatalf("deleted cwd: GitBranch = %q, want empty", got)
	}
}
