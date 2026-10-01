package herdr

import (
	"path/filepath"
	"testing"
)

const originCfg = "[core]\n\tbare = false\n[remote \"origin\"]\n\turl = git@github.com:o/n.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"

func TestRepoForDirMainCheckout(t *testing.T) {
	main := t.TempDir()
	write(t, filepath.Join(main, ".git", "config"), originCfg)
	if got := RepoForDir(mkdir(t, filepath.Join(main, "sub"))); got != "o/n" {
		t.Fatalf("RepoForDir = %q, want o/n", got)
	}
}

// A linked worktree's gitdir has no config of its own; commondir leads to the
// main repository's.
func TestRepoForDirLinkedWorktree(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, "main", ".git", "config"), originCfg)
	wtGit := filepath.Join(base, "main", ".git", "worktrees", "wt")
	write(t, filepath.Join(wtGit, "HEAD"), "ref: refs/heads/x\n")
	write(t, filepath.Join(wtGit, "commondir"), "../..\n")
	write(t, filepath.Join(base, "wt", ".git"), "gitdir: "+wtGit+"\n")
	if got := RepoForDir(filepath.Join(base, "wt")); got != "o/n" {
		t.Fatalf("RepoForDir = %q, want o/n", got)
	}
}

// A submodule's .git file points into the parent's modules dir, which holds
// its own config and no commondir.
func TestRepoForDirSubmodule(t *testing.T) {
	base := t.TempDir()
	mod := filepath.Join(base, "parent", ".git", "modules", "sub")
	write(t, filepath.Join(mod, "config"), "[remote \"origin\"]\n\turl = https://github.com/s/m.git\n")
	write(t, filepath.Join(base, "parent", "sub", ".git"), "gitdir: "+mod+"\n")
	if got := RepoForDir(filepath.Join(base, "parent", "sub")); got != "s/m" {
		t.Fatalf("RepoForDir = %q, want s/m", got)
	}
}

func TestRepoForDirOrigin(t *testing.T) {
	cases := map[string]struct{ cfg, want string }{
		"no remote":            {"[core]\n\tbare = false\n", ""},
		"no origin":            {"[remote \"upstream\"]\n\turl = git@github.com:u/p.git\n", ""},
		"no url":               {"[remote \"origin\"]\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n", ""},
		"after other":          {"[remote \"upstream\"]\n\turl = git@github.com:u/p.git\n[remote \"origin\"]\n\turl = git@github.com:o/n.git\n", "o/n"},
		"url in later section": {"[remote \"origin\"]\n\tfetch = x\n[branch \"main\"]\n\turl = git@github.com:b/b.git\n", ""},
		"Origin subsection":    {"[remote \"Origin\"]\n\turl = git@github.com:x/y.git\n[remote \"origin\"]\n\turl = git@github.com:o/n.git\n", "o/n"},
		"only Origin":          {"[remote \"Origin\"]\n\turl = git@github.com:x/y.git\n", ""},
		"Remote keyword":       {"[Remote \"origin\"]\n\tURL = git@github.com:o/n.git\n", "o/n"},
		"header comment":       {"[remote \"origin\"] # c\n\turl = git@github.com:o/n.git\n", "o/n"},
		"quoted value":         {"[remote \"origin\"]\n\turl = \"git@github.com:o/n.git\"\n", "o/n"},
		"inline hash comment":  {"[remote \"origin\"]\n\turl = git@github.com:o/n.git # mirror\n", "o/n"},
		"inline semi comment":  {"[remote \"origin\"]\n\turl = git@github.com:o/n.git ; mirror\n", "o/n"},
		"quoted then comment":  {"[remote \"origin\"]\n\turl = \"git@github.com:o/n.git\" # x\n", "o/n"},
		"not github":           {"[remote \"origin\"]\n\turl = git@gitlab.com:o/n.git\n", ""},
	}
	for name, c := range cases {
		dir := t.TempDir()
		write(t, filepath.Join(dir, ".git", "config"), c.cfg)
		if got := RepoForDir(dir); got != c.want {
			t.Errorf("%s: RepoForDir = %q, want %q", name, got, c.want)
		}
	}
}

// A relative gitdir: path in a worktree's .git file resolves against the
// directory holding that file.
func TestRepoForDirRelativeGitdir(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, "main", ".git", "config"), originCfg)
	wtGit := filepath.Join(base, "main", ".git", "worktrees", "wt")
	write(t, filepath.Join(wtGit, "HEAD"), "ref: refs/heads/x\n")
	write(t, filepath.Join(wtGit, "commondir"), "../..\n")
	write(t, filepath.Join(base, "wt", ".git"), "gitdir: ../main/.git/worktrees/wt\n")
	if got := RepoForDir(filepath.Join(base, "wt")); got != "o/n" {
		t.Fatalf("RepoForDir = %q, want o/n", got)
	}
}

// config.worktree overrides the common config when it sets an origin url, and
// is ignored when it does not.
func TestRepoForDirConfigWorktree(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, "main", ".git", "config"), originCfg)
	wtGit := filepath.Join(base, "main", ".git", "worktrees", "wt")
	write(t, filepath.Join(wtGit, "commondir"), "../..\n")
	write(t, filepath.Join(base, "wt", ".git"), "gitdir: "+wtGit+"\n")
	wt := filepath.Join(base, "wt")

	write(t, filepath.Join(wtGit, "config.worktree"), "[core]\n\tbare = false\n")
	if got := RepoForDir(wt); got != "o/n" {
		t.Fatalf("no origin in config.worktree: RepoForDir = %q, want o/n", got)
	}
	write(t, filepath.Join(wtGit, "config.worktree"), "[remote \"origin\"]\n\turl = https://github.com/w/t.git\n")
	if got := RepoForDir(wt); got != "w/t" {
		t.Fatalf("RepoForDir = %q, want w/t", got)
	}
}

func TestRepoForDirNotARepo(t *testing.T) {
	if got := RepoForDir(t.TempDir()); got != "" {
		t.Fatalf("RepoForDir = %q, want empty", got)
	}
	if got := RepoForDir(""); got != "" {
		t.Fatalf("RepoForDir(\"\") = %q, want empty", got)
	}
}

func TestNormalizeRepo(t *testing.T) {
	cases := map[string]string{
		// accepted forms
		"git@github.com:o/n.git":                 "o/n",
		"github.com:o/n.git":                     "o/n",
		"https://github.com/o/n":                 "o/n",
		"https://github.com/o/n.git":             "o/n",
		"https://github.com/o/n/":                "o/n",
		"https://github.com/o/n.git/":            "o/n",
		"https://user@github.com/o/n.git":        "o/n",
		"https://user:pw@github.com:443/o/n.git": "o/n",
		"ssh://git@github.com/o/n.git":           "o/n",
		"ssh://git@github.com:22/o/n.git":        "o/n",
		"git://github.com/o/n":                   "o/n",
		"HTTPS://GitHub.com/o/n":                 "o/n",
		"  git@github.com:o/n.git  ":             "o/n",
		"https://github.com/Owner/Name.git":      "owner/name",
		"git@GitHub.com:Owner/Name.git":          "owner/name",
		// rejected
		"":                                "",
		"n":                               "",
		"/":                               "",
		"https://github.com":              "",
		"https://github.com/":             "",
		"https://github.com/n":            "",
		"git@github.com:n":                "",
		"https://github.com/o/.git":       "",
		"file:///x/o/n":                   "",
		"file://github.com/o/n":           "",
		"/x/o/n":                          "",
		"./o/n":                           "",
		"o/n":                             "",
		"../github.com/o/n":               "",
		"/x/github.com:o/n":               "",
		"https://gitlab.com/o/n":          "",
		"https://host.example/g/o/n/":     "",
		"git@gitlab.com:o/n.git":          "",
		"https://notgithub.com/o/n":       "",
		"https://github.com.evil.com/o/n": "",
		"https://github.com@evil.com/o/n": "",
		"https:///o/n":                    "",
		"ssh://git@/o/n.git":              "",
		"git@:o/n.git":                    "",
		"https://github.com/g/o/n":        "",
		"git@github.com:g/o/n.git":        "",
		"ftp://github.com/o/n":            "",
	}
	for in, want := range cases {
		if got := normalizeRepo(in); got != want {
			t.Errorf("normalizeRepo(%q) = %q, want %q", in, got, want)
		}
	}
}
