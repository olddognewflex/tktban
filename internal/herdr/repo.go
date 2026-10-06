package herdr

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// RepoForDir returns "owner/name" for the GitHub repository containing dir,
// taken from its origin remote, or "" when there is no repository, no origin,
// or origin is not a github.com repo. The result is lowercase, since GitHub
// owner and repo names compare case-insensitively. Like GitBranch it only
// reads files, so it cannot hang on a git subprocess.
//
// A linked worktree keeps its config in the main repository: the per-worktree
// gitdir names it in a commondir file, so that is followed. A config.worktree
// in the gitdir itself overrides it when it sets an origin url, but only when
// the common config enables extensions.worktreeConfig, as in git. insteadOf
// rewrites and includeIf are not handled; the result then fails to match
// whatever it is compared with, which is the safe direction.
func RepoForDir(dir string) string {
	gitDir := findGitDir(dir)
	if gitDir == "" {
		return ""
	}
	common := gitDir
	if raw, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		if c := strings.TrimSpace(string(raw)); c != "" {
			if !filepath.IsAbs(c) {
				c = filepath.Join(gitDir, c)
			}
			common = filepath.Clean(c)
		}
	}
	cfg, err := os.ReadFile(filepath.Join(common, "config"))
	if err != nil {
		return ""
	}
	if worktreeConfigEnabled(string(cfg)) {
		if wcfg, err := os.ReadFile(filepath.Join(gitDir, "config.worktree")); err == nil {
			if u := originURL(string(wcfg)); u != "" {
				return normalizeRepo(u)
			}
		}
	}
	return normalizeRepo(originURL(string(cfg)))
}

// worktreeConfigEnabled reports whether the common config sets
// extensions.worktreeConfig to a true value, the only case in which git reads
// config.worktree. Section and key are case-insensitive, the last assignment
// wins, and a bare key counts as true, as in git.
func worktreeConfigEnabled(cfg string) bool {
	inExt, on := false, false
	for _, line := range strings.Split(cfg, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "["), "]")
			inExt = strings.EqualFold(strings.TrimSpace(name), "extensions")
			continue
		}
		if !inExt {
			continue
		}
		k, v, hasVal := strings.Cut(line, "=")
		if !strings.EqualFold(strings.TrimSpace(k), "worktreeConfig") {
			continue
		}
		if !hasVal {
			on = true
			continue
		}
		switch strings.ToLower(configValue(v)) {
		case "true", "yes", "on", "1":
			on = true
		default:
			on = false
		}
	}
	return on
}

// originURL returns the first url of the [remote "origin"] section. As in git,
// the section keyword is case-insensitive but the subsection name is not, so
// [remote "Origin"] is a different remote.
func originURL(cfg string) string {
	inOrigin := false
	for _, line := range strings.Split(cfg, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inOrigin = isOriginHeader(line)
			continue
		}
		if !inOrigin {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.EqualFold(strings.TrimSpace(k), "url") {
			return configValue(v)
		}
	}
	return ""
}

// isOriginHeader reports whether line is a [remote "origin"] header. Anything
// after the closing bracket (a trailing comment) is ignored.
func isOriginHeader(line string) bool {
	rest := strings.TrimPrefix(line, "[")
	kw, rest, ok := strings.Cut(rest, `"`)
	if !ok || !strings.EqualFold(strings.TrimSpace(kw), "remote") {
		return false
	}
	sub, rest, ok := strings.Cut(rest, `"`)
	return ok && sub == "origin" && strings.HasPrefix(strings.TrimSpace(rest), "]")
}

// configValue cleans a raw config value the way git does: an unquoted # or ;
// starts a comment, double quotes group text and are dropped, and a backslash
// escapes the next character inside them.
func configValue(raw string) string {
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\\' && i+1 < len(raw):
			i++
			b.WriteByte(raw[i])
		case c == '"':
			inQuote = !inQuote
		case !inQuote && (c == '#' || c == ';'):
			return strings.TrimSpace(b.String())
		default:
			b.WriteByte(c)
		}
	}
	return strings.TrimSpace(b.String())
}

// normalizeRepo reduces a github.com remote URL to lowercase "owner/name":
// scheme, credentials, port, a trailing slash and ".git" all go. It accepts
// https/http/ssh/git URLs and scp-like [user@]host:path, and returns "" for
// any other host, a local or file:// path, or a path that is not exactly
// owner/name (a gitlab-style subgroup, say). Lowercasing makes the comparison
// case-insensitive, as GitHub treats names.
func normalizeRepo(url string) string {
	url = strings.TrimSpace(url)
	var host, path string
	if scheme, rest, ok := strings.Cut(url, "://"); ok {
		switch strings.ToLower(scheme) {
		case "https", "http", "ssh", "git":
		default:
			return ""
		}
		// scheme://[user[:pw]@]host[:port]/path
		auth, p, ok := strings.Cut(rest, "/")
		if !ok {
			return ""
		}
		if i := strings.LastIndex(auth, "@"); i >= 0 {
			auth = auth[i+1:]
		}
		var port string
		var hasPort bool
		host, port, hasPort = strings.Cut(auth, ":")
		if hasPort && !validPort(port) {
			return ""
		}
		path = p
	} else if h, p, ok := strings.Cut(url, ":"); ok && !strings.Contains(h, "/") {
		// scp-like [user@]host:path
		if i := strings.LastIndex(h, "@"); i >= 0 {
			h = h[i+1:]
		}
		host, path = h, p
	} else {
		return ""
	}
	if !strings.EqualFold(host, "github.com") {
		return ""
	}
	path = strings.TrimRight(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimRight(path, "/")
	parts := strings.Split(strings.TrimLeft(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return strings.ToLower(parts[0] + "/" + parts[1])
}

// validPort reports whether p is a decimal TCP port, 1 to 65535. An empty or
// non-numeric port is how a lookalike authority such as github.com:evil.example
// would otherwise slip past a split at the first colon.
func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535 && strings.Trim(p, "0123456789") == ""
}
