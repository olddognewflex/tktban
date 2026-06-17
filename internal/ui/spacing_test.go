package ui

import (
	"strings"
	"testing"
)

// TestWindowBlocksSeparatesCards verifies cards are rendered with a blank line
// between them so they read as visibly separate (TKB-15).
func TestWindowBlocksSeparatesCards(t *testing.T) {
	blocks := []string{"card-a", "card-b", "card-c"}
	out := windowBlocks(blocks, -1, 100)
	if !strings.Contains(out, "card-a\n\ncard-b") {
		t.Fatalf("expected a blank line between cards, got:\n%q", out)
	}
	// No trailing separator after the last visible card.
	if strings.HasSuffix(out, "\n\n") {
		t.Fatalf("unexpected trailing blank line:\n%q", out)
	}
}

// TestWindowBlocksHonorsMaxLines keeps the windowing math correct now that a
// margin row is actually drawn: each card reserves block+margin lines.
func TestWindowBlocksHonorsMaxLines(t *testing.T) {
	blocks := []string{"a", "b", "c", "d"}
	// Each single-line block reserves 2 lines (block + margin). maxLines 4 fits
	// two cards.
	out := windowBlocks(blocks, -1, 4)
	if got := strings.Count(out, "\n\n") + 1; got != 2 {
		t.Fatalf("expected 2 cards to fit in 4 lines, got %d:\n%q", got, out)
	}
}
