// Package record implements `nastro record`: naming, locking, and headless
// orchestration of the nastro-tap subprocess.
package record

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/records"
)

const lockFileName = ".lock"

// ErrAlreadyLocked marks an AcquireLock failure caused by an existing lock.
var ErrAlreadyLocked = errors.New("another recording is already running")

var slugCollapse = regexp.MustCompile("-+")

var accentFold = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u',
	'ñ': 'n', 'ç': 'c', 'ý': 'y', 'ÿ': 'y',
}

// Slugify turns arbitrary user input into a lowercase, dash-separated,
// filesystem-safe slug.
func Slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if repl, ok := accentFold[r]; ok {
			r = repl
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(slugCollapse.ReplaceAllString(b.String(), "-"), "-")
}

// BuildDirName formats a record directory name from a timestamp and an
// optional slug (already slugified). An empty slug yields a bare timestamp.
func BuildDirName(now time.Time, slug string) string {
	ts := now.Format("2006-01-02-1504")
	if slug == "" {
		return ts
	}
	return ts + "-" + slug
}

// ShouldDiscard reports whether a recording this short should be discarded
// rather than kept.
func ShouldDiscard(elapsed time.Duration) bool {
	return elapsed < 2*time.Second
}

// LockPath returns the lockfile path for outputDir.
func LockPath(outputDir string) string {
	return filepath.Join(outputDir, lockFileName)
}

// AcquireLock atomically creates the lockfile in outputDir, failing with
// ErrAlreadyLocked if one already exists. No staleness detection.
func AcquireLock(outputDir string) (*os.File, error) {
	path := LockPath(outputDir)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%w (lockfile at %s)", ErrAlreadyLocked, path)
		}
		return nil, err
	}
	return f, nil
}

// ReleaseLock closes and removes the lockfile.
func ReleaseLock(f *os.File, outputDir string) error {
	if err := f.Close(); err != nil {
		return err
	}
	return os.Remove(LockPath(outputDir))
}

// --- everything below is side-effecting orchestration: subprocess exec,
// signals, timers, disk stats. Deliberately outside TDD scope. ---

// Options holds the flags for `nastro record`.
type Options struct {
	Name       string
	MicOnly    bool
	SystemOnly bool
}

// Run executes a full headless recording session: lock, spawn nastro-tap,
// print a live timer, and finalize on SIGINT.
func Run(cfg config.Config, opts Options) error {
	if opts.MicOnly && opts.SystemOnly {
		return fmt.Errorf("--mic-only and --system-only are mutually exclusive")
	}

	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	warnLowDiskSpace(cfg.OutputDir, os.Stderr)

	lockFile, err := AcquireLock(cfg.OutputDir)
	if err != nil {
		return err
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if err := ReleaseLock(lockFile, cfg.OutputDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to release lockfile: %v\n", err)
		}
	}
	defer release()

	dirName := BuildDirName(time.Now(), Slugify(opts.Name))
	recordDir := filepath.Join(cfg.OutputDir, dirName)
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		return fmt.Errorf("create record dir: %w", err)
	}

	tapPath, err := resolveTapPath()
	if err != nil {
		return err
	}

	audioPath := filepath.Join(recordDir, "audio.m4a")
	args := []string{audioPath}
	switch {
	case opts.MicOnly:
		args = append(args, "--mic-only")
	case opts.SystemOnly:
		args = append(args, "--system-only")
	}

	var stderrBuf bytes.Buffer
	cmd := exec.Command(tapPath, args...)
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start nastro-tap: %w", err)
	}
	spawnCaffeinate(cmd.Process.Pid)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	start := time.Now()
	stopTimer := make(chan struct{})
	go printTimer(start, stopTimer)

	select {
	case <-sigCh:
		close(stopTimer)
		return finishOnInterrupt(cmd, waitErr, start, recordDir, release)

	case err := <-waitErr:
		close(stopTimer)
		release()
		return handleTapExit(err, stderrBuf.String())
	}
}

func finishOnInterrupt(cmd *exec.Cmd, waitErr chan error, start time.Time, recordDir string, release func()) error {
	_ = cmd.Process.Signal(os.Interrupt)

	select {
	case <-waitErr:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-waitErr
	}

	release()
	elapsed := time.Since(start)

	fmt.Println()
	if ShouldDiscard(elapsed) {
		if err := os.RemoveAll(recordDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to remove short recording: %v\n", err)
		}
		fmt.Println("recording discarded (shorter than 2s)")
		return nil
	}

	fmt.Println(recordDir)
	return nil
}

func handleTapExit(err error, stderr string) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
		return fmt.Errorf(
			"macOS needs to grant audio-capture permission to your terminal app.\n"+
				"Open System Settings > Privacy & Security > Screen & System Audio Recording,\n"+
				"enable it for your terminal, then re-run this command.\n\nnastro-tap said: %s",
			strings.TrimSpace(stderr))
	}
	return fmt.Errorf("nastro-tap exited unexpectedly: %w\nnastro-tap said: %s", err, strings.TrimSpace(stderr))
}

func printTimer(start time.Time, stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			fmt.Printf("\r%s", records.FormatDuration(now.Sub(start).Seconds()))
		}
	}
}

func warnLowDiskSpace(dir string, stderr *os.File) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return
	}
	const oneGB = 1 << 30
	free := stat.Bavail * uint64(stat.Bsize)
	if free < oneGB {
		fmt.Fprintf(stderr, "warning: less than 1GB free space in %s\n", dir)
	}
}

func spawnCaffeinate(tapPID int) {
	cmd := exec.Command("caffeinate", "-dims", "-w", strconv.Itoa(tapPID))
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to spawn caffeinate: %v\n", err)
	}
}

func resolveTapPath() (string, error) {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "nastro-tap")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if p, err := exec.LookPath("nastro-tap"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("nastro-tap not found: build it with `make build` or install nastro via your package manager once packaged")
}
