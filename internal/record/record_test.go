package record

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"Cliente EPPI", "cliente-eppi"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"already-slug", "already-slug"},
		{"weird!!chars??here", "weird-chars-here"},
		{"multiple   spaces", "multiple-spaces"},
		{"àccènti", "accenti"},
		{"", ""},
		{"---", ""},
	}

	for _, tt := range tests {
		got := Slugify(tt.name)
		if got != tt.want {
			t.Errorf("Slugify(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestBuildDirName(t *testing.T) {
	now := time.Date(2026, 8, 6, 14, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		slug string
		want string
	}{
		{"with slug", "cliente-eppi", "2026-08-06-1430-cliente-eppi"},
		{"no slug", "", "2026-08-06-1430"},
	}

	for _, tt := range tests {
		got := BuildDirName(now, tt.slug)
		if got != tt.want {
			t.Errorf("BuildDirName(%v, %q) = %q, want %q", now, tt.slug, got, tt.want)
		}
	}
}

func TestShouldDiscard(t *testing.T) {
	tests := []struct {
		elapsed time.Duration
		want    bool
	}{
		{0, true},
		{1 * time.Second, true},
		{1900 * time.Millisecond, true},
		{2 * time.Second, false},
		{5 * time.Second, false},
	}

	for _, tt := range tests {
		got := ShouldDiscard(tt.elapsed)
		if got != tt.want {
			t.Errorf("ShouldDiscard(%v) = %v, want %v", tt.elapsed, got, tt.want)
		}
	}
}

func TestLockPath(t *testing.T) {
	got := LockPath("/some/dir")
	want := filepath.Join("/some/dir", ".lock")
	if got != want {
		t.Errorf("LockPath() = %q, want %q", got, want)
	}
}

func TestAcquireLockRelease(t *testing.T) {
	dir := t.TempDir()

	f, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock() first call error: %v", err)
	}

	_, err = AcquireLock(dir)
	if err == nil {
		t.Fatalf("AcquireLock() second call error = nil, want error (already locked)")
	}

	if err := ReleaseLock(f, dir); err != nil {
		t.Fatalf("ReleaseLock() error: %v", err)
	}

	f2, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock() after release error: %v", err)
	}
	_ = ReleaseLock(f2, dir)
}

func TestAcquireLockErrorIsDescriptive(t *testing.T) {
	dir := t.TempDir()
	f, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock() error: %v", err)
	}
	defer ReleaseLock(f, dir)

	_, err = AcquireLock(dir)
	if !errors.Is(err, ErrAlreadyLocked) {
		t.Errorf("AcquireLock() second call error = %v, want wrapping ErrAlreadyLocked", err)
	}
}
