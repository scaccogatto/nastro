package tui

import (
	"path/filepath"
	"strings"

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
	{"recording", []string{"s stop & save", "x discard", "ctrl+c stop & save"}},
	{"name form", []string{"enter confirm", "tab mode", "esc cancel"}},
	{"downloading model", []string{"esc cancel download"}},
	{"transcribing", []string{"esc cancel", "ctrl+c cancel"}},
	{"message screens (missing whisper-cli, errors)", []string{"any key continue"}},
}

// helpView renders the help overlay: every screen's keybindings, useful
// paths, and the crash-survival guarantee.
func (m Model) helpView() string {
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
		"  whisper model: "+abbreviateHome(transcribe.ModelPath(m.homeDir, m.cfg.WhisperModel), m.homeDir),
		"",
	)

	lines = append(lines, "recordings survive crashes: audio is written incrementally")

	return strings.Join(wrapLines(lines, width), "\n")
}
