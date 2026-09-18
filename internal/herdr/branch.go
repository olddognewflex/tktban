package herdr

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// herdr reports each agent pane's directory but not its branch, so the ticket
// join is ours: directory → git branch → ticket key. The branch convention
// comes from .sdlc config (feature/{key-lower}-{slug}, hotfix/{key-lower}-{slug}).

// keyRe finds a ticket key as a whole branch path segment or segment prefix:
// PROJ-123 followed by a separator or the end. "feature/tkb-22x" is no match,
// so a key never reads as a longer one.
var keyRe = regexp.MustCompile(`(?i)(?:^|/)([a-z][a-z0-9]*-[0-9]+)(?:[-_./]|$)`)

// KeyFromBranch returns the first ticket key in a branch name, uppercased, or
// "" when the branch names no ticket.
func KeyFromBranch(branch string) string {
	m := keyRe.FindStringSubmatch(branch)
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

// KeyForDir is the ticket key for whatever branch is checked out at dir.
func KeyForDir(dir string) string {
	return KeyFromBranch(GitBranch(dir))
}

// GitBranch returns the branch checked out in the repo containing dir, or ""
// when dir is in no repo or HEAD is detached. It reads .git directly rather
// than running git: this runs for every agent pane on every poll, and a
// checkout in a pane must show up on the next one.
func GitBranch(dir string) string {
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
