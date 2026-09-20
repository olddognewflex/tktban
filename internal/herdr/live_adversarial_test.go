// QA Author — adversarial tests
// Break-It dimensions covered: an unrecognized agent_status string from
// herdr (forward compatibility with a status this build does not know
// about), and an exhaustive pairwise rank ordering across every known
// status plus that unknown one.
package herdr

import "testing"

// herdr may one day send a status this build predates (e.g. "sleeping").
// It must be treated exactly like StatusUnknown: lowest rank, no badge
// (badge mapping is covered on the ui side by TestLiveBadgeMapping's
// "surprise" case; this covers the herdr-side ranking that feeds it).
func TestResolveUnknownStatusStringRanksLikeUnknown(t *testing.T) {
	keyFor := keysByDir(map[string]string{"/a": "TKB-1"})
	got := Resolve([]Agent{
		agent("p1", Status("sleeping"), "", "/a"),
		agent("p2", StatusIdle, "", "/a"),
	}, keyFor)
	if got["TKB-1"].Status != StatusIdle {
		t.Fatalf("unrecognized status outranked idle: %q", got["TKB-1"].Status)
	}

	// Symmetric: unrecognized status alone still resolves the pane (it is
	// still an agent-owned ticket key), it just never wins the rank.
	got = Resolve([]Agent{agent("p1", Status("sleeping"), "", "/a")}, keyFor)
	if got["TKB-1"].Status != Status("sleeping") {
		t.Fatalf("lone unrecognized status = %q", got["TKB-1"].Status)
	}
}

func TestRankOrderingExhaustive(t *testing.T) {
	order := []Status{StatusBlocked, StatusWorking, StatusIdle, StatusDone, StatusUnknown, Status("sleeping"), Status("")}
	ranks := make([]int, len(order))
	for i, s := range order {
		ranks[i] = rank(s)
	}
	// blocked > working > {idle == done} > {unknown == "sleeping" == ""}
	if !(ranks[0] > ranks[1]) {
		t.Fatalf("blocked (%d) must outrank working (%d)", ranks[0], ranks[1])
	}
	if !(ranks[1] > ranks[2]) {
		t.Fatalf("working (%d) must outrank idle (%d)", ranks[1], ranks[2])
	}
	if ranks[2] != ranks[3] {
		t.Fatalf("idle (%d) and done (%d) must tie", ranks[2], ranks[3])
	}
	if !(ranks[3] > ranks[4]) {
		t.Fatalf("done (%d) must outrank unknown (%d)", ranks[3], ranks[4])
	}
	if ranks[4] != ranks[5] || ranks[5] != ranks[6] {
		t.Fatalf("unknown (%d), \"sleeping\" (%d) and \"\" (%d) must all tie at the floor", ranks[4], ranks[5], ranks[6])
	}
}
