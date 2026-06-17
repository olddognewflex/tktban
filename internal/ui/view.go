package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/olddognewflex/tktban/internal/model"
)

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "loading…"
	}
	header := m.renderHeader()
	status := m.renderStatus()
	midHeight := max(m.height-2, 1) // header + status rows

	var mid string
	if m.modal != nil {
		mid = m.modal.View(m.styles, m.width, midHeight)
	} else {
		mid = m.renderBoard(midHeight)
	}
	return header + "\n" + mid + "\n" + status
}

func (m Model) renderHeader() string {
	title := "tktban"
	sub := m.subtitle()
	line := title
	if sub != "" {
		line += " — " + sub
	}
	return m.styles.header.Width(m.width).Render(line)
}

func (m Model) subtitle() string {
	if !m.loaded {
		return "loading…"
	}
	total := 0
	for _, c := range m.columns {
		total += len(c.Cards)
	}
	return fmt.Sprintf("%d tickets%s%s", total, m.filterLabel(), m.autoLabel())
}

func (m Model) filterLabel() string {
	var parts []string
	if m.filter.assignee != "" {
		parts = append(parts, "@"+m.filter.assignee)
	}
	if m.filter.prefix != "" {
		parts = append(parts, m.filter.prefix)
	}
	if len(parts) == 0 {
		return ""
	}
	return "  ·  filter: " + strings.Join(parts, ", ")
}

func (m Model) autoLabel() string {
	if m.autoOn {
		return fmt.Sprintf("  ·  auto %ds", int(m.refreshSecs))
	}
	return "  ·  auto off"
}

func (m Model) renderStatus() string {
	keys := "r refresh · a auto · t theme · f filter · v view · e edit · m move · c comment · n new · q quit"
	if m.status != "" {
		st := m.styles.statusBar
		switch m.statusKind {
		case "error":
			st = m.styles.statusErr
		case "warn":
			st = m.styles.statusWarn
		}
		return st.Width(m.width).Render(m.status)
	}
	return m.styles.footer.Width(m.width).Render(keys)
}

func (m Model) renderBoard(height int) string {
	if !m.loaded || len(m.columns) == 0 {
		return m.styles.statusBar.Render("(no columns — press r to refresh)")
	}
	n := len(m.columns)
	// Split the full terminal width across columns. lipgloss adds each column's
	// border (2 cols) outside its set width, while the padding sits inside it, so
	// only 2 cols of chrome are reserved per column. The width/n remainder is
	// handed out one col at a time to the leftmost columns so the row fills the
	// terminal with no dead space on the right.
	base := m.width / n
	extra := m.width % n
	inner := height - 2 // column border top/bottom

	rendered := make([]string, n)
	for i, col := range m.columns {
		outer := base
		if i < extra {
			outer++
		}
		colInner := max(outer-2, 8)
		rendered[i] = m.renderColumn(col, i, colInner, inner)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}

func (m Model) renderColumn(col model.Column, colIdx, innerWidth, innerHeight int) string {
	focused := colIdx == m.focusCol
	title := m.styles.colTitle.Width(innerWidth).Render(fmt.Sprintf("%s  (%d)", col.Lane, len(col.Cards)))

	selIdx := -1
	if focused && len(col.Cards) > 0 {
		selIdx = clamp(m.sel[col.Role], 0, len(col.Cards)-1)
	}

	blocks := make([]string, len(col.Cards))
	for i, c := range col.Cards {
		blocks[i] = m.renderCard(c, innerWidth, i == selIdx)
	}

	body := windowBlocks(blocks, selIdx, innerHeight-2) // title takes ~1 row
	content := title + "\n" + body

	style := m.styles.column
	if focused {
		style = m.styles.columnFocused
	}
	return style.Width(innerWidth).Height(innerHeight).Render(content)
}

func (m Model) renderCard(c model.Card, width int, selected bool) string {
	prio := ""
	if c.Priority != "" {
		prio = "[" + c.Priority + "] "
	}
	badge := ""
	if c.BlockerCount > 0 {
		badge = fmt.Sprintf("  ⛔%d", c.BlockerCount)
	}
	head := m.styles.cardHead.Render(prio + c.Key + badge)
	// Card text area = card width minus its border (2) and padding (2). The card
	// itself is rendered at width-4 (see below), so usable text is width-6.
	summary := m.styles.cardSummary.Render(truncate(c.Summary, width-6))
	lines := head + "\n" + summary

	var meta []string
	if c.Assignee != "" {
		meta = append(meta, "@"+c.Assignee)
	}
	if c.LaneHuman != "" {
		meta = append(meta, "⏱ "+c.LaneHuman)
	}
	if len(meta) > 0 {
		lines += "\n" + m.styles.cardMeta.Render(strings.Join(meta, "  "))
	}

	style := m.styles.card
	if selected {
		style = m.styles.cardSelected
	}
	// The card must fit inside the column's text area, which is the column's
	// inner width minus its own border+padding. lipgloss also adds the card's
	// border outside the set width, so the card box is width-4 — otherwise the
	// card's right border is clipped by the column and the outline fragments.
	return style.Width(width - 4).Render(lines)
}

// windowBlocks joins card blocks to fit maxLines, scrolling so the selected
// block stays visible (a simple start-offset that keeps the selection in view).
func windowBlocks(blocks []string, sel, maxLines int) string {
	if maxLines < 1 {
		maxLines = 1
	}
	if len(blocks) == 0 {
		return ""
	}
	start := 0
	for {
		lines, lastVisible := 0, start-1
		for i := start; i < len(blocks); i++ {
			h := strings.Count(blocks[i], "\n") + 1 // block lines
			if lines+h > maxLines {
				break
			}
			lines += h
			lastVisible = i
		}
		if sel < 0 || sel <= lastVisible || start >= len(blocks)-1 {
			var out []string
			for i := start; i <= lastVisible && i < len(blocks); i++ {
				out = append(out, blocks[i])
			}
			// Cards are joined directly: each card's full outline already
			// separates it from its neighbours, so no extra blank margin row is
			// needed (that was only required before cards had a border).
			return strings.Join(out, "\n")
		}
		start++ // selected card is below the fold; scroll down
	}
}

func truncate(s string, max int) string {
	if max < 1 {
		max = 1
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}
