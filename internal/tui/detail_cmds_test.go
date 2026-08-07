package tui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scaccogatto/nastro/internal/records"
)

func TestTrashDestPathNoCollisionUsesPlainName(t *testing.T) {
	got := trashDestPath("/Users/x/.Trash", "2026-08-06-1430-standup", time.Time{}, func(string) bool { return false })
	want := "/Users/x/.Trash/2026-08-06-1430-standup"
	if got != want {
		t.Errorf("trashDestPath() = %q, want %q", got, want)
	}
}

func TestTrashDestPathCollisionAppendsTimestamp(t *testing.T) {
	now := time.Date(2026, 8, 7, 14, 30, 22, 0, time.UTC)
	taken := map[string]bool{
		filepath.Join("/Users/x/.Trash", "standup"): true,
	}
	got := trashDestPath("/Users/x/.Trash", "standup", now, func(p string) bool { return taken[p] })
	want := filepath.Join("/Users/x/.Trash", "standup-20260807-143022")
	if got != want {
		t.Errorf("trashDestPath() = %q, want %q", got, want)
	}
}

func TestTrashDestPathDoubleCollisionAppendsCounter(t *testing.T) {
	now := time.Date(2026, 8, 7, 14, 30, 22, 0, time.UTC)
	stamped := filepath.Join("/Users/x/.Trash", "standup-20260807-143022")
	taken := map[string]bool{
		filepath.Join("/Users/x/.Trash", "standup"): true,
		stamped: true,
	}
	got := trashDestPath("/Users/x/.Trash", "standup", now, func(p string) bool { return taken[p] })
	want := stamped + "-2"
	if got != want {
		t.Errorf("trashDestPath() = %q, want %q", got, want)
	}
}

func TestDeleteStatus(t *testing.T) {
	tests := []struct {
		name      string
		msg       deletedMsg
		wantErr   bool
		wantMatch string
	}{
		{"moved to trash", deletedMsg{id: "x"}, false, "moved to Trash: x"},
		{"fell back to remove", deletedMsg{id: "x", fellBack: true}, true, "trash unavailable, deleted permanently: x"},
		{"error", deletedMsg{id: "x", err: errors.New("permission denied")}, true, "error deleting x: permission denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, isErr := deleteStatus(tt.msg)
			if status != tt.wantMatch {
				t.Errorf("deleteStatus() status = %q, want %q", status, tt.wantMatch)
			}
			if isErr != tt.wantErr {
				t.Errorf("deleteStatus() isErr = %v, want %v", isErr, tt.wantErr)
			}
		})
	}
}

func TestMoveToTrashMovesDirAndReturnsDest(t *testing.T) {
	outputDir := t.TempDir()
	trash := t.TempDir()
	src := filepath.Join(outputDir, "2026-08-06-1430-standup")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "audio.m4a"), []byte("fake-audio"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dest, err := moveToTrash(src, trash)
	if err != nil {
		t.Fatalf("moveToTrash() error = %v", err)
	}
	if dest != filepath.Join(trash, "2026-08-06-1430-standup") {
		t.Errorf("moveToTrash() dest = %q", dest)
	}
	if _, statErr := os.Stat(src); !os.IsNotExist(statErr) {
		t.Errorf("source dir still exists after move")
	}
	got, err := os.ReadFile(filepath.Join(dest, "audio.m4a"))
	if err != nil {
		t.Fatalf("read moved file: %v", err)
	}
	if string(got) != "fake-audio" {
		t.Errorf("moved content = %q, want %q", got, "fake-audio")
	}
}

func TestMoveToTrashCollisionGetsUniqueName(t *testing.T) {
	outputDir := t.TempDir()
	trash := t.TempDir()
	src := filepath.Join(outputDir, "standup")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(trash, "standup"), 0o755); err != nil {
		t.Fatalf("MkdirAll existing trash entry: %v", err)
	}

	dest, err := moveToTrash(src, trash)
	if err != nil {
		t.Fatalf("moveToTrash() error = %v", err)
	}
	if dest == filepath.Join(trash, "standup") {
		t.Errorf("moveToTrash() dest collided with existing entry: %q", dest)
	}
	if _, statErr := os.Stat(dest); statErr != nil {
		t.Errorf("moved dir not found at %q: %v", dest, statErr)
	}
}

func TestMoveToTrashEmptyTrashDirErrors(t *testing.T) {
	src := t.TempDir()
	if _, err := moveToTrash(src, ""); err == nil {
		t.Errorf("moveToTrash() with empty trashDir error = nil, want error")
	}
	if _, statErr := os.Stat(src); statErr != nil {
		t.Errorf("moveToTrash() with empty trashDir must not touch src, but: %v", statErr)
	}
}

func TestDeleteCmdMovesToTrash(t *testing.T) {
	outputDir := t.TempDir()
	trash := t.TempDir()
	src := filepath.Join(outputDir, "2026-08-06-1430-standup")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cmd := deleteCmd(records.Record{ID: "2026-08-06-1430-standup"}, outputDir, trash)
	msg, ok := cmd().(deletedMsg)
	if !ok {
		t.Fatalf("deleteCmd() = %T, want deletedMsg", cmd())
	}
	if msg.err != nil {
		t.Errorf("deletedMsg.err = %v, want nil", msg.err)
	}
	if msg.fellBack {
		t.Errorf("deletedMsg.fellBack = true, want false (Trash was available)")
	}
	if _, statErr := os.Stat(src); !os.IsNotExist(statErr) {
		t.Errorf("source dir still exists after deleteCmd")
	}
	if _, statErr := os.Stat(filepath.Join(trash, "2026-08-06-1430-standup")); statErr != nil {
		t.Errorf("record dir not found in trash: %v", statErr)
	}
}

func TestDeleteCmdFallsBackToRemoveWhenTrashUnavailable(t *testing.T) {
	outputDir := t.TempDir()
	src := filepath.Join(outputDir, "2026-08-06-1430-standup")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cmd := deleteCmd(records.Record{ID: "2026-08-06-1430-standup"}, outputDir, "")
	msg, ok := cmd().(deletedMsg)
	if !ok {
		t.Fatalf("deleteCmd() = %T, want deletedMsg", cmd())
	}
	if msg.err != nil {
		t.Errorf("deletedMsg.err = %v, want nil (fallback remove should succeed)", msg.err)
	}
	if !msg.fellBack {
		t.Errorf("deletedMsg.fellBack = false, want true (Trash unavailable)")
	}
	if _, statErr := os.Stat(src); !os.IsNotExist(statErr) {
		t.Errorf("source dir still exists after fallback delete")
	}
}
