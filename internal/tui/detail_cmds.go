package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

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

// deletedMsg is delivered once a record's directory has been dealt with:
// moved to Trash (the common case), permanently removed as a fallback (when
// the move itself failed), or neither (err set, nothing changed on disk).
type deletedMsg struct {
	id       string
	fellBack bool  // true if the move failed and a permanent RemoveAll was used instead
	err      error // set if deletion failed outright (both the move and the fallback remove failed)
}

// deleteStatus renders msg as the list/detail status line and its severity.
// Pure over deletedMsg so the wording is independently testable.
func deleteStatus(msg deletedMsg) (status string, isErr bool) {
	switch {
	case msg.err != nil:
		return "error deleting " + msg.id + ": " + msg.err.Error(), true
	case msg.fellBack:
		return "trash unavailable, deleted permanently: " + msg.id, true
	default:
		return "moved to Trash: " + msg.id, false
	}
}

// deleteCmd moves rec's directory to trashDir (recoverable), falling back to
// permanently removing it only if the move itself fails (e.g. no home
// directory, or Trash unwritable).
func deleteCmd(rec records.Record, outputDir, trashDir string) tea.Cmd {
	return func() tea.Msg {
		src := filepath.Join(outputDir, rec.ID)
		if _, err := moveToTrash(src, trashDir); err == nil {
			return deletedMsg{id: rec.ID}
		}
		rmErr := os.RemoveAll(src)
		if rmErr != nil {
			return deletedMsg{id: rec.ID, err: rmErr}
		}
		return deletedMsg{id: rec.ID, fellBack: true}
	}
}

// trashDestPath computes where src's basename (base) should land inside
// trashDir: the plain name, or -- if that's already taken (per exists, so
// this stays testable without touching a real filesystem) -- the name
// suffixed with now's timestamp, with a numeric tie-breaker in the
// vanishingly unlikely case even that collides.
func trashDestPath(trashDir, base string, now time.Time, exists func(string) bool) string {
	dest := filepath.Join(trashDir, base)
	if !exists(dest) {
		return dest
	}

	stamped := base + "-" + now.Format("20060102-150405")
	dest = filepath.Join(trashDir, stamped)
	for i := 2; exists(dest); i++ {
		dest = filepath.Join(trashDir, fmt.Sprintf("%s-%d", stamped, i))
	}
	return dest
}

// pathExists reports whether p already exists on disk (any type), the
// exists probe trashDestPath needs at the call site.
func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// moveToTrash moves src into trashDir, computing a collision-safe
// destination via trashDestPath. Falls back to copy-then-remove for the rare
// cross-device case a plain os.Rename can't handle. Returns an error (never
// removing src) if trashDir is empty, can't be created, or neither move
// strategy works -- callers decide what to do next (deleteCmd falls back to
// a permanent RemoveAll).
func moveToTrash(src, trashDir string) (dest string, err error) {
	if trashDir == "" {
		return "", fmt.Errorf("no Trash directory (home directory unresolved)")
	}
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		return "", fmt.Errorf("create Trash dir: %w", err)
	}

	dest = trashDestPath(trashDir, filepath.Base(src), time.Now(), pathExists)
	if err := os.Rename(src, dest); err == nil {
		return dest, nil
	}

	// Cross-device (or other rename failure): copy then remove the original.
	if err := os.CopyFS(dest, os.DirFS(src)); err != nil {
		return "", fmt.Errorf("copy to Trash: %w", err)
	}
	if err := os.RemoveAll(src); err != nil {
		return "", fmt.Errorf("remove original after copy to Trash: %w", err)
	}
	return dest, nil
}
