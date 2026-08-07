package tui

import (
	"os"
	"os/exec"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"github.com/scaccogatto/nastro/internal/records"
)

// detailLoadedMsg carries a detail screen's on-disk data: its absolute
// record dir path, and (if it has one) a preview of its transcript.
type detailLoadedMsg struct {
	path    string
	preview []string
}

// loadDetailCmd reads rec's dir path and, if it has a transcript, a preview
// of its first lines.
func loadDetailCmd(rec records.Record, outputDir string) tea.Cmd {
	return func() tea.Msg {
		dir := filepath.Join(outputDir, rec.ID)
		var preview []string
		if rec.HasTranscript {
			if b, err := os.ReadFile(filepath.Join(dir, "transcript.txt")); err == nil {
				preview = records.TranscriptPreview(string(b), 15)
			}
		}
		return detailLoadedMsg{path: dir, preview: preview}
	}
}

// finderOpenedMsg is delivered once `open <dir>` has been spawned (or failed
// to spawn).
type finderOpenedMsg struct{ err error }

// openFinderCmd reveals dir in Finder.
func openFinderCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		return finderOpenedMsg{err: exec.Command("open", dir).Run()}
	}
}

// deletedMsg is delivered once a record's directory has been removed (or
// failed to be).
type deletedMsg struct {
	id  string
	err error
}

// deleteCmd removes rec's directory entirely.
func deleteCmd(rec records.Record, outputDir string) tea.Cmd {
	return func() tea.Msg {
		err := os.RemoveAll(filepath.Join(outputDir, rec.ID))
		return deletedMsg{id: rec.ID, err: err}
	}
}
