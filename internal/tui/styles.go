package tui

import (
	"charm.land/bubbles/v2/list"
	"charm.land/lipgloss/v2"
)

// Palette: ANSI base 0-15 only, so the TUI inherits whatever theme the
// user's terminal is configured with (e.g. Catppuccin light/dark in
// Ghostty) instead of carrying its own hardcoded colors.
var (
	colorWarn   = lipgloss.Color("1") // red: destructive/error
	colorAccent = lipgloss.Color("6") // cyan: selection, emphasis
	colorMuted  = lipgloss.Color("8") // bright black: secondary chrome
)

var (
	appStyle    = lipgloss.NewStyle().Margin(1, 2)
	recDotStyle = lipgloss.NewStyle().Foreground(colorWarn).Bold(true)
	errStyle    = lipgloss.NewStyle().Foreground(colorWarn).Bold(true)
	accentStyle = lipgloss.NewStyle().Foreground(colorAccent)
	footerStyle = lipgloss.NewStyle().Foreground(colorMuted)
	selectedRow = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	normalRow   = lipgloss.NewStyle()
)

// bodyStyle word-wraps body text (error messages, transcript previews,
// whisper-cli output) to width, rather than letting it overflow the
// terminal.
func bodyStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().Width(width)
}

// themedListStyles overrides bubbles' list.DefaultStyles chrome (the violet
// title badge and pink/green filter accents) with the shared ANSI palette,
// so the list matches the rest of the TUI instead of carrying its own
// distinct color family.
func themedListStyles() list.Styles {
	s := list.DefaultStyles(true)
	s.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, 0)
	s.Title = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	s.StatusBar = lipgloss.NewStyle().Foreground(colorMuted).Padding(0, 0, 1, 0)
	s.StatusEmpty = lipgloss.NewStyle().Foreground(colorMuted)
	s.StatusBarActiveFilter = lipgloss.NewStyle().Foreground(colorAccent)
	s.NoItems = lipgloss.NewStyle().Foreground(colorMuted)
	s.DefaultFilterCharacterMatch = lipgloss.NewStyle().Underline(true).Foreground(colorAccent)
	s.Filter.Focused.Prompt = lipgloss.NewStyle().Foreground(colorAccent)
	s.Filter.Blurred.Prompt = lipgloss.NewStyle().Foreground(colorMuted)
	return s
}
