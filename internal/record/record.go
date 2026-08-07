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

// lockDecision is the pure verdict on an existing lockfile's content.
type lockDecision int

const (
	// lockAlive means the lock's owner is still running: a real conflict.
	lockAlive lockDecision = iota
	// lockStale means the lock's owner is gone (or the content is
	// unreadable/corrupt): safe to remove and retry.
	lockStale
)

// lockState decides whether an existing lockfile's content is alive or
// stale, given an isAlive probe for a pid. Corrupt or empty content is
// always stale: it can't belong to a running process we can identify.
func lockState(content []byte, isAlive func(int) bool) lockDecision {
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		return lockStale
	}
	if isAlive(pid) {
		return lockAlive
	}
	return lockStale
}

// isProcessAlive reports whether pid names a running process, via a
// zero-signal kill probe. Treats "exists but not ours" (EPERM) as alive:
// only a definite ESRCH means it's actually gone.
func isProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || !errors.Is(err, syscall.ESRCH)
}

// tryAcquire atomically creates the lockfile in outputDir with the current
// pid as its content, failing with fs.ErrExist if one already exists.
func tryAcquire(outputDir string) (*os.File, error) {
	f, err := os.OpenFile(LockPath(outputDir), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(f, "%d", os.Getpid()); err != nil {
		f.Close()
		os.Remove(LockPath(outputDir))
		return nil, err
	}
	return f, nil
}

// AcquireLock atomically creates the lockfile in outputDir with the current
// pid as its content. If one already exists, it checks whether the owning
// pid is still alive: a stale lock (dead pid, or unreadable/corrupt
// content) is removed and acquisition is retried once. A live lock fails
// with ErrAlreadyLocked.
func AcquireLock(outputDir string) (*os.File, error) {
	f, err := tryAcquire(outputDir)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return nil, err
	}

	path := LockPath(outputDir)
	content, readErr := os.ReadFile(path)
	switch {
	case readErr == nil && lockState(content, isProcessAlive) == lockStale:
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return nil, rmErr
		}
	case readErr != nil && errors.Is(readErr, fs.ErrNotExist):
		// Released concurrently between our EEXIST and this read: just retry.
	default:
		return nil, fmt.Errorf("%w (lockfile at %s)", ErrAlreadyLocked, path)
	}

	f, err = tryAcquire(outputDir)
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
// signals, timers, disk stats. Deliberately outside TDD scope, except for
// pure helpers pulled out along the way (DescribeTapExit), which are. ---

// Options holds the flags for `nastro record`.
type Options struct {
	Name       string
	MicOnly    bool
	SystemOnly bool
}

// Session is a running nastro-tap subprocess plus everything needed to
// observe, stop, and finalize it. Shared by the headless `nastro record`
// CLI and the TUI recording screen so neither duplicates the
// lock/dir/spawn/caffeinate orchestration.
type Session struct {
	Cmd       *exec.Cmd
	RecordDir string
	AudioPath string
	OutputDir string
	Stderr    *bytes.Buffer
	Start     time.Time

	lockFile *os.File
	waitErr  chan error
	released bool
}

// Start validates opts, prepares the record dir, acquires the lock, and
// spawns nastro-tap plus caffeinate. The caller must eventually call
// Release (directly, or via Discard) to release the lock.
func Start(cfg config.Config, opts Options) (*Session, error) {
	if opts.MicOnly && opts.SystemOnly {
		return nil, fmt.Errorf("--mic-only and --system-only are mutually exclusive")
	}

	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}
	warnLowDiskSpace(cfg.OutputDir, os.Stderr)

	lockFile, err := AcquireLock(cfg.OutputDir)
	if err != nil {
		return nil, err
	}

	dirName := BuildDirName(time.Now(), Slugify(opts.Name))
	recordDir := filepath.Join(cfg.OutputDir, dirName)
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		_ = ReleaseLock(lockFile, cfg.OutputDir)
		return nil, fmt.Errorf("create record dir: %w", err)
	}

	tapPath, err := resolveTapPath()
	if err != nil {
		_ = ReleaseLock(lockFile, cfg.OutputDir)
		return nil, err
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
		_ = ReleaseLock(lockFile, cfg.OutputDir)
		return nil, fmt.Errorf("start nastro-tap: %w", err)
	}
	spawnCaffeinate(cmd.Process.Pid)

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	return &Session{
		Cmd:       cmd,
		RecordDir: recordDir,
		AudioPath: audioPath,
		OutputDir: cfg.OutputDir,
		Stderr:    &stderrBuf,
		Start:     time.Now(),
		lockFile:  lockFile,
		waitErr:   waitErr,
	}, nil
}

// Wait returns the channel nastro-tap's exit is delivered on, exactly once.
func (s *Session) Wait() <-chan error { return s.waitErr }

// Signal forwards an OS signal to nastro-tap.
func (s *Session) Signal(sig os.Signal) error {
	return s.Cmd.Process.Signal(sig)
}

// Kill force-stops nastro-tap.
func (s *Session) Kill() error {
	return s.Cmd.Process.Kill()
}

// Release releases the session's lock. Safe to call more than once.
func (s *Session) Release() {
	if s.released {
		return
	}
	s.released = true
	if err := ReleaseLock(s.lockFile, s.OutputDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to release lockfile: %v\n", err)
	}
}

// Discard removes RecordDir unconditionally (an explicit "quit without
// saving", or a too-short recording that isn't worth keeping).
func (s *Session) Discard() error {
	return os.RemoveAll(s.RecordDir)
}

// Run executes a full headless recording session: lock, spawn nastro-tap,
// print a live timer, and finalize on SIGINT.
func Run(cfg config.Config, opts Options) error {
	sess, err := Start(cfg, opts)
	if err != nil {
		return err
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	stopTimer := make(chan struct{})
	go printTimer(sess.Start, stopTimer)

	select {
	case <-sigCh:
		close(stopTimer)
		return finishOnInterrupt(sess)

	case err := <-sess.Wait():
		close(stopTimer)
		sess.Release()
		return DescribeTapExit(err, sess.Stderr.String())
	}
}

func finishOnInterrupt(sess *Session) error {
	_ = sess.Signal(os.Interrupt)

	select {
	case <-sess.Wait():
	case <-time.After(5 * time.Second):
		_ = sess.Kill()
		<-sess.Wait()
	}

	sess.Release()
	elapsed := time.Since(sess.Start)

	fmt.Println()
	if ShouldDiscard(elapsed) {
		if err := sess.Discard(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to remove short recording: %v\n", err)
		}
		fmt.Println("recording discarded (shorter than 2s)")
		return nil
	}

	fmt.Println(sess.RecordDir)
	return nil
}

// DescribeTapExit turns a nastro-tap subprocess exit (err from cmd.Wait, its
// stderr output) into a user-facing error, special-casing exit code 2 (the
// TCC audio-capture permission failure) with a guided message.
func DescribeTapExit(err error, stderr string) error {
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
