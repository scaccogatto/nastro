package tui

import (
	"github.com/charmbracelet/x/ansi"
)

// clampWidth truncates s to at most width printable columns, appending an
// ellipsis when it doesn't fit. Used for single-line chrome (footer, status
// strip) that must never wrap or overflow the terminal.
func clampWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

// truncateMiddle shortens s to at most width runes by cutting out its
// middle and inserting an ellipsis, keeping both ends legible. Used for
// filesystem paths, where the interesting parts are usually the start
// (which directory) and the end (which record).
func truncateMiddle(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	keep := width - 1
	head := keep / 2
	tail := keep - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// truncatedPathLine composes label+path into a single line, truncating path
// from the middle so the whole line fits width, and wraps the (possibly
// truncated) displayed text in an OSC 8 hyperlink pointing at the full,
// untruncated path -- so a shortened path stays clickable.
func truncatedPathLine(label, path string, width int) string {
	avail := width - len([]rune(label))
	shown := truncateMiddle(path, avail)
	return label + hyperlink(shown, path)
}
