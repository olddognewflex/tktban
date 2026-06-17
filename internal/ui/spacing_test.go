package ui

import (
	"strings"
	"testing"
)

// TestWindowBlocksSeparatesCards verifies cards are joined directly (no blank
// margin row) — each card's full outline provides the visual separation, so the
// board stays compact.
func TestWindowBlocksSeparatesCards(t *testing.T) {
	blocks := []string{"card-a", "card-b", "card-c"}
	out := windowBlocks(blocks, -1, 100)
	if want := "card-a\ncard-b\ncard-c"; out != want {
		t.Fatalf("expected cards joined directly, got:\n%q", out)
	}
}

// TestWindowBlocksHonorsMaxLines keeps the windowing math correct: each card
// reserves only its own block lines (no margin row).
func TestWindowBlocksHonorsMaxLines(t *testing.T) {
	blocks := []string{"a", "b", "c", "d"}
	// Each single-line block reserves 1 line. maxLines 3 fits three cards.
	out := windowBlocks(blocks, -1, 3)
	if got := strings.Count(out, "\n") + 1; got != 3 {
		t.Fatalf("expected 3 cards to fit in 3 lines, got %d:\n%q", got, out)
	}
}
