// QA Author — adversarial tests
// Break-It dimensions covered: SelectKey when one directory's branch names
// several tickets (first must win, matching KeysFromBranch's own order), and
// SelectKey wired to the real (non-injected) KeysForDir against directories
// that do not exist at all, to prove the production path degrades the same
// way the pure unit tests already assume.
package herdr

import (
	"context"
	"path/filepath"
	"testing"
)

// A branch can name several tickets at once (e.g. "revert-45-feature/tkb-22").
// SelectKey must take the first key that dir's own resolver returns, exactly
// as KeysFromBranch orders them, rather than the last or some other pick.
func TestSelectKeyMultipleKeysInOneDirPicksFirst(t *testing.T) {
	keysForDir := func(dir string) []string {
		if dir == "/multi" {
			return []string{"REVERT-45", "TKB-22"}
		}
		return nil
	}
	e := Env{InHerdr: true, Context: Context{FocusedPaneCwd: "/multi"}}
	if got := SelectKey(e, "/cwd", keysForDir); got != "REVERT-45" {
		t.Fatalf("SelectKey = %q, want the first key REVERT-45", got)
	}
}

// A directory that does not exist on disk at all (not merely lacking a repo)
// must not panic and must fall through exactly like a keyless directory,
// using the real production KeysForDir (which shells out to git only for
// reftable repos, and stats plain files otherwise).
func TestSelectKeyRealKeysForDirNonexistentDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	e := Env{InHerdr: true, Context: Context{FocusedPaneCwd: missing, WorkspaceCwd: missing}}
	got := SelectKey(e, missing, func(dir string) []string {
		return KeysForDir(context.Background(), dir)
	})
	if got != "" {
		t.Fatalf("SelectKey over a nonexistent directory = %q, want empty", got)
	}
}
