package transcribe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withExtraToolDirs overrides extraToolDirs for the duration of the test,
// restoring the original afterward. Needed to make "nothing found" tests
// hermetic: unlike ~/.local/bin (redirectable via $HOME), extraToolDirs'
// entries are fixed absolute paths that may genuinely exist (and hold a
// real uv/brew) on the machine running the test.
func withExtraToolDirs(t *testing.T, dirs []string) {
	t.Helper()
	old := extraToolDirs
	extraToolDirs = dirs
	t.Cleanup(func() { extraToolDirs = old })
}

// statFileMode is a stub os.FileInfo carrying just what resolveToolAmong
// inspects (IsDir, Mode).
type statFileMode struct {
	mode os.FileMode
}

func (s statFileMode) Name() string       { return "" }
func (s statFileMode) Size() int64        { return 0 }
func (s statFileMode) Mode() os.FileMode  { return s.mode }
func (s statFileMode) ModTime() time.Time { return time.Time{} }
func (s statFileMode) IsDir() bool        { return s.mode.IsDir() }
func (s statFileMode) Sys() any           { return nil }

func fakeStat(existing map[string]os.FileMode) func(string) (os.FileInfo, error) {
	return func(p string) (os.FileInfo, error) {
		mode, ok := existing[p]
		if !ok {
			return nil, os.ErrNotExist
		}
		return statFileMode{mode: mode}, nil
	}
}

func TestResolveToolAmongFindsFirstMatch(t *testing.T) {
	stat := fakeStat(map[string]os.FileMode{
		"/dir-a/whisperx": 0o755,
		"/dir-b/whisperx": 0o755,
	})
	got, ok := resolveToolAmong("whisperx", []string{"/dir-a", "/dir-b"}, stat)
	if !ok || got != "/dir-a/whisperx" {
		t.Errorf("resolveToolAmong() = (%q, %v), want (/dir-a/whisperx, true)", got, ok)
	}
}

func TestResolveToolAmongSkipsEarlierMiss(t *testing.T) {
	stat := fakeStat(map[string]os.FileMode{
		"/dir-b/whisperx": 0o755,
	})
	got, ok := resolveToolAmong("whisperx", []string{"/dir-a", "/dir-b"}, stat)
	if !ok || got != "/dir-b/whisperx" {
		t.Errorf("resolveToolAmong() = (%q, %v), want (/dir-b/whisperx, true)", got, ok)
	}
}

func TestResolveToolAmongSkipsDirectory(t *testing.T) {
	stat := fakeStat(map[string]os.FileMode{
		"/dir-a/whisperx": os.ModeDir | 0o755,
		"/dir-b/whisperx": 0o755,
	})
	got, ok := resolveToolAmong("whisperx", []string{"/dir-a", "/dir-b"}, stat)
	if !ok || got != "/dir-b/whisperx" {
		t.Errorf("resolveToolAmong() = (%q, %v), want it to skip the directory and find /dir-b/whisperx", got, ok)
	}
}

func TestResolveToolAmongSkipsNonExecutable(t *testing.T) {
	stat := fakeStat(map[string]os.FileMode{
		"/dir-a/whisperx": 0o644, // exists, but not executable
		"/dir-b/whisperx": 0o755,
	})
	got, ok := resolveToolAmong("whisperx", []string{"/dir-a", "/dir-b"}, stat)
	if !ok || got != "/dir-b/whisperx" {
		t.Errorf("resolveToolAmong() = (%q, %v), want it to skip the non-executable file", got, ok)
	}
}

func TestResolveToolAmongAbsent(t *testing.T) {
	stat := fakeStat(nil)
	_, ok := resolveToolAmong("whisperx", []string{"/dir-a", "/dir-b"}, stat)
	if ok {
		t.Errorf("resolveToolAmong() ok = true, want false (nothing exists)")
	}
}

// writeExecutable writes an executable stub file at dir/name.
func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestResolveToolFindsOnPATH(t *testing.T) {
	scriptDir := t.TempDir()
	want := writeExecutable(t, scriptDir, "whisperx")
	t.Setenv("PATH", scriptDir)
	withExtraToolDirs(t, nil)

	got, ok := resolveTool("whisperx")
	if !ok || got != want {
		t.Errorf("resolveTool() = (%q, %v), want (%q, true)", got, ok, want)
	}
}

func TestResolveToolFallsBackToLocalBin(t *testing.T) {
	// PATH has nothing; extraToolDirs cleared so the test is hermetic
	// regardless of what's actually installed on the machine running it.
	t.Setenv("PATH", t.TempDir())
	withExtraToolDirs(t, nil)

	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatalf("mkdir ~/.local/bin: %v", err)
	}
	want := writeExecutable(t, localBin, "whisperx")
	t.Setenv("HOME", home)

	got, ok := resolveTool("whisperx")
	if !ok || got != want {
		t.Errorf("resolveTool() = (%q, %v), want (%q, true) via ~/.local/bin", got, ok, want)
	}
}

func TestResolveToolFallsBackToExtraDirs(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // empty ~/.local/bin

	extraDir := t.TempDir()
	want := writeExecutable(t, extraDir, "uv")
	withExtraToolDirs(t, []string{extraDir})

	got, ok := resolveTool("uv")
	if !ok || got != want {
		t.Errorf("resolveTool() = (%q, %v), want (%q, true) via extraToolDirs", got, ok, want)
	}
}

func TestResolveToolAbsentEverywhere(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	withExtraToolDirs(t, nil)

	_, ok := resolveTool("whisperx")
	if ok {
		t.Errorf("resolveTool() ok = true, want false: whisperx isn't anywhere nastro looked")
	}
}

func TestSearchedToolPathsListsEveryLocation(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	withExtraToolDirs(t, []string{"/opt/homebrew/bin", "/usr/local/bin"})

	got := searchedToolPaths("whisperx")
	want := []string{"$PATH", "/home/tester/.local/bin/whisperx", "/opt/homebrew/bin/whisperx", "/usr/local/bin/whisperx"}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("searchedToolPaths() = %q, want it to contain %q", got, w)
		}
	}
}

func TestAugmentedPathPrependsMissingDirs(t *testing.T) {
	withExtraToolDirs(t, []string{"/opt/homebrew/bin", "/usr/local/bin"})

	got := augmentedPath("/usr/bin:/bin", "/home/tester")
	want := "/home/tester/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	if got != want {
		t.Errorf("augmentedPath() = %q, want %q", got, want)
	}
}

func TestAugmentedPathDedupsAlreadyPresentDirs(t *testing.T) {
	withExtraToolDirs(t, []string{"/opt/homebrew/bin", "/usr/local/bin"})

	got := augmentedPath("/opt/homebrew/bin:/usr/bin:/bin", "/home/tester")
	want := "/home/tester/.local/bin:/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin"
	if got != want {
		t.Errorf("augmentedPath() = %q, want %q (no duplicate /opt/homebrew/bin)", got, want)
	}
}

func TestAugmentedPathEmptyCurrent(t *testing.T) {
	withExtraToolDirs(t, []string{"/opt/homebrew/bin", "/usr/local/bin"})

	got := augmentedPath("", "/home/tester")
	want := "/home/tester/.local/bin:/opt/homebrew/bin:/usr/local/bin"
	if got != want {
		t.Errorf("augmentedPath() = %q, want %q (no stray leading/trailing colon)", got, want)
	}
}

func TestAugmentedPathNoHome(t *testing.T) {
	withExtraToolDirs(t, []string{"/opt/homebrew/bin"})

	got := augmentedPath("/usr/bin", "")
	want := "/opt/homebrew/bin:/usr/bin"
	if got != want {
		t.Errorf("augmentedPath() = %q, want %q (no ~/.local/bin without a home dir)", got, want)
	}
}

func TestAugmentedPathAllDirsAlreadyPresentReturnsUnchanged(t *testing.T) {
	withExtraToolDirs(t, []string{"/opt/homebrew/bin"})

	current := "/home/tester/.local/bin:/opt/homebrew/bin:/usr/bin"
	got := augmentedPath(current, "/home/tester")
	if got != current {
		t.Errorf("augmentedPath() = %q, want unchanged %q", got, current)
	}
}

func TestSubprocessEnvAugmentsPathOnly(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	withExtraToolDirs(t, []string{"/opt/homebrew/bin"})
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("SOME_OTHER_VAR", "unchanged")

	env := subprocessEnv()
	var gotPath string
	var sawOther bool
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			gotPath = v
		}
		if kv == "SOME_OTHER_VAR=unchanged" {
			sawOther = true
		}
	}
	wantPath := "/home/tester/.local/bin:/opt/homebrew/bin:/usr/bin"
	if gotPath != wantPath {
		t.Errorf("subprocessEnv() PATH = %q, want %q", gotPath, wantPath)
	}
	if !sawOther {
		t.Errorf("subprocessEnv() dropped an unrelated env var")
	}
}
