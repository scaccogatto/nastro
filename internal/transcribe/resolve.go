// resolve.go implements robust lookup of nastro's transcription-tool
// dependencies (whisperx, whisper-cli, uv, brew): exec.LookPath alone misses
// tools installed by uv (~/.local/bin) or Homebrew (/opt/homebrew/bin,
// /usr/local/bin) when the running process's PATH doesn't include them --
// common for a GUI-launched terminal or a minimal shell profile. resolveTool
// checks those well-known locations too, so nastro finds (and can offer to
// install) its dependencies without the user having to fix their PATH.
package transcribe

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// extraToolDirs are the fixed, well-known install locations checked after
// PATH and ~/.local/bin. A package var (rather than a literal in
// toolSearchDirs) so tests can override it -- the real paths are absolute
// and can't be redirected via $HOME the way ~/.local/bin can, so hermetic
// "nothing found" tests need a seam here too.
var extraToolDirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}

// toolSearchDirs is resolveTool's fixed search list, past PATH: home's
// ~/.local/bin (where `uv tool install` puts its shims) first, then
// extraToolDirs (Homebrew's own bin dirs, Apple Silicon and Intel).
func toolSearchDirs(home string) []string {
	dirs := append([]string{}, extraToolDirs...)
	if home == "" {
		return dirs
	}
	return append([]string{filepath.Join(home, ".local", "bin")}, dirs...)
}

// resolveToolAmong picks name's path among candidateDirs, in order: the
// first for which stat reports an existing, executable, non-directory file.
// Pure (stat is injected) -- the actual choice logic PATH-independent
// resolution boils down to, and what P1's tests exercise directly.
func resolveToolAmong(name string, candidateDirs []string, stat func(string) (os.FileInfo, error)) (string, bool) {
	for _, dir := range candidateDirs {
		p := filepath.Join(dir, name)
		info, err := stat(p)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		return p, true
	}
	return "", false
}

// resolveTool locates name's executable: first via PATH (exec.LookPath,
// respecting whatever the user has set up), then among toolSearchDirs --
// the well-known locations uv/Homebrew install to but a GUI-launched or
// minimal-PATH process often doesn't have.
func resolveTool(name string) (string, bool) {
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	home, _ := os.UserHomeDir()
	return resolveToolAmong(name, toolSearchDirs(home), os.Stat)
}

// searchedToolPaths renders the locations resolveTool checked for name, for
// a "not found" message that tells the user exactly where nastro looked
// instead of just "not on PATH".
func searchedToolPaths(name string) string {
	home, _ := os.UserHomeDir()
	dirs := toolSearchDirs(home)
	paths := make([]string, 0, len(dirs)+1)
	paths = append(paths, "$PATH")
	for _, d := range dirs {
		paths = append(paths, filepath.Join(d, name))
	}
	return strings.Join(paths, ", ")
}

// augmentedPath returns current's PATH with toolSearchDirs(home) prepended
// (skipping any already present, in toolSearchDirs order), so a subprocess
// nastro spawns (whisperx, an installer) can find ffmpeg and other helpers
// via its own PATH lookups regardless of the parent process's PATH --
// resolveTool's own fallback, applied to the child's environment. Pure:
// candidate dirs are passed in, not resolved here.
func augmentedPath(current, home string) string {
	present := map[string]bool{}
	for _, p := range strings.Split(current, ":") {
		if p != "" {
			present[p] = true
		}
	}

	var prepend []string
	for _, d := range toolSearchDirs(home) {
		if present[d] {
			continue
		}
		present[d] = true
		prepend = append(prepend, d)
	}
	if len(prepend) == 0 {
		return current
	}
	if current == "" {
		return strings.Join(prepend, ":")
	}
	return strings.Join(prepend, ":") + ":" + current
}

// subprocessEnv returns os.Environ() with PATH augmented (see
// augmentedPath), for spawning whisperx or an installer.
func subprocessEnv() []string {
	home, _ := os.UserHomeDir()
	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	found := false
	for _, kv := range env {
		if path, ok := strings.CutPrefix(kv, "PATH="); ok {
			out = append(out, "PATH="+augmentedPath(path, home))
			found = true
			continue
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, "PATH="+augmentedPath("", home))
	}
	return out
}
