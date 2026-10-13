// Package transcribe implements `nastro transcribe <id|last>`: resolving the
// target record and driving one of two backends via afconvert -- whisper-cli
// (default, fast, no accounts) or whisperx (speaker diarization). Its phases (check
// prereqs, convert, run, download) are exposed as separate, reusable
// functions so the TUI can drive them without going through the CLI's
// stdin-prompt flow.
package transcribe

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/records"
)

// ResolveRecord picks the record matching idOrLast: "last" is the most
// recently created record, anything else must match a dirname exactly.
func ResolveRecord(recs []records.Record, idOrLast string) (records.Record, error) {
	if idOrLast == "last" {
		if len(recs) == 0 {
			return records.Record{}, fmt.Errorf("record not found: last")
		}
		latest := recs[0]
		for _, r := range recs[1:] {
			if r.Date.After(latest.Date) {
				latest = r
			}
		}
		return latest, nil
	}

	for _, r := range recs {
		if r.ID == idOrLast {
			return r, nil
		}
	}
	return records.Record{}, fmt.Errorf("record not found: %s", idOrLast)
}

// ModelPath returns the expected on-disk path of a whisper.cpp ggml model.
func ModelPath(homeDir, model string) string {
	return filepath.Join(homeDir, ".cache", "whisper", "ggml-"+model+".bin")
}

// modelRepoCommit pins ggerganov/whisper.cpp on huggingface.co to the
// commit modelSHA256 was taken from, so a known model's bytes can't change
// under a moving ref.
const modelRepoCommit = "5359861c739e955e79d9a303bcbc70fb988958b1"

// modelSHA256 maps each ggml model published at modelRepoCommit to its
// SHA-256 (the file's Git LFS oid on huggingface.co).
var modelSHA256 = map[string]string{
	"base-q5_1":           "422f1ae452ade6f30a004d7e5c6a43195e4433bc370bf23fac9cc591f01a8898",
	"base-q8_0":           "c577b9a86e7e048a0b7eada054f4dd79a56bbfa911fbdacf900ac5b567cbb7d9",
	"base":                "60ed5bc3dd14eea856493d334349b405782ddcaf0028d4b5df4088345fba2efe",
	"base.en-q5_1":        "4baf70dd0d7c4247ba2b81fafd9c01005ac77c2f9ef064e00dcf195d0e2fdd2f",
	"base.en-q8_0":        "a4d4a0768075e13cfd7e19df3ae2dbc4a68d37d36a7dad45e8410c9a34f8c87e",
	"base.en":             "a03779c86df3323075f5e796cb2ce5029f00ec8869eee3fdfb897afe36c6d002",
	"large-v1":            "7d99f41a10525d0206bddadd86760181fa920438b6b33237e3118ff6c83bb53d",
	"large-v2-q5_0":       "3a214837221e4530dbc1fe8d734f302af393eb30bd0ed046042ebf4baf70f6f2",
	"large-v2-q8_0":       "fef54e6d898246a65c8285bfa83bd1807e27fadf54d5d4e81754c47634737e8c",
	"large-v2":            "9a423fe4d40c82774b6af34115b8b935f34152246eb19e80e376071d3f999487",
	"large-v3-q5_0":       "d75795ecff3f83b5faa89d1900604ad8c780abd5739fae406de19f23ecd98ad1",
	"large-v3-turbo-q5_0": "394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2",
	"large-v3-turbo-q8_0": "317eb69c11673c9de1e1f0d459b253999804ec71ac4c23c17ecf5fbe24e259a1",
	"large-v3-turbo":      "1fc70f774d38eb169993ac391eea357ef47c88757ef72ee5943879b7e8e2bc69",
	"large-v3":            "64d182b440b98d5203c4f9bd541544d84c605196c4f7b845dfa11fb23594d1e2",
	"medium-q5_0":         "19fea4b380c3a618ec4723c3eef2eb785ffba0d0538cf43f8f235e7b3b34220f",
	"medium-q8_0":         "42a1ffcbe4167d224232443396968db4d02d4e8e87e213d3ee2e03095dea6502",
	"medium":              "6c14d5adee5f86394037b4e4e8b59f1673b6cee10e3cf0b11bbdbee79c156208",
	"medium.en-q5_0":      "76733e26ad8fe1c7a5bf7531a9d41917b2adc0f20f2e4f5531688a8c6cd88eb0",
	"medium.en-q8_0":      "43fa2cd084de5a04399a896a9a7a786064e221365c01700cea4666005218f11c",
	"medium.en":           "cc37e93478338ec7700281a7ac30a10128929eb8f427dda2e865faa8f6da4356",
	"small-q5_1":          "ae85e4a935d7a567bd102fe55afc16bb595bdb618e11b2fc7591bc08120411bb",
	"small-q8_0":          "49c8fb02b65e6049d5fa6c04f81f53b867b5ec9540406812c643f177317f779f",
	"small":               "1be3a9b2063867b937e64e2ec7483364a79917e157fa98c5d94b5c1fffea987b",
	"small.en-q5_1":       "bfdff4894dcb76bbf647d56263ea2a96645423f1669176f4844a1bf8e478ad30",
	"small.en-q8_0":       "67a179f608ea6114bd3fdb9060e762b588a3fb3bd00c4387971be4d177958067",
	"small.en":            "c6138d6d58ecc8322097e0f987c32f1be8bb0a18532a3f88f734d1bbf9c41e5d",
	"tiny-q5_1":           "818710568da3ca15689e31a743197b520007872ff9576237bda97bd1b469c3d7",
	"tiny-q8_0":           "c2085835d3f50733e2ff6e4b41ae8a2b8d8110461e18821b09a15c40c42d1cca",
	"tiny":                "be07e048e1e599ad46341c8d2a135645097a538221678b7acdd1b1919c6e1b21",
	"tiny.en-q5_1":        "c77c5766f1cef09b6b7d47f21b546cbddd4157886b3b5d6d4f709e91e66c7c2b",
	"tiny.en-q8_0":        "5bc2b3860aa151a4c6e7bb095e1fcce7cf12c7b020ca08dcec0c6d018bb7dd94",
	"tiny.en":             "921e4cf8686fdd993dcd081a5da5b6c365bfde1162e72b08d75ac75289920b1f",
}

// ModelSHA256 returns the expected SHA-256 (hex) of a known ggml model, and
// ok=false for a custom model name nastro has no checksum for.
func ModelSHA256(model string) (sum string, ok bool) {
	sum, ok = modelSHA256[model]
	return sum, ok
}

// ModelDownloadURL returns the huggingface.co URL a whisper.cpp ggml model
// is downloaded from: pinned to modelRepoCommit for a known model, the
// repo's main branch for a custom name (which may postdate the pin).
func ModelDownloadURL(model string) string {
	ref := "main"
	if _, ok := modelSHA256[model]; ok {
		ref = modelRepoCommit
	}
	return "https://huggingface.co/ggerganov/whisper.cpp/resolve/" + ref + "/ggml-" + model + ".bin"
}

// DownloadPercent computes 0..1 progress from downloaded/total bytes; 0 when
// total is unknown (<=0, i.e. no Content-Length).
func DownloadPercent(downloaded, total int64) float64 {
	if total <= 0 {
		return 0
	}
	switch p := float64(downloaded) / float64(total); {
	case p > 1:
		return 1
	case p < 0:
		return 0
	default:
		return p
	}
}

// whisperProgressPrefix is the line prefix whisper-cli's --print-progress
// emits progress updates on.
const whisperProgressPrefix = "whisper_print_progress_callback: progress = "

// TranscribeTip is the one-line upsell shown after a successful whisper-cli
// transcription, pointing at whisperx for speaker labels -- whisper-cli is
// the default backend (fast, no accounts) but doesn't diarize. Shared by the
// CLI (Run, printed every time) and the TUI (shown once per session).
const TranscribeTip = `tip: want [SPEAKER_00] labels? set transcriber = "whisperx"`

// ParseWhisperProgress parses one line of whisper-cli's combined
// stdout/stderr for a --print-progress update ("whisper_print_progress_callback:
// progress = 45%"), returning the percentage (0..100) and ok=true if line is
// one. Anything else -- the bulk of whisper-cli's output -- is ok=false.
func ParseWhisperProgress(line string) (percent int, ok bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), whisperProgressPrefix)
	if !ok {
		return 0, false
	}
	rest = strings.TrimSuffix(strings.TrimSpace(rest), "%")
	pct, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return pct, true
}

// mentionsMemoryOrModel reports whether s (an error message, or captured
// whisper-cli output) hints at a memory/model-sizing problem, the one case
// FriendlyTranscribeError has specific guidance for.
func mentionsMemoryOrModel(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "memory") || strings.Contains(s, "model")
}

// FriendlyTranscribeError turns a transcribe run failure into an
// actionable, user-facing message in place of a bare exit code. err is the
// failure from ConvertToWav (afconvert) or a whisper-cli run's Job.Wait();
// output is whatever whisper-cli output was captured alongside it (unused
// for an afconvert failure: ConvertToWav already folds its own output into
// err). Returns "" for a nil err.
func FriendlyTranscribeError(err error, output string) string {
	if err == nil {
		return ""
	}

	msg := err.Error()
	if detail, ok := strings.CutPrefix(msg, "afconvert: "); ok {
		return fmt.Sprintf("audio conversion failed - the recording may be corrupted or empty (afconvert: %s)", detail)
	}

	detail := strings.TrimSpace(output)
	if repo, ok := gatedRepo(detail); ok {
		return fmt.Sprintf(
			"transcription failed: your HuggingFace token works, but you haven't been granted access to the diarization model yet.\n"+
				"Visit https://huggingface.co/%s while logged in and accept the model terms, then retry.", repo)
	}
	if mentionsMemoryOrModel(msg + " " + detail) {
		return fmt.Sprintf("transcription failed (%s): try a smaller model in ~/.config/nastro/config.toml", msg)
	}
	if detail == "" {
		return fmt.Sprintf("transcription failed: %s", msg)
	}
	return fmt.Sprintf("transcription failed: %s: %s", msg, detail)
}

// gatedRepo extracts the repo id from a huggingface GatedRepoError in
// whisperx output, so the guidance can point at the exact terms page.
func gatedRepo(output string) (string, bool) {
	if !strings.Contains(output, "GatedRepoError") && !strings.Contains(output, "gated repo") {
		return "", false
	}
	if m := gatedRepoID.FindStringSubmatch(output); m != nil {
		return m[1], true
	}
	return "pyannote/speaker-diarization-community-1", true
}

var gatedRepoID = regexp.MustCompile(`Access to model ([\w./-]+) is restricted`)

// tqdmProgressLine matches a tqdm-style progress bar line (e.g. "45%|####  |
// 12/27 [00:03<00:04, 3.21it/s]"), the kind of repeated, carriage-return-
// driven noise third-party libraries (torch, pyannote) emit -- useful once,
// as a live bar, meaningless as a scrollback line.
var tqdmProgressLine = regexp.MustCompile(`\d+%\|.*\|.*(it/s|s/it)`)

// IsNoiseLine reports whether line is display noise to drop from the TUI's
// on-screen tail of backend output: blank lines, Python's warnings.warn()
// output (both the "<file>:<line>: FooWarning: ..." line and the indented
// source line it prints beneath itself -- the well-known pyannote
// std()-degrees-of-freedom warning being the recurring example), internal
// (non-error) traceback frames, and repeated third-party progress bars.
// Everything else -- including useful INFO lines like "Performing
// diarization..." -- passes through. Display-only: the full, unfiltered
// output still goes to transcribe.log and to FriendlyTranscribeError on
// failure.
func IsNoiseLine(line string) bool {
	if strings.TrimSpace(line) == "" {
		return true
	}
	// A line indented with leading whitespace is a continuation of the
	// previous one (a warning's source snippet, a traceback frame's code
	// line) rather than a standalone message.
	if line[0] == ' ' || line[0] == '\t' {
		return true
	}

	trimmed := strings.TrimSpace(line)
	switch {
	case strings.Contains(trimmed, "Warning:"):
		return true
	case strings.HasPrefix(trimmed, "Traceback (most recent call last)"):
		return true
	case strings.HasPrefix(trimmed, `File "`) && strings.Contains(trimmed, "line "):
		return true
	case tqdmProgressLine.MatchString(trimmed):
		return true
	}
	return false
}

// --- everything below is side-effecting orchestration: subprocess exec,
// network I/O, filesystem. Deliberately outside TDD scope, except for pure
// helpers pulled out along the way (DownloadPercent), which are. ---

// CheckWhisperCLI reports whether whisper-cli can be found -- on PATH, or at
// one of the well-known locations resolveTool falls back to.
func CheckWhisperCLI() bool {
	_, ok := resolveTool("whisper-cli")
	return ok
}

// CheckModel reports whether the configured whisper model is already
// downloaded at ModelPath(homeDir, model).
func CheckModel(homeDir, model string) bool {
	_, err := os.Stat(ModelPath(homeDir, model))
	return err == nil
}

// whisperCLIMissingMsg is the guided error shown when neither whisper-cli
// nor an installer for it (brew) can be found -- shared by Run and the
// TUI's prereq check.
func whisperCLIMissingMsg() string {
	return fmt.Sprintf("whisper-cli not found (looked in %s). Install it with: brew install whisper-cpp", searchedToolPaths("whisper-cli"))
}

// InstallOffer describes a missing backend binary nastro can offer to
// install itself, via an installer it already found on the machine (uv for
// whisperx, brew for whisper-cli). TUI and CLI each format their own
// confirmation prompt from it (inline [y/n] vs. stdin [y/N]) and, on
// confirmation, run it via StartToolInstall.
type InstallOffer struct {
	Tool          string   // binary being installed, e.g. "whisperx"
	Installer     string   // display name of the installer, e.g. "uv"
	InstallerPath string   // resolveTool's resolved path to Installer
	Args          []string // args to run Installer with, e.g. ["tool", "install", "whisperx"]
	SizeHint      string   // best-effort size/time hint for the confirm prompt, "" if none
}

// PrereqStatus reports whether cfg's configured backend is ready to
// transcribe. Install is a missing binary nastro can offer to install
// itself (see InstallOffer); MissingMsg is a guided, user-facing message for
// a hard prerequisite that isn't fixable in-app (no installer found, or
// whisperx's HF token). ModelMissing/ModelPath cover whisper-cli's local
// ggml model, the other prerequisite the TUI can offer to download.
type PrereqStatus struct {
	MissingMsg   string
	Install      *InstallOffer
	ModelMissing bool
	ModelPath    string
}

// CheckPrereqs checks cfg's configured backend's prerequisites -- the one
// seam Run and the TUI's prereq check share, so the whisper-cli/whisperx
// branch only lives here.
func CheckPrereqs(cfg config.Config) PrereqStatus {
	if cfg.Transcriber == "whisperx" {
		if !CheckWhisperX() {
			if uvPath, ok := resolveTool("uv"); ok {
				return PrereqStatus{Install: &InstallOffer{
					Tool: "whisperx", Installer: "uv", InstallerPath: uvPath,
					Args: []string{"tool", "install", "whisperx"}, SizeHint: "~2 GB, a few minutes",
				}}
			}
			return PrereqStatus{MissingMsg: WhisperXMissingMsg()}
		}
		if !CheckFFmpeg() {
			if brewPath, ok := resolveTool("brew"); ok {
				return PrereqStatus{Install: &InstallOffer{
					Tool: "ffmpeg", Installer: "brew", InstallerPath: brewPath,
					Args: []string{"install", "ffmpeg"},
				}}
			}
			return PrereqStatus{MissingMsg: ffmpegMissingMsg()}
		}
		if cfg.ResolvedHFToken() == "" {
			return PrereqStatus{MissingMsg: MissingHFTokenMessage()}
		}
		return PrereqStatus{}
	}

	if !CheckWhisperCLI() {
		if brewPath, ok := resolveTool("brew"); ok {
			return PrereqStatus{Install: &InstallOffer{
				Tool: "whisper-cli", Installer: "brew", InstallerPath: brewPath,
				Args: []string{"install", "whisper-cpp"},
			}}
		}
		return PrereqStatus{MissingMsg: whisperCLIMissingMsg()}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return PrereqStatus{MissingMsg: whisperCLIMissingMsg()}
	}
	if !CheckModel(home, cfg.WhisperModel) {
		return PrereqStatus{ModelMissing: true, ModelPath: ModelPath(home, cfg.WhisperModel)}
	}
	return PrereqStatus{}
}

// StartTranscribeBackend dispatches to the configured backend -- whisper-cli
// or whisperx -- the one seam Run and the TUI's transcribe command share.
// home is only used by whisper-cli, to resolve its local model path.
func StartTranscribeBackend(ctx context.Context, cfg config.Config, home, outPrefix, wavPath string) (*Job, error) {
	if cfg.Transcriber == "whisperx" {
		return StartWhisperX(ctx, cfg.ResolvedHFToken(), cfg.Lang, cfg.WhisperModel, outPrefix, wavPath)
	}
	return StartWhisper(ctx, ModelPath(home, cfg.WhisperModel), cfg.Lang, outPrefix, wavPath)
}

// ConvertToWav converts audioPath (the recording's .m4a) to a 16kHz mono
// WAV at wavPath via afconvert, the format whisper-cli expects. Canceling
// ctx kills afconvert and reports context.Canceled.
func ConvertToWav(ctx context.Context, audioPath, wavPath string) error {
	cmd := exec.CommandContext(ctx, "afconvert", "-f", "WAVE", "-d", "LEI16@16000", "-c", "1", audioPath, wavPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return context.Canceled
		}
		return fmt.Errorf("afconvert: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Job is a running whisper-cli subprocess plus the means to observe its
// progress: a channel of its combined stdout/stderr lines, and its exit --
// exactly the awaitOrTick pattern the TUI already uses for nastro-tap.
type Job struct {
	cmd     *exec.Cmd
	lines   chan string
	waitErr chan error
}

// Lines returns the channel whisper-cli's combined stdout/stderr, split into
// lines, is delivered on. Closed once the process's output pipe reaches EOF.
func (j *Job) Lines() <-chan string { return j.lines }

// Wait returns the channel whisper-cli's exit is delivered on, exactly once:
// nil on success, context.Canceled if ctx was canceled, or the underlying
// error otherwise.
func (j *Job) Wait() <-chan error { return j.waitErr }

// StartWhisper spawns whisper-cli over wavPath, writing to a temporary
// outPrefix+".partial" prefix first. Only once the run succeeds are
// outPrefix+".partial.txt"/".srt" renamed (atomically) to outPrefix+".txt"/
// ".srt", overwriting a previous transcript at that point and no sooner --
// so a canceled or failed run never touches, let alone destroys, whatever
// transcript.txt/.srt was already there (principle #1: never destroy data
// on an aborted operation). Canceling ctx kills whisper-cli and removes
// whatever partial output it had written; a non-cancel failure does the
// same.
func StartWhisper(ctx context.Context, modelPath, lang, outPrefix, wavPath string) (*Job, error) {
	whisperPath, ok := resolveTool("whisper-cli")
	if !ok {
		return nil, errors.New(whisperCLIMissingMsg())
	}

	partialPrefix := outPrefix + ".partial"
	cmd := exec.CommandContext(ctx, whisperPath, "-m", modelPath, "-l", lang, "-otxt", "-osrt", "-of", partialPrefix, "-pp", "-f", wavPath)
	pr, pw := io.Pipe()
	logW, closeLog := openTranscribeLogWriter(outPrefix)
	cmd.Stdout = io.MultiWriter(pw, logW)
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		closeLog()
		return nil, fmt.Errorf("start whisper-cli: %w", err)
	}

	lines := make(chan string, 16)
	waitErr := make(chan error, 1)
	go streamLines(pr, lines)
	go func() {
		runErr := cmd.Wait()
		pw.Close()
		closeLog()
		if ctx.Err() != nil {
			runErr = context.Canceled
		}
		if runErr != nil {
			os.Remove(partialPrefix + ".txt")
			os.Remove(partialPrefix + ".srt")
		} else if finalizeErr := finalizeTranscript(partialPrefix, outPrefix); finalizeErr != nil {
			runErr = finalizeErr
		}
		waitErr <- runErr
	}()

	return &Job{cmd: cmd, lines: lines, waitErr: waitErr}, nil
}

// finalizeTranscript atomically promotes a successful run's partial
// .txt/.srt output to outPrefix, overwriting any previous transcript only
// now that the new one is known-complete.
func finalizeTranscript(partialPrefix, outPrefix string) error {
	// whisper-cli creates its output with the process umask (usually
	// 0644): tighten both files before they become the transcript.
	for _, ext := range []string{".txt", ".srt"} {
		if err := os.Chmod(partialPrefix+ext, config.PrivateFileMode); err != nil {
			return err
		}
	}
	if err := os.Rename(partialPrefix+".txt", outPrefix+".txt"); err != nil {
		return err
	}
	return os.Rename(partialPrefix+".srt", outPrefix+".srt")
}

// StartToolInstall runs an installer command (e.g. `uv tool install
// whisperx` or `brew install whisper-cpp`) in the background, streaming its
// combined stdout/stderr line by line the same way StartWhisper does --
// reused as-is by both the TUI's install-confirm flow and the CLI's stdin
// one. Canceling ctx kills the installer; on cancel the tool is simply left
// uninstalled (see the TUI's cancel handling for why no explicit "uv tool
// uninstall" is needed).
func StartToolInstall(ctx context.Context, installerPath string, args ...string) (*Job, error) {
	cmd := exec.CommandContext(ctx, installerPath, args...)
	cmd.Env = subprocessEnv()
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", filepath.Base(installerPath), err)
	}

	lines := make(chan string, 16)
	waitErr := make(chan error, 1)
	go streamLines(pr, lines)
	go func() {
		runErr := cmd.Wait()
		pw.Close()
		if ctx.Err() != nil {
			runErr = context.Canceled
		}
		waitErr <- runErr
	}()

	return &Job{cmd: cmd, lines: lines, waitErr: waitErr}, nil
}

// transcribeLogPath returns the crash-safe append log a backend run's full
// output is written to, alongside outPrefix (e.g. "<record
// dir>/transcript") -- so it lands at "<record dir>/transcribe.log". It
// isn't part of HasTranscript/the transcript itself, just raw evidence of
// what the backend printed.
func transcribeLogPath(outPrefix string) string {
	return filepath.Join(filepath.Dir(outPrefix), "transcribe.log")
}

// openTranscribeLogWriter opens outPrefix's transcribe.log for appending,
// returning it as an io.Writer alongside a close func. Best-effort: on any
// error opening it (e.g. the record dir vanished), returns io.Discard and a
// no-op close rather than failing the whole transcribe run over a log file.
func openTranscribeLogWriter(outPrefix string) (io.Writer, func()) {
	f, err := os.OpenFile(transcribeLogPath(outPrefix), os.O_APPEND|os.O_CREATE|os.O_WRONLY, config.PrivateFileMode)
	if err != nil {
		return io.Discard, func() {}
	}
	return f, func() { f.Close() }
}

func streamLines(r io.Reader, out chan<- string) {
	defer close(out)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		out <- scanner.Text()
	}
}

// DownloadProgress is one tick of an in-flight model download.
type DownloadProgress struct {
	Downloaded int64
	Total      int64 // <=0 if the server didn't send a Content-Length
}

// DownloadJob is a running model download: an HTTP GET streamed to a
// destPath+".part" file, renamed atomically to destPath on success.
type DownloadJob struct {
	progress chan DownloadProgress
	waitErr  chan error
	cancel   context.CancelFunc
}

// Progress returns the channel download ticks are delivered on (best-effort:
// a slow consumer just misses intermediate ticks). Closed when the download
// finishes, successfully or not.
func (j *DownloadJob) Progress() <-chan DownloadProgress { return j.progress }

// Wait returns the channel the download's outcome is delivered on, exactly
// once: nil on success, context.Canceled if Cancel was called, or the
// underlying error otherwise.
func (j *DownloadJob) Wait() <-chan error { return j.waitErr }

// Cancel aborts the download; its partial .part file is removed.
func (j *DownloadJob) Cancel() { j.cancel() }

// StartModelDownload starts streaming url to destPath in the background.
// When wantSHA256 is non-empty, the downloaded bytes must hash to it (hex,
// case-insensitive) or the download fails and nothing lands at destPath.
// An empty wantSHA256 skips the check (custom models).
func StartModelDownload(url, destPath, wantSHA256 string) *DownloadJob {
	ctx, cancel := context.WithCancel(context.Background())
	progress := make(chan DownloadProgress, 1)
	waitErr := make(chan error, 1)

	go func() {
		waitErr <- runDownload(ctx, url, destPath, wantSHA256, progress)
		close(progress)
	}()

	return &DownloadJob{progress: progress, waitErr: waitErr, cancel: cancel}
}

func runDownload(ctx context.Context, url, destPath, wantSHA256 string, progress chan<- DownloadProgress) (err error) {
	partPath := destPath + ".part"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", url, resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	out, err := os.Create(partPath)
	if err != nil {
		return err
	}
	defer func() {
		out.Close()
		if err != nil {
			os.Remove(partPath)
		}
	}()

	hash := sha256.New()
	total := resp.ContentLength
	var downloaded int64
	buf := make([]byte, 32*1024)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
			hash.Write(buf[:n])
			downloaded += int64(n)
			select {
			case progress <- DownloadProgress{Downloaded: downloaded, Total: total}:
			default:
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	if err := out.Close(); err != nil {
		return err
	}
	if wantSHA256 != "" {
		if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, wantSHA256) {
			return fmt.Errorf("download %s: checksum mismatch: got sha256 %s, want %s", url, got, wantSHA256)
		}
	}
	return os.Rename(partPath, destPath)
}

// modelMissingMsg is the CLI's error for a missing ggml model: where it is
// expected, where to download it from and, for a known model, the SHA-256
// to verify the manual download against.
func modelMissingMsg(modelPath, model string) string {
	msg := fmt.Sprintf("whisper model not found at %s\ndownload it from: %s", modelPath, ModelDownloadURL(model))
	if sum, ok := ModelSHA256(model); ok {
		msg += "\nexpected sha256: " + sum
	}
	return msg
}

// installConfirmPrompt formats offer's stdin confirmation prompt for Run --
// [y/N] (default no), matching the rest of Run's stdin-driven confirmations
// (see the overwrite prompt below). The TUI builds its own inline [y/n]
// wording instead (tui/confirm.go's formatToolInstallPrompt).
func installConfirmPrompt(offer InstallOffer) string {
	return fmt.Sprintf("%s is not installed. Install it now with %s? [y/N] ", offer.Tool, offer.Installer)
}

// confirmAndInstall asks the user (via stdin) whether to install offer's
// tool, streaming the installer's output to stdout on confirmation the same
// way Run streams the transcribe job's own output below. installed is false
// (with a nil error) when the user declines -- Run then reports the tool as
// still missing rather than pressing on.
func confirmAndInstall(offer InstallOffer) (installed bool, err error) {
	fmt.Print(installConfirmPrompt(offer))
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		return false, nil
	}

	job, err := StartToolInstall(context.Background(), offer.InstallerPath, offer.Args...)
	if err != nil {
		return true, err
	}
	for line := range job.Lines() {
		fmt.Println(line)
	}
	return true, <-job.Wait()
}

// Run resolves idOrLast against cfg.OutputDir and transcribes it with
// cfg.Transcriber's backend (whisper-cli by default, or whisperx),
// converting the source audio to WAV via afconvert first. Its overwrite
// confirmation reads from stdin directly: the CLI is the one caller that's
// expected to (the TUI drives the same phases itself, with an inline y/n
// instead).
func Run(cfg config.Config, idOrLast string) error {
	recs, err := records.Scan(cfg.OutputDir)
	if err != nil {
		return fmt.Errorf("scan records: %w", err)
	}
	rec, err := ResolveRecord(recs, idOrLast)
	if err != nil {
		return err
	}

	status := CheckPrereqs(cfg)
	if status.Install != nil {
		installed, err := confirmAndInstall(*status.Install)
		if err != nil {
			return err
		}
		if !installed {
			return fmt.Errorf("aborted: %s not installed", status.Install.Tool)
		}
		status = CheckPrereqs(cfg) // e.g. HF token (whisperx) or the ggml model (whisper-cli) may still be missing
	}
	if status.MissingMsg != "" {
		return errors.New(status.MissingMsg)
	}
	if status.ModelMissing {
		return errors.New(modelMissingMsg(status.ModelPath, cfg.WhisperModel))
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}

	recordDir := filepath.Join(cfg.OutputDir, rec.ID)
	transcriptPath := filepath.Join(recordDir, "transcript.txt")
	if _, err := os.Stat(transcriptPath); err == nil {
		fmt.Print("record already transcribed. Overwrite? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("aborted, nothing changed")
			return nil
		}
	}

	audioPath := filepath.Join(recordDir, "audio.m4a")
	tmpWav, err := os.CreateTemp("", "nastro-transcribe-*.wav")
	if err != nil {
		return fmt.Errorf("create temp wav: %w", err)
	}
	tmpWavPath := tmpWav.Name()
	tmpWav.Close()
	defer os.Remove(tmpWavPath)

	if err := ConvertToWav(context.Background(), audioPath, tmpWavPath); err != nil {
		return errors.New(FriendlyTranscribeError(err, ""))
	}

	job, err := StartTranscribeBackend(context.Background(), cfg, home, filepath.Join(recordDir, "transcript"), tmpWavPath)
	if err != nil {
		return err
	}
	var output strings.Builder
	for line := range job.Lines() {
		fmt.Println(line)
		output.WriteString(line)
		output.WriteByte('\n')
	}
	if err := <-job.Wait(); err != nil {
		return errors.New(FriendlyTranscribeError(err, output.String()))
	}
	if cfg.Transcriber != "whisperx" {
		fmt.Println(TranscribeTip)
	}
	return nil
}
