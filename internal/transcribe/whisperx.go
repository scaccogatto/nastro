// whisperx.go implements the whisperx backend: speaker-diarized
// transcription via the `whisperx` CLI (installed with
// `uv tool install whisperx`). Unlike whisper-cli, whisperx doesn't write
// nastro's transcript.txt/.srt directly -- it only produces a JSON dump of
// its segments (--output_format json). The pure parsing/formatting below
// turns that JSON into the same [SPEAKER_00]-prefixed txt/srt shape
// whisper-cli's own output has, so the rest of nastro (detail preview,
// TUI/CLI) doesn't need to know which backend produced a transcript.
package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// WhisperXMissingMsg is the guided error shown when neither whisperx nor its
// installer (uv) can be found anywhere resolveTool looks -- the one
// whisperx prerequisite that can't be offered as an in-app install (see
// CheckPrereqs: when uv *is* found, an InstallOffer is returned instead).
func WhisperXMissingMsg() string {
	return fmt.Sprintf(
		"whisperx not found (looked in %s).\ninstall uv first, then whisperx:\n  brew install uv\n  uv tool install whisperx",
		searchedToolPaths("whisperx"))
}

// MissingHFTokenMessage is the guided, multi-line error shown when neither
// cfg.HFToken nor $HF_TOKEN is set -- whisperx's --diarize needs a
// HuggingFace access token to download pyannote's gated diarization model.
// The token itself is never part of this message (or logged anywhere).
//
// pyannote/speaker-diarization-community-1 is whisperx's own default
// --diarize_model (whisperx --help, v3.8.6) and the actual gate a real,
// tokenless run hits -- not speaker-diarization-3.1/segmentation-3.0, an
// older default this message used to point at.
func MissingHFTokenMessage() string {
	return "a HuggingFace token is required for speaker diarization.\n\n" +
		"1. Create a read token at https://huggingface.co/settings/tokens\n" +
		"2. Accept the terms at https://huggingface.co/pyannote/speaker-diarization-community-1\n" +
		"3. Add it to ~/.config/nastro/config.toml:\n" +
		"   hf_token = \"hf_...\""
}

// CheckWhisperX reports whether whisperx can be found -- on PATH, or at one
// of the well-known locations resolveTool falls back to.
func CheckWhisperX() bool {
	_, ok := resolveTool("whisperx")
	return ok
}

// CheckFFmpeg reports whether ffmpeg can be found -- on PATH, or at one of
// the well-known locations resolveTool falls back to. whisperx shells out
// to it (via torchcodec/pyav) to load audio, so it's a whisperx prerequisite
// too, not just something subprocessEnv papers over for a PATH that already
// has it under a name resolveTool wouldn't find.
func CheckFFmpeg() bool {
	_, ok := resolveTool("ffmpeg")
	return ok
}

// ffmpegMissingMsg is the guided error shown when neither ffmpeg nor an
// installer for it (brew) can be found.
func ffmpegMissingMsg() string {
	return fmt.Sprintf("ffmpeg not found (looked in %s), required by whisperx to load audio. Install it with: brew install ffmpeg", searchedToolPaths("ffmpeg"))
}

// Segment is one diarized transcript segment, as parsed from whisperx's
// --output_format json output.
type Segment struct {
	Speaker string
	Start   float64
	End     float64
	Text    string
}

// wxOutput mirrors the shape of whisperx's --output_format json: a flat
// "segments" array. Word-level alignment fields are present in the real
// output but ignored here -- nastro only renders segment-level transcripts.
type wxOutput struct {
	Segments []struct {
		Start   float64 `json:"start"`
		End     float64 `json:"end"`
		Text    string  `json:"text"`
		Speaker string  `json:"speaker"`
	} `json:"segments"`
}

// parseWhisperXJSON parses whisperx's --output_format json output into a
// flat list of segments. A segment whose diarization couldn't assign a
// speaker (no "speaker" key) keeps an empty Speaker -- speakerLabel is
// where that gets a display fallback.
func parseWhisperXJSON(data []byte) ([]Segment, error) {
	var out wxOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse whisperx json: %w", err)
	}
	segs := make([]Segment, len(out.Segments))
	for i, s := range out.Segments {
		segs[i] = Segment{Speaker: s.Speaker, Start: s.Start, End: s.End, Text: strings.TrimSpace(s.Text)}
	}
	return segs, nil
}

// speakerLabel returns seg's speaker label, falling back to a fixed
// placeholder when diarization left it unassigned.
func speakerLabel(speaker string) string {
	if speaker == "" {
		return "SPEAKER_UNKNOWN"
	}
	return speaker
}

// speakerRun is one run of consecutive same-speaker segments, merged.
type speakerRun struct {
	Speaker string
	Start   float64
	End     float64
	Text    string
}

// groupBySpeaker merges consecutive segments sharing the same speaker into
// single runs, concatenating their text and spanning from the first
// segment's start to the last segment's end.
func groupBySpeaker(segs []Segment) []speakerRun {
	var runs []speakerRun
	for _, s := range segs {
		label := speakerLabel(s.Speaker)
		if n := len(runs); n > 0 && runs[n-1].Speaker == label {
			runs[n-1].End = s.End
			runs[n-1].Text += " " + s.Text
			continue
		}
		runs = append(runs, speakerRun{Speaker: label, Start: s.Start, End: s.End, Text: s.Text})
	}
	return runs
}

// formatDiarizedTxt renders segs as "[SPEAKER_00] text..." lines, one per
// run of consecutive same-speaker segments.
func formatDiarizedTxt(segs []Segment) string {
	var b strings.Builder
	for _, r := range groupBySpeaker(segs) {
		fmt.Fprintf(&b, "[%s] %s\n", r.Speaker, strings.TrimSpace(r.Text))
	}
	return b.String()
}

// formatDiarizedSrt renders segs as numbered SRT blocks, one per run of
// consecutive same-speaker segments, prefixed with the speaker label.
func formatDiarizedSrt(segs []Segment) string {
	var b strings.Builder
	for i, r := range groupBySpeaker(segs) {
		fmt.Fprintf(&b, "%d\n%s --> %s\n[%s] %s\n\n", i+1, srtTimestamp(r.Start), srtTimestamp(r.End), r.Speaker, strings.TrimSpace(r.Text))
	}
	return b.String()
}

// srtTimestamp formats seconds as SRT's HH:MM:SS,mmm timestamp.
func srtTimestamp(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	d -= s * time.Second
	ms := d / time.Millisecond
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

// --- everything below is side-effecting orchestration: subprocess exec,
// filesystem. Deliberately outside TDD scope, same as StartWhisper. ---

// StartWhisperX spawns whisperx over wavPath with diarization enabled.
// whisperx's own output (--output_format json) is written to a private
// temp directory; once the run succeeds, that JSON is parsed and rendered
// into transcript.txt/.srt at outPrefix via the same .partial->rename
// pattern StartWhisper uses (see writeDiarizedTranscript/finalizeTranscript),
// so a failed, canceled, or externally-killed run never touches an
// existing transcript.
//
// Failure-mode guarantee matrix, for outPrefix.txt/.srt (the existing
// transcript, if any):
//
//	ctx canceled                        -> untouched; whisperx killed (SIGKILL, exec's CommandContext default); temp dir removed
//	whisperx crashes / killed externally -> untouched; temp dir removed
//	whisperx exits non-zero              -> untouched; temp dir removed
//	whisperx succeeds, JSON unparsable   -> untouched; temp dir removed
//	parse OK, partial .txt/.srt written  -> replaced atomically (rename), only now
//
// The window in which a *new* transcript could be left half-written is
// exactly finalizeTranscript's two renames -- whisperx itself never writes
// anywhere near outPrefix.
func StartWhisperX(ctx context.Context, hfToken, lang, model, outPrefix, wavPath string) (*Job, error) {
	whisperxPath, ok := resolveTool("whisperx")
	if !ok {
		return nil, errors.New(WhisperXMissingMsg())
	}

	tmpDir, err := os.MkdirTemp("", "nastro-whisperx-*")
	if err != nil {
		return nil, fmt.Errorf("create whisperx temp dir: %w", err)
	}

	cmd := exec.CommandContext(ctx, whisperxPath, wavPath,
		"--diarize",
		"--language", lang,
		"--model", model,
		"--hf_token", hfToken,
		"--device", "cpu",
		"--compute_type", "int8",
		"--output_dir", tmpDir,
		"--output_format", "json",
	)
	// whisperx shells out to ffmpeg (via torchcodec/pyav) to load audio; a
	// bare PATH -- common outside an interactive shell -- often can't find
	// it even once whisperx itself is resolved. Same fallback as
	// resolveTool, applied to the child's own PATH lookups.
	cmd.Env = subprocessEnv()
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("start whisperx: %w", err)
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
		if runErr == nil {
			runErr = finishWhisperX(tmpDir, wavPath, outPrefix)
		}
		os.RemoveAll(tmpDir)
		waitErr <- runErr
	}()

	return &Job{cmd: cmd, lines: lines, waitErr: waitErr}, nil
}

// finishWhisperX reads and parses a successful whisperx run's JSON output
// (written at tmpDir/<wav-basename>.json, whisperx's own naming for
// --output_dir) and promotes it to outPrefix.txt/.srt.
func finishWhisperX(tmpDir, wavPath, outPrefix string) error {
	base := strings.TrimSuffix(filepath.Base(wavPath), filepath.Ext(wavPath))
	data, err := os.ReadFile(filepath.Join(tmpDir, base+".json"))
	if err != nil {
		return fmt.Errorf("read whisperx output: %w", err)
	}
	segs, err := parseWhisperXJSON(data)
	if err != nil {
		return err
	}
	return writeDiarizedTranscript(segs, outPrefix)
}

// writeDiarizedTranscript writes segs' txt/srt renderings to
// outPrefix+".partial.txt"/".srt" then promotes them atomically via
// finalizeTranscript -- the same never-destroy-on-failure guarantee
// StartWhisper's whisper-cli output gets.
func writeDiarizedTranscript(segs []Segment, outPrefix string) error {
	partialPrefix := outPrefix + ".partial"
	if err := os.WriteFile(partialPrefix+".txt", []byte(formatDiarizedTxt(segs)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(partialPrefix+".srt", []byte(formatDiarizedSrt(segs)), 0o644); err != nil {
		os.Remove(partialPrefix + ".txt")
		return err
	}
	return finalizeTranscript(partialPrefix, outPrefix)
}
