// QA Author — adversarial tests
// Break-It dimensions covered: FocusPane with a pane id containing characters
// that are awkward in JSON or shells (quotes, backslashes, unicode, spaces),
// over a real unix socket so the actual json.Marshal round trip is exercised,
// not a hand-built request.
package herdr

import (
	"context"
	"net"
	"testing"
)

// A pane id is herdr's to name, and tktban must send back exactly what it
// received without escaping surprises corrupting the wire request.
func TestFocusPaneOddCharacterIDs(t *testing.T) {
	cases := []string{
		`wC:p1"with"quotes`,
		`wC:p1\with\backslashes`,
		"wC:p1 with spaces",
		"wC:p1-emoji-🎉-pane",
		"wC:p1\twith\ttabs",
		"",
	}
	for _, id := range cases {
		var got map[string]any
		c := realSocket(t, func(conn net.Conn) {
			got = readReq(t, conn)
			conn.Write([]byte(`{"id":"x","result":{}}` + "\n"))
		})
		if err := c.FocusPane(context.Background(), id); err != nil {
			t.Fatalf("id %q: focus: %v", id, err)
		}
		params, ok := got["params"].(map[string]any)
		if !ok {
			t.Fatalf("id %q: params = %v, want an object", id, got["params"])
		}
		if params["pane_id"] != id {
			t.Fatalf("id %q: server received %v", id, params["pane_id"])
		}
	}
}
