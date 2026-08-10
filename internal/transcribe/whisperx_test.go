package transcribe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/scaccogatto/nastro/internal/config"
)

// realisticWhisperXJSON is a fixture shaped like whisperx's real
// --output_format json output for a short two-speaker exchange: segments
// with start/end/text/speaker, plus word-level alignment fields nastro
// doesn't use (present to prove parseWhisperXJSON tolerates and ignores
// them).
const realisticWhisperXJSON = `{
  "segments": [
    {
      "start": 0.512,
      "end": 2.834,
      "text": " Buongiorno a tutti, iniziamo la call.",
      "speaker": "SPEAKER_00",
      "words": [{"word": "Buongiorno", "start": 0.512, "end": 1.0, "score": 0.9}]
    },
    {
      "start": 3.1,
      "end": 4.2,
      "text": " Certo, procediamo pure.",
      "speaker": "SPEAKER_01",
      "words": [{"word": "Certo", "start": 3.1, "end": 3.4, "score": 0.8}]
    },
    {
      "start": 4.3,
      "end": 6.75,
      "text": " Il punto principale oggi è la migrazione.",
      "speaker": "SPEAKER_01",
      "words": [{"word": "Il", "start": 4.3, "end": 4.4, "score": 0.95}]
    },
    {
      "start": 7.0,
      "end": 8.0,
      "text": " Perfetto.",
      "speaker": "SPEAKER_00"
    }
  ],
  "language": "it"
}`

func TestParseWhisperXJSON(t *testing.T) {
	segs, err := parseWhisperXJSON([]byte(realisticWhisperXJSON))
	if err != nil {
		t.Fatalf("parseWhisperXJSON: %v", err)
	}
	if len(segs) != 4 {
		t.Fatalf("len(segs) = %d, want 4", len(segs))
	}
	want := Segment{Speaker: "SPEAKER_00", Start: 0.512, End: 2.834, Text: "Buongiorno a tutti, iniziamo la call."}
	if segs[0] != want {
		t.Errorf("segs[0] = %+v, want %+v", segs[0], want)
	}
	if segs[2].Speaker != "SPEAKER_01" {
		t.Errorf("segs[2].Speaker = %q, want SPEAKER_01", segs[2].Speaker)
	}
}

func TestParseWhisperXJSONMissingSpeaker(t *testing.T) {
	segs, err := parseWhisperXJSON([]byte(`{"segments":[{"start":0,"end":1,"text":"hello"}]}`))
	if err != nil {
		t.Fatalf("parseWhisperXJSON: %v", err)
	}
	if segs[0].Speaker != "" {
		t.Errorf("Speaker = %q, want empty (no diarization assignment)", segs[0].Speaker)
	}
}

func TestParseWhisperXJSONMalformed(t *testing.T) {
	if _, err := parseWhisperXJSON([]byte("not json")); err == nil {
		t.Fatalf("parseWhisperXJSON(malformed) error = nil, want error")
	}
}

func TestFormatDiarizedTxtGroupsConsecutiveSpeaker(t *testing.T) {
	segs, err := parseWhisperXJSON([]byte(realisticWhisperXJSON))
	if err != nil {
		t.Fatalf("parseWhisperXJSON: %v", err)
	}
	got := formatDiarizedTxt(segs)
	want := "[SPEAKER_00] Buongiorno a tutti, iniziamo la call.\n" +
		"[SPEAKER_01] Certo, procediamo pure. Il punto principale oggi è la migrazione.\n" +
		"[SPEAKER_00] Perfetto.\n"
	if got != want {
		t.Errorf("formatDiarizedTxt() = %q, want %q", got, want)
	}
}

func TestFormatDiarizedTxtUnknownSpeaker(t *testing.T) {
	segs := []Segment{{Speaker: "", Start: 0, End: 1, Text: "hi"}}
	got := formatDiarizedTxt(segs)
	if !strings.Contains(got, "[SPEAKER_UNKNOWN] hi") {
		t.Errorf("formatDiarizedTxt() = %q, want it to contain [SPEAKER_UNKNOWN] hi", got)
	}
}

func TestFormatDiarizedSrt(t *testing.T) {
	segs, err := parseWhisperXJSON([]byte(realisticWhisperXJSON))
	if err != nil {
		t.Fatalf("parseWhisperXJSON: %v", err)
	}
	got := formatDiarizedSrt(segs)
	want := "1\n00:00:00,512 --> 00:00:02,834\n[SPEAKER_00] Buongiorno a tutti, iniziamo la call.\n\n" +
		"2\n00:00:03,100 --> 00:00:06,750\n[SPEAKER_01] Certo, procediamo pure. Il punto principale oggi è la migrazione.\n\n" +
		"3\n00:00:07,000 --> 00:00:08,000\n[SPEAKER_00] Perfetto.\n\n"
	if got != want {
		t.Errorf("formatDiarizedSrt() =\n%q\nwant\n%q", got, want)
	}
}

func TestSrtTimestamp(t *testing.T) {
	tests := []struct {
		seconds float64
		want    string
	}{
		{0, "00:00:00,000"},
		{2.834, "00:00:02,834"},
		{3661.5, "01:01:01,500"},
	}
	for _, tt := range tests {
		if got := srtTimestamp(tt.seconds); got != tt.want {
			t.Errorf("srtTimestamp(%v) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

// fakeWhisperXSuccessScript stands in for a whisperx run that succeeds: it
// writes a JSON file at <output_dir>/<basename(audio)>.json -- the naming
// whisperx's own --output_dir/--output_format json produces -- then exits 0.
const fakeWhisperXSuccessScript = `#!/bin/sh
audio="$1"
shift
outdir=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--output_dir" ]; then
    outdir="$2"
  fi
  shift
done
base=$(basename "$audio")
base="${base%.*}"
cat > "$outdir/$base.json" <<'JSON'
` + realisticWhisperXJSON + `
JSON
`

// writeFakeWhisperX also stubs ffmpeg alongside whisperx (CheckPrereqs'
// whisperx branch requires both) so tests using it don't depend on whether
// the machine running them actually has ffmpeg installed.
func writeFakeWhisperX(t *testing.T, script string) {
	t.Helper()
	scriptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scriptDir, "whisperx"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake whisperx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "ffmpeg"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))
}

func TestStartWhisperXSuccessWritesDiarizedTranscript(t *testing.T) {
	writeFakeWhisperX(t, fakeWhisperXSuccessScript)

	outPrefix := filepath.Join(t.TempDir(), "transcript")
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	job, err := StartWhisperX(context.Background(), "hf_test", "it", "large-v3-turbo", outPrefix, wavPath)
	if err != nil {
		t.Fatalf("StartWhisperX: %v", err)
	}
	go func() {
		for range job.Lines() {
		}
	}()
	if err := <-job.Wait(); err != nil {
		t.Fatalf("Wait(): %v", err)
	}

	got, err := os.ReadFile(outPrefix + ".txt")
	if err != nil {
		t.Fatalf("read transcript.txt: %v", err)
	}
	if !strings.Contains(string(got), "[SPEAKER_00] Buongiorno") {
		t.Errorf("transcript.txt = %q, want it to contain [SPEAKER_00] Buongiorno", got)
	}
	if _, err := os.Stat(outPrefix + ".srt"); err != nil {
		t.Errorf("transcript.srt missing: %v", err)
	}
	if _, statErr := os.Stat(outPrefix + ".partial.txt"); !os.IsNotExist(statErr) {
		t.Errorf(".partial.txt still exists after successful run")
	}
}

// fakeWhisperXHangScript stands in for a whisperx run that never finishes on
// its own -- used to test cancellation/external kill. It execs its sleep
// (rather than forking it) so killing this single process is instantaneous.
const fakeWhisperXHangScript = `#!/bin/sh
exec sleep 5
`

func TestStartWhisperXCancelPreservesExistingTranscript(t *testing.T) {
	writeFakeWhisperX(t, fakeWhisperXHangScript)

	outPrefix := filepath.Join(t.TempDir(), "transcript")
	const oldTxt, oldSrt = "old diarized transcript", "old srt content"
	if err := os.WriteFile(outPrefix+".txt", []byte(oldTxt), 0o644); err != nil {
		t.Fatalf("seed old transcript.txt: %v", err)
	}
	if err := os.WriteFile(outPrefix+".srt", []byte(oldSrt), 0o644); err != nil {
		t.Fatalf("seed old transcript.srt: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	job, err := StartWhisperX(ctx, "hf_test", "it", "large-v3-turbo", outPrefix, wavPath)
	if err != nil {
		t.Fatalf("StartWhisperX: %v", err)
	}
	go func() {
		for range job.Lines() {
		}
	}()

	time.Sleep(100 * time.Millisecond) // let the fake process actually start
	cancel()

	if err := <-job.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() = %v, want context.Canceled", err)
	}

	gotTxt, err := os.ReadFile(outPrefix + ".txt")
	if err != nil || string(gotTxt) != oldTxt {
		t.Errorf("transcript.txt = %q, %v, want untouched %q", gotTxt, err, oldTxt)
	}
	gotSrt, err := os.ReadFile(outPrefix + ".srt")
	if err != nil || string(gotSrt) != oldSrt {
		t.Errorf("transcript.srt = %q, %v, want untouched %q", gotSrt, err, oldSrt)
	}
	if _, statErr := os.Stat(outPrefix + ".partial.txt"); !os.IsNotExist(statErr) {
		t.Errorf(".partial.txt exists after cancel")
	}
}

// TestStartWhisperXExternalSIGKILLPreservesExistingTranscript is the
// crash-safety regression test: an external `kill -9` of the whisperx
// subprocess -- a real crash, not nastro's own graceful ctx-cancel path --
// must be observed the same way a cancel is (Job.Wait() returns a non-nil
// error, never hangs) and must leave any pre-existing transcript completely
// untouched, exactly like TestStartWhisperCancelPreservesExistingTranscript
// does for the whisper-cli backend.
func TestStartWhisperXExternalSIGKILLPreservesExistingTranscript(t *testing.T) {
	writeFakeWhisperX(t, fakeWhisperXHangScript)

	outPrefix := filepath.Join(t.TempDir(), "transcript")
	const oldTxt, oldSrt = "old diarized transcript, pre-crash", "old srt, pre-crash"
	if err := os.WriteFile(outPrefix+".txt", []byte(oldTxt), 0o644); err != nil {
		t.Fatalf("seed old transcript.txt: %v", err)
	}
	if err := os.WriteFile(outPrefix+".srt", []byte(oldSrt), 0o644); err != nil {
		t.Fatalf("seed old transcript.srt: %v", err)
	}

	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	// A real, uncancelled context: nastro never asked for this run to stop.
	job, err := StartWhisperX(context.Background(), "hf_test", "it", "large-v3-turbo", outPrefix, wavPath)
	if err != nil {
		t.Fatalf("StartWhisperX: %v", err)
	}
	go func() {
		for range job.Lines() {
		}
	}()

	time.Sleep(100 * time.Millisecond) // let the fake process actually start
	if err := job.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL whisperx process: %v", err)
	}

	select {
	case err := <-job.Wait():
		if err == nil {
			t.Fatalf("Wait() = nil after external SIGKILL, want a non-nil error")
		}
		if errors.Is(err, context.Canceled) {
			t.Errorf("Wait() = context.Canceled, want a plain kill/signal error (nastro's ctx was never canceled)")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Wait() never returned after external SIGKILL")
	}

	gotTxt, err := os.ReadFile(outPrefix + ".txt")
	if err != nil || string(gotTxt) != oldTxt {
		t.Errorf("transcript.txt = %q, %v, want untouched %q", gotTxt, err, oldTxt)
	}
	gotSrt, err := os.ReadFile(outPrefix + ".srt")
	if err != nil || string(gotSrt) != oldSrt {
		t.Errorf("transcript.srt = %q, %v, want untouched %q", gotSrt, err, oldSrt)
	}
}

func TestCheckPrereqsWhisperX(t *testing.T) {
	// Neither whisperx nor uv anywhere nastro looks: guided message.
	t.Run("whisperx missing, uv missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		t.Setenv("HOME", t.TempDir())
		withExtraToolDirs(t, nil)
		status := CheckPrereqs(config.Config{Transcriber: "whisperx"})
		if status.Install != nil {
			t.Errorf("Install = %+v, want nil (no installer found)", status.Install)
		}
		want := WhisperXMissingMsg()
		if status.MissingMsg != want {
			t.Errorf("MissingMsg = %q, want %q", status.MissingMsg, want)
		}
	})

	// whisperx missing, but uv is there: offer to install instead of a bare
	// error message -- the P2 flow.
	t.Run("whisperx missing, uv found", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		home := t.TempDir()
		t.Setenv("HOME", home)
		withExtraToolDirs(t, nil)
		localBin := filepath.Join(home, ".local", "bin")
		if err := os.MkdirAll(localBin, 0o755); err != nil {
			t.Fatalf("mkdir ~/.local/bin: %v", err)
		}
		uvPath := filepath.Join(localBin, "uv")
		if err := os.WriteFile(uvPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write fake uv: %v", err)
		}

		status := CheckPrereqs(config.Config{Transcriber: "whisperx"})
		if status.MissingMsg != "" {
			t.Errorf("MissingMsg = %q, want empty (uv found, install offer instead)", status.MissingMsg)
		}
		if status.Install == nil {
			t.Fatalf("Install = nil, want an offer to install whisperx with uv")
		}
		if status.Install.Tool != "whisperx" || status.Install.Installer != "uv" || status.Install.InstallerPath != uvPath {
			t.Errorf("Install = %+v, want Tool=whisperx Installer=uv InstallerPath=%q", status.Install, uvPath)
		}
		wantArgs := []string{"tool", "install", "whisperx"}
		if strings.Join(status.Install.Args, " ") != strings.Join(wantArgs, " ") {
			t.Errorf("Install.Args = %v, want %v", status.Install.Args, wantArgs)
		}
	})

	// whisperx found, but ffmpeg (which whisperx itself shells out to) isn't
	// -- brew is there, so nastro offers to install it, same as any other
	// missing binary.
	t.Run("whisperx present, ffmpeg missing, brew found", func(t *testing.T) {
		scriptDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(scriptDir, "whisperx"), []byte(fakeWhisperXHangScript), 0o755); err != nil {
			t.Fatalf("write fake whisperx: %v", err)
		}
		t.Setenv("PATH", scriptDir) // no real PATH leaking in: ffmpeg must be absent
		home := t.TempDir()
		t.Setenv("HOME", home)
		withExtraToolDirs(t, nil)
		localBin := filepath.Join(home, ".local", "bin")
		if err := os.MkdirAll(localBin, 0o755); err != nil {
			t.Fatalf("mkdir ~/.local/bin: %v", err)
		}
		brewPath := filepath.Join(localBin, "brew")
		if err := os.WriteFile(brewPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write fake brew: %v", err)
		}

		status := CheckPrereqs(config.Config{Transcriber: "whisperx", HFToken: "hf_x"})
		if status.MissingMsg != "" {
			t.Errorf("MissingMsg = %q, want empty (brew found, install offer instead)", status.MissingMsg)
		}
		if status.Install == nil {
			t.Fatalf("Install = nil, want an offer to install ffmpeg with brew")
		}
		if status.Install.Tool != "ffmpeg" || status.Install.Installer != "brew" || status.Install.InstallerPath != brewPath {
			t.Errorf("Install = %+v, want Tool=ffmpeg Installer=brew InstallerPath=%q", status.Install, brewPath)
		}
		wantArgs := []string{"install", "ffmpeg"}
		if strings.Join(status.Install.Args, " ") != strings.Join(wantArgs, " ") {
			t.Errorf("Install.Args = %v, want %v", status.Install.Args, wantArgs)
		}
	})

	t.Run("whisperx present, ffmpeg missing, brew missing", func(t *testing.T) {
		scriptDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(scriptDir, "whisperx"), []byte(fakeWhisperXHangScript), 0o755); err != nil {
			t.Fatalf("write fake whisperx: %v", err)
		}
		t.Setenv("PATH", scriptDir)
		t.Setenv("HOME", t.TempDir())
		withExtraToolDirs(t, nil)

		status := CheckPrereqs(config.Config{Transcriber: "whisperx", HFToken: "hf_x"})
		if status.Install != nil {
			t.Errorf("Install = %+v, want nil (no brew found)", status.Install)
		}
		want := ffmpegMissingMsg()
		if status.MissingMsg != want {
			t.Errorf("MissingMsg = %q, want %q", status.MissingMsg, want)
		}
	})

	t.Run("whisperx present, hf token missing", func(t *testing.T) {
		writeFakeWhisperX(t, fakeWhisperXHangScript)
		t.Setenv("HF_TOKEN", "")
		status := CheckPrereqs(config.Config{Transcriber: "whisperx", HFToken: ""})
		if status.MissingMsg != MissingHFTokenMessage() {
			t.Errorf("MissingMsg = %q, want the guided HF token message", status.MissingMsg)
		}
	})

	t.Run("whisperx present, token via env", func(t *testing.T) {
		writeFakeWhisperX(t, fakeWhisperXHangScript)
		t.Setenv("HF_TOKEN", "hf_from_env")
		status := CheckPrereqs(config.Config{Transcriber: "whisperx"})
		if status.MissingMsg != "" {
			t.Errorf("MissingMsg = %q, want empty (token resolved from env)", status.MissingMsg)
		}
	})

	t.Run("whisperx present, token in config", func(t *testing.T) {
		writeFakeWhisperX(t, fakeWhisperXHangScript)
		status := CheckPrereqs(config.Config{Transcriber: "whisperx", HFToken: "hf_from_config"})
		if status.MissingMsg != "" {
			t.Errorf("MissingMsg = %q, want empty", status.MissingMsg)
		}
	})
}

func TestStartTranscribeBackendDispatch(t *testing.T) {
	t.Run("whisperx", func(t *testing.T) {
		writeFakeWhisperX(t, fakeWhisperXSuccessScript)
		outPrefix := filepath.Join(t.TempDir(), "transcript")
		wavPath := filepath.Join(t.TempDir(), "audio.wav")
		job, err := StartTranscribeBackend(context.Background(), config.Config{Transcriber: "whisperx", HFToken: "hf_test", Lang: "it", WhisperModel: "large-v3-turbo"}, "/home/user", outPrefix, wavPath)
		if err != nil {
			t.Fatalf("StartTranscribeBackend: %v", err)
		}
		go func() {
			for range job.Lines() {
			}
		}()
		if err := <-job.Wait(); err != nil {
			t.Fatalf("Wait(): %v", err)
		}
		if _, err := os.Stat(outPrefix + ".txt"); err != nil {
			t.Errorf("transcript.txt missing: %v", err)
		}
	})

	t.Run("whisper-cli", func(t *testing.T) {
		scriptDir := t.TempDir()
		script := strings.Replace(fakeWhisperSuccessScript, "ARGSFILE", filepath.Join(scriptDir, "args.txt"), 1)
		if err := os.WriteFile(filepath.Join(scriptDir, "whisper-cli"), []byte(script), 0o755); err != nil {
			t.Fatalf("write fake whisper-cli: %v", err)
		}
		t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))

		outPrefix := filepath.Join(t.TempDir(), "transcript")
		job, err := StartTranscribeBackend(context.Background(), config.Config{Transcriber: "whisper-cli", Lang: "en", WhisperModel: "small"}, "/home/user", outPrefix, "audio.wav")
		if err != nil {
			t.Fatalf("StartTranscribeBackend: %v", err)
		}
		if err := <-job.Wait(); err != nil {
			t.Fatalf("Wait(): %v", err)
		}
		if _, err := os.Stat(outPrefix + ".txt"); err != nil {
			t.Errorf("transcript.txt missing: %v", err)
		}
	})
}
