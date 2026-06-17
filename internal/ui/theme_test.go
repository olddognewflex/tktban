package ui

import "testing"

// All four catppuccin flavors are registered and resolvable.
func TestCatppuccinThemesRegistered(t *testing.T) {
	for _, name := range []string{
		"catppuccin-latte", "catppuccin-frappe", "catppuccin-macchiato", "catppuccin-mocha",
	} {
		th, ok := themeByName(name)
		if !ok {
			t.Fatalf("theme %q not registered", name)
		}
		if th.surface == "" || th.fg == "" || th.primary == "" {
			t.Fatalf("theme %q has empty palette fields: %+v", name, th)
		}
	}
}

// isLight classifies flavors by surface luminance so derived light/dark styles
// (e.g. glamour) pick correctly without hardcoding names.
func TestThemeIsLight(t *testing.T) {
	cases := map[string]bool{
		"catppuccin-latte":     true,
		"catppuccin-mocha":     false,
		"catppuccin-frappe":    false,
		"catppuccin-macchiato": false,
		"textual-light":        true,
		"textual-dark":         false,
	}
	for name, want := range cases {
		th, ok := themeByName(name)
		if !ok {
			t.Fatalf("theme %q not registered", name)
		}
		if got := th.isLight(); got != want {
			t.Fatalf("%s isLight() = %v, want %v", name, got, want)
		}
		if want && glamourStyle(th) != "light" {
			t.Fatalf("%s should map to glamour 'light'", name)
		}
		if !want && glamourStyle(th) != "dark" {
			t.Fatalf("%s should map to glamour 'dark'", name)
		}
	}
}

// nextTheme cycles through every registered theme, including the catppuccin set,
// and returns to the start.
func TestNextThemeCyclesAll(t *testing.T) {
	seen := map[string]bool{}
	cur := themes[0].name
	for range themes {
		seen[cur] = true
		cur = nextTheme(cur).name
	}
	if cur != themes[0].name {
		t.Fatalf("cycle did not return to start: ended at %q", cur)
	}
	for _, want := range []string{"catppuccin-latte", "catppuccin-frappe", "catppuccin-macchiato", "catppuccin-mocha"} {
		if !seen[want] {
			t.Fatalf("cycle skipped %q", want)
		}
	}
}
