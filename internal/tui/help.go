package tui

import (
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/viewport"

	"github.com/scaccogatto/nastro/internal/transcribe"
)

// helpSection is one screen's worth of keybindings, for the help overlay.
type helpSection struct {
	title string
	keys  []string
}

// helpSections lists every screen's keybindings, kept in sync with
// footerFor: this is the full list (not the possibly-compact footer line).
var helpSections = []helpSection{
	{"list", []string{"r rec", "enter detail", "t transcribe", "o finder", "d delete", "/ filter", "? help", "q/ctrl+c quit"}},
	{"detail", []string{"t transcribe", "o finder", "d delete", "esc list"}},
	{"recording", []string{"s stop & save", "q stop & save", "x discard", "ctrl+c stop & save"}},
	{"name form", []string{"enter confirm", "tab mode", "esc/ctrl+c cancel"}},
	{"downloading model", []string{"esc cancel download", "ctrl+c cancel download"}},
	{"transcribing", []string{"esc/ctrl+c back to list (job keeps running)", "c cancel"}},
	{"download confirm", []string{"y download", "q/any other key cancel"}},
	{"message screens (missing prereqs, errors)", []string{"any key continue"}},
}

// helpContent renders the help overlay's full text: every screen's
// keybindings, useful paths, and the crash-survival guarantee. Fed into
// helpViewport (see enterHelp) rather than shown directly, so it scrolls
// instead of overflowing short terminals.
func helpContent(m Model) string {
	width := m.contentWidth()
	var lines []string
	lines = append(lines, accentStyle.Render("keybindings"), "")
	for _, sec := range helpSections {
		lines = append(lines, sec.title+":")
		for _, k := range sec.keys {
			lines = append(lines, "  "+k)
		}
		lines = append(lines, "")
	}

	lines = append(lines, accentStyle.Render("paths"), "")
	lines = append(lines,
		"  output dir: "+abbreviateHome(m.cfg.OutputDir, m.homeDir),
		"  config: "+abbreviateHome(filepath.Join(m.homeDir, ".config", "nastro", "config.toml"), m.homeDir),
		"  transcriber: "+m.cfg.Transcriber,
	)
	if m.cfg.Transcriber != "whisperx" {
		lines = append(lines, "  whisper model: "+abbreviateHome(transcribe.ModelPath(m.homeDir, m.cfg.WhisperModel), m.homeDir))
	}
	lines = append(lines, "")

	lines = append(lines, "recordings survive crashes: audio is written incrementally")

	return strings.Join(wrapLines(lines, width), "\n")
}

// helpFooter renders the help overlay's footer, appending a "more below"
// hint when vp hasn't scrolled all the way to the bottom.
func helpFooter(vp viewport.Model) string {
	base := "↑/↓ scroll · any other key close"
	if vp.AtBottom() {
		return base
	}
	return base + " · ↓ more"
}
