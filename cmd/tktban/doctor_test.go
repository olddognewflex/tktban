package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/olddognewflex/tktban/internal/tkt"
)

// doctorOutput runs the doctor command over a fake tkt that reports roles and
// an empty `all` query, returning what it printed and its exit status.
func doctorOutput(t *testing.T, roles string) (string, int) {
	t.Helper()
	run := func(_ context.Context, _ string, args, _ []string) ([]byte, []byte, int, error) {
		if len(args) > 0 && args[0] == "list" {
			return []byte("[]"), nil, 0, nil
		}
		return []byte(roles), nil, 0, nil
	}
	// "sh" is on PATH, so the binary check passes without a stub.
	tk := tkt.New("", "sh").WithRunner(run)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	code := doctor(tk)
	os.Stdout = orig
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// TKB-27: a board with no archived role gets a hint, not a failure.
func TestDoctorPrintsHintAndPasses(t *testing.T) {
	out, code := doctorOutput(t, `{"todo": "To Do", "done": "Done"}`)
	if code != 0 {
		t.Fatalf("exit = %d with only a hint; output:\n%s", code, out)
	}
	if !strings.Contains(out, `[hint] archive lane — auto-archive off: add archived = "Archived" under [board.roles]`) {
		t.Fatalf("hint line missing:\n%s", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Fatalf("a hint printed as a failure:\n%s", out)
	}
}

func TestDoctorArchivedRoleIsOK(t *testing.T) {
	out, code := doctorOutput(t, `{"todo": "To Do", "done": "Done", "archived": "Archived"}`)
	if code != 0 || !strings.Contains(out, "[ok  ] archive lane — archived role configured") {
		t.Fatalf("exit = %d; output:\n%s", code, out)
	}
}
