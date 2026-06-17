package ui

import (
	"sort"

	"github.com/charmbracelet/lipgloss"
)

// theme is a named color palette. The Python relied on Textual's theme registry;
// Bubble Tea/lipgloss has none, so tktban ships its own small set. "textual-dark"
// is kept as the default name so a settings.toml written by the Python version
// still resolves on load.
type theme struct {
	name    string
	primary lipgloss.Color // borders, column titles
	accent  lipgloss.Color // selected card, meta line
	muted   lipgloss.Color // card summary
	surface lipgloss.Color // dialog background
	boost   lipgloss.Color // title background
	errc    lipgloss.Color // error text
	fg      lipgloss.Color // default text
}

// themes is the registry, in display (name-sorted) order to match the Python's
// `sorted(available_themes)` cycle.
var themes = []theme{
	{"dracula", "#BD93F9", "#8BE9FD", "#6272A4", "#282A36", "#44475A", "#FF5555", "#F8F8F2"},
	{"gruvbox", "#83A598", "#8EC07C", "#928374", "#282828", "#3C3836", "#FB4934", "#EBDBB2"},
	{"nord", "#81A1C1", "#88C0D0", "#7B88A1", "#2E3440", "#3B4252", "#BF616A", "#ECEFF4"},
	{"textual-dark", "#5A9BD5", "#2EC4B6", "#8A8A8A", "#1E1E2E", "#313244", "#F38BA8", "#CDD6F4"},
	{"textual-light", "#1E66F5", "#179299", "#6C6F85", "#EFF1F5", "#CCD0DA", "#D20F39", "#4C4F69"},
}

func init() {
	sort.Slice(themes, func(i, j int) bool { return themes[i].name < themes[j].name })
}

// themeByName returns the theme with the given name and whether it was found.
func themeByName(name string) (theme, bool) {
	for _, t := range themes {
		if t.name == name {
			return t, true
		}
	}
	return theme{}, false
}

// nextTheme returns the theme cyclically after the named one.
func nextTheme(current string) theme {
	idx := -1
	for i, t := range themes {
		if t.name == current {
			idx = i
			break
		}
	}
	return themes[(idx+1)%len(themes)]
}

// styles bundles the lipgloss styles derived from a theme, rebuilt whenever the
// theme changes.
type styles struct {
	t theme

	column        lipgloss.Style
	columnFocused lipgloss.Style
	colTitle      lipgloss.Style
	card          lipgloss.Style
	cardSelected  lipgloss.Style
	cardHead      lipgloss.Style
	cardSummary   lipgloss.Style
	cardMeta      lipgloss.Style
	dialog        lipgloss.Style
	dialogTitle   lipgloss.Style
	fieldLabel    lipgloss.Style
	errorText     lipgloss.Style
	statusBar     lipgloss.Style
	statusErr     lipgloss.Style
	statusWarn    lipgloss.Style
	header        lipgloss.Style
	footer        lipgloss.Style
}

func newStyles(t theme) styles {
	return styles{
		t:             t,
		column:        lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.muted).Padding(0, 1),
		columnFocused: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.primary).Padding(0, 1),
		colTitle:      lipgloss.NewStyle().Bold(true).Foreground(t.fg).Background(t.boost),
		// Cards carry a faint full outline (muted) that brightens to the accent
		// colour when selected — TKB-17. Rounded matches the column frame.
		card:         lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.muted).Padding(0, 1),
		cardSelected: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.accent).Padding(0, 1),
		cardHead:     lipgloss.NewStyle().Bold(true).Foreground(t.fg),
		cardSummary:  lipgloss.NewStyle().Foreground(t.muted),
		cardMeta:     lipgloss.NewStyle().Foreground(t.accent),
		dialog:       lipgloss.NewStyle().Border(lipgloss.ThickBorder()).BorderForeground(t.primary).Background(t.surface).Padding(1, 2),
		dialogTitle:  lipgloss.NewStyle().Bold(true).Foreground(t.fg).MarginBottom(1),
		fieldLabel:   lipgloss.NewStyle().Foreground(t.muted),
		errorText:    lipgloss.NewStyle().Bold(true).Foreground(t.errc),
		statusBar:    lipgloss.NewStyle().Foreground(t.muted),
		statusErr:    lipgloss.NewStyle().Bold(true).Foreground(t.errc),
		statusWarn:   lipgloss.NewStyle().Foreground(t.accent),
		header:       lipgloss.NewStyle().Bold(true).Foreground(t.fg).Background(t.boost).Padding(0, 1),
		footer:       lipgloss.NewStyle().Foreground(t.muted),
	}
}
