package tui

import (
	"path/filepath"
	"strings"
)

// captureMode is the audio source chosen on the name form, before starting a
// recording: mixed (system + mic, the default), mic-only, or system-only.
type captureMode int

const (
	captureMixed captureMode = iota
	captureMicOnly
	captureSystemOnly
)

// nextCaptureMode cycles mixed → mic-only → system-only → mixed, driven by
// tab on the name form.
func nextCaptureMode(m captureMode) captureMode {
	return (m + 1) % 3
}

// captureModeLabel renders m for the name form's mode row.
func captureModeLabel(m captureMode) string {
	switch m {
	case captureMicOnly:
		return "mic-only"
	case captureSystemOnly:
		return "system-only"
	default:
		return "mixed"
	}
}

// captureModeOptions converts m into the MicOnly/SystemOnly flags
// record.Options expects.
func captureModeOptions(m captureMode) (micOnly, systemOnly bool) {
	switch m {
	case captureMicOnly:
		return true, false
	case captureSystemOnly:
		return false, true
	default:
		return false, false
	}
}

// abbreviateHome replaces a leading homeDir in path with "~", for compact,
// recognizable display of paths under the user's home directory. Pure over
// homeDir (rather than calling os.UserHomeDir itself) so it's testable
// without touching the environment; homeDir == "" (unresolved) leaves path
// unchanged.
func abbreviateHome(path, homeDir string) string {
	if homeDir == "" {
		return path
	}
	if path == homeDir {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, homeDir+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}

// nameFormView renders the name form: the input, the capture mode row (G4),
// and where the recording will land (G6) -- so the user sees both before
// committing.
func (m Model) nameFormView() string {
	lines := []string{
		"new recording",
		"",
		m.nameInput.View(),
		footerStyle.Render("mode: " + captureModeLabel(m.nameFormMode) + " ▸ (tab to change)"),
		footerStyle.Render("→ " + abbreviateHome(m.cfg.OutputDir, m.homeDir)),
	}
	return strings.Join(lines, "\n")
}
