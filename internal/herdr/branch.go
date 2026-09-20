package herdr

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// herdr reports each agent pane's directory but not its branch, so the ticket
// join is ours: directory → git branch → ticket key. The branch convention
// comes from .sdlc config (feature/{key-lower}-{slug}, hotfix/{key-lower}-{slug}).

// keyRe finds a ticket key at the start of one branch path segment: PROJ-123
// (or MY_PROJ-12) followed by a separator or the segment's end.
// "feature/tkb-22x" is no match, so a key never reads as a longer one.
var keyRe = regexp.MustCompile(`(?i)^([a-z][a-z0-9_]*-[0-9]+)(?:[-_.]|$)`)

// KeysFromBranch returns every distinct ticket key in a branch name,
// uppercased, in order, one per path segment at most:
// "revert-45-feature/tkb-22-x" gives REVERT-45 and TKB-22. Callers only look
// up keys that are real tickets, so a segment that merely looks like a key is
// harmless.
func KeysFromBranch(branch string) []string {
	var keys []string
	for seg := range strings.SplitSeq(branch, "/") {
		m := keyRe.FindStringSubmatch(seg)
		if m == nil {
			continue
		}
		if key := strings.ToUpper(m[1]); !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	return keys
}

// KeysForDir is the ticket keys for whatever branch is checked out at dir.
func KeysForDir(ctx context.Context, dir string) []string {
	return KeysFromBranch(GitBranch(ctx, dir))
}

// reftableStub is what HEAD holds in a repo using reftable ref storage: the
// real HEAD lives in the reftable, so the file alone cannot name the branch.
const reftableStub = ".invalid"

// symbolicRef asks git for the branch at dir; only used for reftable repos.
// It stops at the caller's deadline (a poll's), and never runs past a second.
// A variable so tests need no git binary.
var symbolicRef = func(ctx context.Context, dir string) string {
	if ctx.Err() != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "symbolic-ref", "--short", "-q", "HEAD").Output()
	if err != nil {
		return "" // detached, or no git
	}
	return strings.TrimSpace(string(out))
}

// GitBranch returns the branch checked out in the repo containing dir, or ""
// when dir is in no repo or HEAD is detached. It reads .git directly rather
// than running git: this runs for every agent pane on every poll, and a
// checkout in a pane must show up on the next one. Only a reftable repo, whose
// HEAD file is a stub, falls back to running git, bounded by ctx.
func GitBranch(ctx context.Context, dir string) string {
	gitDir := findGitDir(dir)
	if gitDir == "" {
		return ""
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	branch, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
	if !ok {
		return "" // detached HEAD holds a bare commit id
	}
	if branch == reftableStub {
		return symbolicRef(ctx, dir)
	}
	return branch
}

// findGitDir walks up from start to the nearest .git. A directory is the git
// dir itself; a file (linked worktree, submodule) points at it with a
// "gitdir: X" line, where a relative X is relative to the file's directory.
func findGitDir(start string) string {
	if start == "" {
		return ""
	}
	dir, err := filepath.Abs(start) // a relative dir could not walk up
	if err != nil {
		return ""
	}
	for {
		dotGit := filepath.Join(dir, ".git")
		if info, err := os.Stat(dotGit); err == nil {
			if info.IsDir() {
				return dotGit
			}
			return readGitFile(dotGit)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func readGitFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(data), "\n")
	target, ok := strings.CutPrefix(strings.TrimSpace(first), "gitdir:")
	if !ok {
		return ""
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return target
}
