// Package transcribe implements `nastro transcribe <id|last>`: resolving the
// target record and driving whisper-cli via afconvert. Its phases (check
// whisper-cli, check model, convert, run, download) are exposed as separate,
// reusable functions so the TUI can drive them without going through the
// CLI's stdin-prompt flow.
package transcribe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

// ModelDownloadURL returns the huggingface.co URL a whisper.cpp ggml model
// is downloaded from.
func ModelDownloadURL(model string) string {
	return "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-" + model + ".bin"
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
	if mentionsMemoryOrModel(msg + " " + detail) {
		return fmt.Sprintf("transcription failed (%s): try a smaller model in ~/.config/nastro/config.toml", msg)
	}
	if detail == "" {
		return fmt.Sprintf("transcription failed: %s", msg)
	}
	return fmt.Sprintf("transcription failed: %s: %s", msg, detail)
}

// --- everything below is side-effecting orchestration: subprocess exec,
// network I/O, filesystem. Deliberately outside TDD scope, except for pure
// helpers pulled out along the way (DownloadPercent), which are. ---

// CheckWhisperCLI reports whether whisper-cli is on PATH.
func CheckWhisperCLI() bool {
	_, err := exec.LookPath("whisper-cli")
	return err == nil
}

// CheckModel reports whether the configured whisper model is already
// downloaded at ModelPath(homeDir, model).
func CheckModel(homeDir, model string) bool {
	_, err := os.Stat(ModelPath(homeDir, model))
	return err == nil
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

// StartWhisper spawns whisper-cli over wavPath, writing outPrefix+".txt" and
// outPrefix+".srt". Canceling ctx kills whisper-cli and removes whatever
// partial .txt/.srt it had written, so a canceled job never leaves a
// half-written transcript behind.
func StartWhisper(ctx context.Context, modelPath, lang, outPrefix, wavPath string) (*Job, error) {
	cmd := exec.CommandContext(ctx, "whisper-cli", "-m", modelPath, "-l", lang, "-otxt", "-osrt", "-of", outPrefix, "-pp", "-f", wavPath)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start whisper-cli: %w", err)
	}

	lines := make(chan string, 16)
	waitErr := make(chan error, 1)
	go streamLines(pr, lines)
	go func() {
		err := cmd.Wait()
		pw.Close()
		if ctx.Err() != nil {
			os.Remove(outPrefix + ".txt")
			os.Remove(outPrefix + ".srt")
			err = context.Canceled
		}
		waitErr <- err
	}()

	return &Job{cmd: cmd, lines: lines, waitErr: waitErr}, nil
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
func StartModelDownload(url, destPath string) *DownloadJob {
	ctx, cancel := context.WithCancel(context.Background())
	progress := make(chan DownloadProgress, 1)
	waitErr := make(chan error, 1)

	go func() {
		waitErr <- runDownload(ctx, url, destPath, progress)
		close(progress)
	}()

	return &DownloadJob{progress: progress, waitErr: waitErr, cancel: cancel}
}

func runDownload(ctx context.Context, url, destPath string, progress chan<- DownloadProgress) (err error) {
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
	return os.Rename(partPath, destPath)
}

// Run resolves idOrLast against cfg.OutputDir and transcribes it with
// whisper-cli, converting the source audio to WAV via afconvert first. Its
// overwrite confirmation reads from stdin directly: the CLI is the one
// caller that's expected to (the TUI drives the same phases itself, with an
// inline y/n instead).
func Run(cfg config.Config, idOrLast string) error {
	recs, err := records.Scan(cfg.OutputDir)
	if err != nil {
		return fmt.Errorf("scan records: %w", err)
	}
	rec, err := ResolveRecord(recs, idOrLast)
	if err != nil {
		return err
	}

	if !CheckWhisperCLI() {
		return fmt.Errorf("whisper-cli not found. Install it with: brew install whisper-cpp")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	if !CheckModel(home, cfg.WhisperModel) {
		return fmt.Errorf("whisper model not found at %s\ndownload it from: %s",
			ModelPath(home, cfg.WhisperModel), ModelDownloadURL(cfg.WhisperModel))
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

	modelPath := ModelPath(home, cfg.WhisperModel)
	job, err := StartWhisper(context.Background(), modelPath, cfg.Lang, filepath.Join(recordDir, "transcript"), tmpWavPath)
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
	return nil
}
