package record

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

func TestAcquireLockWritesCurrentPID(t *testing.T) {
	dir := t.TempDir()
	f, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock() error: %v", err)
	}
	defer ReleaseLock(f, dir)

	content, err := os.ReadFile(LockPath(dir))
	if err != nil {
		t.Fatalf("read lockfile: %v", err)
	}
	if got, want := string(content), strconv.Itoa(os.Getpid()); got != want {
		t.Errorf("lockfile content = %q, want current pid %q", got, want)
	}
}

func TestLockState(t *testing.T) {
	alive := func(int) bool { return true }
	dead := func(int) bool { return false }

	tests := []struct {
		name    string
		content string
		isAlive func(int) bool
		want    lockDecision
	}{
		{"valid pid, alive", "12345", alive, lockAlive},
		{"valid pid, dead", "12345", dead, lockStale},
		{"corrupted content", "not-a-pid", alive, lockStale},
		{"empty content", "", alive, lockStale},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lockState([]byte(tt.content), tt.isAlive)
			if got != tt.want {
				t.Errorf("lockState(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestAcquireLockStaleRetry(t *testing.T) {
	dir := t.TempDir()

	// A short-lived process, already exited by the time we use its pid:
	// a real pid that's guaranteed dead, without guessing an unused number.
	deadCmd := exec.Command("true")
	if err := deadCmd.Run(); err != nil {
		t.Fatalf("spawn short-lived process: %v", err)
	}
	deadPID := deadCmd.Process.Pid

	if err := os.WriteFile(LockPath(dir), []byte(strconv.Itoa(deadPID)), 0o644); err != nil {
		t.Fatalf("seed stale lock: %v", err)
	}

	f, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock() with stale lock error: %v", err)
	}
	defer ReleaseLock(f, dir)

	content, err := os.ReadFile(LockPath(dir))
	if err != nil {
		t.Fatalf("read lockfile: %v", err)
	}
	if got, want := string(content), strconv.Itoa(os.Getpid()); got != want {
		t.Errorf("lockfile content after stale retry = %q, want current pid %q", got, want)
	}
}

func TestAcquireLockCorruptContentRetry(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(LockPath(dir), []byte("garbage"), 0o644); err != nil {
		t.Fatalf("seed corrupt lock: %v", err)
	}

	f, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock() with corrupt lock error: %v", err)
	}
	_ = ReleaseLock(f, dir)
}

func TestParseLevelLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Level
		ok   bool
	}{
		{"mixed", "level sys=0.42 mic=0.10", Level{System: 0.42, HasSystem: true, Mic: 0.10, HasMic: true}, true},
		{"system only", "level sys=1.00", Level{System: 1.00, HasSystem: true}, true},
		{"mic only", "level mic=0.00", Level{Mic: 0.00, HasMic: true}, true},
		{"unrelated line", "some other tap output", Level{}, false},
		{"blank line", "", Level{}, false},
		{"wrong prefix", "levels sys=0.5", Level{}, false},
		{"malformed value ignored, rest parses", "level sys=nope mic=0.3", Level{Mic: 0.3, HasMic: true}, true},
		{"no keys at all", "level", Level{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseLevelLine(tt.line)
			if ok != tt.ok {
				t.Fatalf("parseLevelLine(%q) ok = %v, want %v", tt.line, ok, tt.ok)
			}
			if !ok {
				return
			}
			if got != tt.want {
				t.Errorf("parseLevelLine(%q) = %+v, want %+v", tt.line, got, tt.want)
			}
		})
	}
}

func TestDiskSpaceWarning(t *testing.T) {
	tests := []struct {
		name        string
		availBlocks uint64
		blockSize   uint64
		wantLow     bool
	}{
		{"plenty free", 10 * (1 << 30) / 4096, 4096, false},
		{"exactly 1GB free", (1 << 30) / 4096, 4096, false},
		{"just under 1GB", (1<<30)/4096 - 1, 4096, true},
		{"nearly empty", 100, 4096, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			low, msg := diskSpaceWarning("/some/dir", tt.availBlocks, tt.blockSize)
			if low != tt.wantLow {
				t.Errorf("diskSpaceWarning() low = %v, want %v", low, tt.wantLow)
			}
			if low && !strings.Contains(msg, "/some/dir") {
				t.Errorf("diskSpaceWarning() msg = %q, want it to mention the dir", msg)
			}
			if !low && msg != "" {
				t.Errorf("diskSpaceWarning() msg = %q, want empty when not low", msg)
			}
		})
	}
}

func TestDescribeTapExit(t *testing.T) {
	exitErrWithCode := func(t *testing.T, code int) error {
		t.Helper()
		cmd := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code))
		return cmd.Run()
	}

	t.Run("exit code 2 is a TCC permission error", func(t *testing.T) {
		err := DescribeTapExit(exitErrWithCode(t, 2), "some stderr output")
		if err == nil {
			t.Fatalf("DescribeTapExit() = nil, want error")
		}
		if !strings.Contains(err.Error(), "permission") {
			t.Errorf("DescribeTapExit() = %q, want mention of permission", err.Error())
		}
		if !strings.Contains(err.Error(), "some stderr output") {
			t.Errorf("DescribeTapExit() = %q, want it to include stderr", err.Error())
		}
	})

	t.Run("other exit codes are generic failures", func(t *testing.T) {
		err := DescribeTapExit(exitErrWithCode(t, 1), "boom")
		if err == nil {
			t.Fatalf("DescribeTapExit() = nil, want error")
		}
		if strings.Contains(err.Error(), "permission") {
			t.Errorf("DescribeTapExit() = %q, want no permission mention for exit code 1", err.Error())
		}
		if !strings.Contains(err.Error(), "boom") {
			t.Errorf("DescribeTapExit() = %q, want it to include stderr", err.Error())
		}
	})
}
