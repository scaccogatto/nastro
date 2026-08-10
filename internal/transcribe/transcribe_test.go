package transcribe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/records"
)

func TestResolveRecord(t *testing.T) {
	older := records.Record{ID: "2026-08-05-1000-standup", Date: time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)}
	newer := records.Record{ID: "2026-08-06-1430-cliente-eppi", Date: time.Date(2026, 8, 6, 14, 30, 0, 0, time.UTC)}
	all := []records.Record{older, newer}

	tests := []struct {
		name     string
		records  []records.Record
		idOrLast string
		want     records.Record
		wantErr  bool
	}{
		{
			name:     "last picks most recent regardless of slice order",
			records:  all,
			idOrLast: "last",
			want:     newer,
		},
		{
			name:     "exact id match",
			records:  all,
			idOrLast: "2026-08-05-1000-standup",
			want:     older,
		},
		{
			name:     "unknown id errors",
			records:  all,
			idOrLast: "does-not-exist",
			wantErr:  true,
		},
		{
			name:     "last on empty slice errors",
			records:  nil,
			idOrLast: "last",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveRecord(tt.records, tt.idOrLast)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveRecord() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveRecord() unexpected error: %v", err)
			}
			if got.ID != tt.want.ID {
				t.Errorf("ResolveRecord() = %q, want %q", got.ID, tt.want.ID)
			}
		})
	}
}

func TestModelPath(t *testing.T) {
	got := ModelPath("/home/user", "large-v3-turbo")
	want := "/home/user/.cache/whisper/ggml-large-v3-turbo.bin"
	if got != want {
		t.Errorf("ModelPath() = %q, want %q", got, want)
	}
}

func TestModelDownloadURL(t *testing.T) {
	got := ModelDownloadURL("large-v3-turbo")
	want := "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin"
	if got != want {
		t.Errorf("ModelDownloadURL() = %q, want %q", got, want)
	}
}

func TestDownloadPercent(t *testing.T) {
	tests := []struct {
		name       string
		downloaded int64
		total      int64
		want       float64
	}{
		{"halfway", 50, 100, 0.5},
		{"complete", 100, 100, 1},
		{"unknown total", 50, 0, 0},
		{"negative total", 50, -1, 0},
		{"overshoot clamps to 1", 150, 100, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DownloadPercent(tt.downloaded, tt.total)
			if got != tt.want {
				t.Errorf("DownloadPercent(%d, %d) = %v, want %v", tt.downloaded, tt.total, got, tt.want)
			}
		})
	}
}

func TestParseWhisperProgress(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantPct int
		wantOK  bool
	}{
		{"typical line", "whisper_print_progress_callback: progress = 45%", 45, true},
		{"zero", "whisper_print_progress_callback: progress = 0%", 0, true},
		{"complete", "whisper_print_progress_callback: progress = 100%", 100, true},
		{"leading/trailing whitespace", "  whisper_print_progress_callback: progress = 12%  ", 12, true},
		{"unrelated line", "whisper_model_load: n_mels = 128", 0, false},
		{"blank line", "", 0, false},
		{"malformed percentage", "whisper_print_progress_callback: progress = abc%", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPct, gotOK := ParseWhisperProgress(tt.line)
			if gotOK != tt.wantOK {
				t.Fatalf("ParseWhisperProgress(%q) ok = %v, want %v", tt.line, gotOK, tt.wantOK)
			}
			if gotOK && gotPct != tt.wantPct {
				t.Errorf("ParseWhisperProgress(%q) pct = %d, want %d", tt.line, gotPct, tt.wantPct)
			}
		})
	}
}

func TestFriendlyTranscribeError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		output    string
		wantEmpty bool
		wantParts []string
	}{
		{
			name:      "nil error",
			err:       nil,
			wantEmpty: true,
		},
		{
			name:      "afconvert failure",
			err:       errors.New("afconvert: exit status 1: ExtAudioFile: bad property size"),
			wantParts: []string{"audio conversion failed", "may be corrupted or empty", "afconvert: exit status 1: ExtAudioFile: bad property size"},
		},
		{
			name:      "whisper failure mentioning memory",
			err:       errors.New("exit status 134"),
			output:    "ggml_metal: failed to allocate memory for model buffer",
			wantParts: []string{"try a smaller model", "~/.config/nastro/config.toml", "exit status 134"},
		},
		{
			name:      "whisper failure mentioning model in the error itself",
			err:       errors.New("model file could not be loaded"),
			wantParts: []string{"try a smaller model", "~/.config/nastro/config.toml"},
		},
		{
			name:      "whisper failure, no known cause",
			err:       errors.New("exit status 1"),
			output:    "some unrelated whisper-cli chatter",
			wantParts: []string{"exit status 1", "some unrelated whisper-cli chatter"},
		},
		{
			name:      "whisper failure with no captured output",
			err:       errors.New("exit status 1"),
			wantParts: []string{"exit status 1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FriendlyTranscribeError(tt.err, tt.output)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("FriendlyTranscribeError() = %q, want empty", got)
				}
				return
			}
			for _, part := range tt.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("FriendlyTranscribeError() = %q, want it to contain %q", got, part)
				}
			}
			if strings.Contains(got, "exit status 1: exit status 1") {
				t.Errorf("FriendlyTranscribeError() = %q, looks duplicated", got)
			}
		})
	}
}

// fakeWhisperSuccessScript stands in for a whisper-cli run that succeeds: it
// captures its full argv to argsFile (before shift consumes it) and writes
// dummy output at the prefix passed via -of, then exits 0 -- enough for
// StartWhisper's finalizeTranscript rename to have something to promote.
const fakeWhisperSuccessScript = `#!/bin/sh
echo "$*" > ARGSFILE
prefix=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-of" ]; then
    prefix="$2"
  fi
  shift
done
echo done > "${prefix}.txt"
echo done > "${prefix}.srt"
`

func TestStartWhisperEnablesPrintProgress(t *testing.T) {
	scriptDir := t.TempDir()
	argsFile := filepath.Join(scriptDir, "args.txt")
	script := strings.Replace(fakeWhisperSuccessScript, "ARGSFILE", argsFile, 1)
	if err := os.WriteFile(filepath.Join(scriptDir, "whisper-cli"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake whisper-cli: %v", err)
	}
	t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))

	outPrefix := filepath.Join(t.TempDir(), "transcript")
	job, err := StartWhisper(context.Background(), "model.bin", "en", outPrefix, "audio.wav")
	if err != nil {
		t.Fatalf("StartWhisper: %v", err)
	}
	if err := <-job.Wait(); err != nil {
		t.Fatalf("Wait(): %v", err)
	}

	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	if !strings.Contains(string(got), "-pp") {
		t.Errorf("whisper-cli args = %q, want it to contain -pp (--print-progress)", got)
	}
}

// TestStartWhisperSuccessFinalizesPartialOutput verifies the H3 fix: a
// successful run's partial output is renamed (not left) at outPrefix, and
// no .partial.txt/.srt is left behind.
func TestStartWhisperSuccessFinalizesPartialOutput(t *testing.T) {
	scriptDir := t.TempDir()
	script := strings.Replace(fakeWhisperSuccessScript, "ARGSFILE", filepath.Join(scriptDir, "args.txt"), 1)
	if err := os.WriteFile(filepath.Join(scriptDir, "whisper-cli"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake whisper-cli: %v", err)
	}
	t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))

	outPrefix := filepath.Join(t.TempDir(), "transcript")
	job, err := StartWhisper(context.Background(), "model.bin", "en", outPrefix, "audio.wav")
	if err != nil {
		t.Fatalf("StartWhisper: %v", err)
	}
	if err := <-job.Wait(); err != nil {
		t.Fatalf("Wait(): %v", err)
	}

	if _, statErr := os.Stat(outPrefix + ".txt"); statErr != nil {
		t.Errorf("outPrefix.txt missing after successful run: %v", statErr)
	}
	if _, statErr := os.Stat(outPrefix + ".srt"); statErr != nil {
		t.Errorf("outPrefix.srt missing after successful run: %v", statErr)
	}
	if _, statErr := os.Stat(outPrefix + ".partial.txt"); !os.IsNotExist(statErr) {
		t.Errorf(".partial.txt still exists after successful run")
	}
	if _, statErr := os.Stat(outPrefix + ".partial.srt"); !os.IsNotExist(statErr) {
		t.Errorf(".partial.srt still exists after successful run")
	}
}

func TestStartModelDownloadSuccess(t *testing.T) {
	content := strings.Repeat("nastro-model-bytes", 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		_, _ = w.Write([]byte(content))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "model.bin")
	job := StartModelDownload(srv.URL, dest)

	var lastPct float64
	for p := range job.Progress() {
		lastPct = DownloadPercent(p.Downloaded, p.Total)
	}
	if err := <-job.Wait(); err != nil {
		t.Fatalf("download error: %v", err)
	}
	if lastPct != 1 {
		t.Errorf("last progress tick = %v, want 1 (complete)", lastPct)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != content {
		t.Errorf("downloaded content mismatch: got %d bytes, want %d", len(got), len(content))
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part file still exists after successful download")
	}
}

func TestStartModelDownloadCancelCleansUpPartFile(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("some-bytes-before-cancel"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-block // keep the connection open until the test cancels
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	dest := filepath.Join(t.TempDir(), "model.bin")
	job := StartModelDownload(srv.URL, dest)

	// Wait for at least one progress tick so we know the .part file exists,
	// then cancel.
	<-job.Progress()
	job.Cancel()

	err := <-job.Wait()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("download error = %v, want context.Canceled", err)
	}

	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("destination file exists after cancel")
	}
	if _, statErr := os.Stat(dest + ".part"); !os.IsNotExist(statErr) {
		t.Errorf(".part file still exists after cancel")
	}
}

// fakeWhisperScript stands in for whisper-cli: it writes partial .txt/.srt
// output (whisper-cli writes incrementally too) at the prefix passed via
// -of, then sleeps, so tests can cancel mid-run and assert the partial
// output gets cleaned up.
// It execs its final sleep (rather than forking it) so killing the script's
// single process is instantaneous: a forked-but-not-exec'd child would
// inherit the stdout/stderr pipe fd and keep it open past the kill, making
// cmd.Wait() block until the child exits on its own.
const fakeWhisperScript = `#!/bin/sh
prefix=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-of" ]; then
    prefix="$2"
  fi
  shift
done
echo partial > "${prefix}.txt"
echo partial > "${prefix}.srt"
exec sleep 5
`

func TestStartWhisperCancelCleansUpPartialOutput(t *testing.T) {
	scriptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scriptDir, "whisper-cli"), []byte(fakeWhisperScript), 0o755); err != nil {
		t.Fatalf("write fake whisper-cli: %v", err)
	}
	t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))

	outPrefix := filepath.Join(t.TempDir(), "transcript")
	ctx, cancel := context.WithCancel(context.Background())
	job, err := StartWhisper(ctx, "model.bin", "en", outPrefix, "audio.wav")
	if err != nil {
		t.Fatalf("StartWhisper: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, statErr := os.Stat(outPrefix + ".partial.txt"); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake whisper-cli never wrote partial output")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	if err := <-job.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(outPrefix + ".partial.txt"); !os.IsNotExist(statErr) {
		t.Errorf("partial .txt still exists after cancel")
	}
	if _, statErr := os.Stat(outPrefix + ".partial.srt"); !os.IsNotExist(statErr) {
		t.Errorf("partial .srt still exists after cancel")
	}
	if _, statErr := os.Stat(outPrefix + ".txt"); !os.IsNotExist(statErr) {
		t.Errorf("final .txt should never have been created by a canceled run")
	}
}

// TestStartWhisperCancelPreservesExistingTranscript is the H3 regression
// test: canceling a re-transcribe over a record that already has a
// transcript must leave that old transcript completely untouched -- never
// even briefly overwritten -- and only the .partial temp files cleaned up.
func TestStartWhisperCancelPreservesExistingTranscript(t *testing.T) {
	scriptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scriptDir, "whisper-cli"), []byte(fakeWhisperScript), 0o755); err != nil {
		t.Fatalf("write fake whisper-cli: %v", err)
	}
	t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))

	outPrefix := filepath.Join(t.TempDir(), "transcript")
	const oldTxt, oldSrt = "the old, previously-transcribed content", "old srt content"
	if err := os.WriteFile(outPrefix+".txt", []byte(oldTxt), 0o644); err != nil {
		t.Fatalf("seed old transcript.txt: %v", err)
	}
	if err := os.WriteFile(outPrefix+".srt", []byte(oldSrt), 0o644); err != nil {
		t.Fatalf("seed old transcript.srt: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	job, err := StartWhisper(ctx, "model.bin", "en", outPrefix, "audio.wav")
	if err != nil {
		t.Fatalf("StartWhisper: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, statErr := os.Stat(outPrefix + ".partial.txt"); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake whisper-cli never wrote partial output")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	if err := <-job.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() = %v, want context.Canceled", err)
	}

	gotTxt, err := os.ReadFile(outPrefix + ".txt")
	if err != nil {
		t.Fatalf("read old transcript.txt after cancel: %v", err)
	}
	if string(gotTxt) != oldTxt {
		t.Errorf("transcript.txt = %q after canceled re-transcribe, want the untouched old content %q", gotTxt, oldTxt)
	}
	gotSrt, err := os.ReadFile(outPrefix + ".srt")
	if err != nil {
		t.Fatalf("read old transcript.srt after cancel: %v", err)
	}
	if string(gotSrt) != oldSrt {
		t.Errorf("transcript.srt = %q after canceled re-transcribe, want the untouched old content %q", gotSrt, oldSrt)
	}
	if _, statErr := os.Stat(outPrefix + ".partial.txt"); !os.IsNotExist(statErr) {
		t.Errorf(".partial.txt still exists after cancel")
	}
	if _, statErr := os.Stat(outPrefix + ".partial.srt"); !os.IsNotExist(statErr) {
		t.Errorf(".partial.srt still exists after cancel")
	}
}

func TestConvertToWavCancelReturnsContextCanceled(t *testing.T) {
	scriptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scriptDir, "afconvert"), []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatalf("write fake afconvert: %v", err)
	}
	t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- ConvertToWav(ctx, "in.m4a", "out.wav") }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("ConvertToWav() = %v, want context.Canceled", err)
	}
}

func TestCheckPrereqsWhisperCLI(t *testing.T) {
	t.Run("whisper-cli missing, brew missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		t.Setenv("HOME", t.TempDir())
		withExtraToolDirs(t, nil)

		status := CheckPrereqs(config.Config{Transcriber: "whisper-cli"})
		if status.Install != nil {
			t.Errorf("Install = %+v, want nil (no brew found)", status.Install)
		}
		want := whisperCLIMissingMsg()
		if status.MissingMsg != want {
			t.Errorf("MissingMsg = %q, want %q", status.MissingMsg, want)
		}
	})

	t.Run("whisper-cli missing, brew found", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
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

		status := CheckPrereqs(config.Config{Transcriber: "whisper-cli"})
		if status.MissingMsg != "" {
			t.Errorf("MissingMsg = %q, want empty (brew found, install offer instead)", status.MissingMsg)
		}
		if status.Install == nil {
			t.Fatalf("Install = nil, want an offer to install whisper-cli with brew")
		}
		if status.Install.Tool != "whisper-cli" || status.Install.Installer != "brew" || status.Install.InstallerPath != brewPath {
			t.Errorf("Install = %+v, want Tool=whisper-cli Installer=brew InstallerPath=%q", status.Install, brewPath)
		}
		wantArgs := []string{"install", "whisper-cpp"}
		if strings.Join(status.Install.Args, " ") != strings.Join(wantArgs, " ") {
			t.Errorf("Install.Args = %v, want %v", status.Install.Args, wantArgs)
		}
	})

	t.Run("whisper-cli present, model missing", func(t *testing.T) {
		scriptDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(scriptDir, "whisper-cli"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write fake whisper-cli: %v", err)
		}
		t.Setenv("PATH", scriptDir)
		t.Setenv("HOME", t.TempDir()) // fresh, so ~/.cache/whisper/ggml-tiny.bin is guaranteed absent

		status := CheckPrereqs(config.Config{Transcriber: "whisper-cli", WhisperModel: "tiny"})
		if !status.ModelMissing {
			t.Errorf("ModelMissing = false, want true")
		}
	})
}

// fakeInstallSuccessScript stands in for an installer (uv/brew) that
// streams a couple of lines and exits 0.
const fakeInstallSuccessScript = `#!/bin/sh
echo "Resolved 1 package"
echo "Installed whisperx"
`

func TestStartToolInstallSuccessStreamsOutput(t *testing.T) {
	scriptDir := t.TempDir()
	installerPath := filepath.Join(scriptDir, "uv")
	if err := os.WriteFile(installerPath, []byte(fakeInstallSuccessScript), 0o755); err != nil {
		t.Fatalf("write fake installer: %v", err)
	}

	job, err := StartToolInstall(context.Background(), installerPath, "tool", "install", "whisperx")
	if err != nil {
		t.Fatalf("StartToolInstall: %v", err)
	}
	var lines []string
	for l := range job.Lines() {
		lines = append(lines, l)
	}
	if err := <-job.Wait(); err != nil {
		t.Fatalf("Wait(): %v", err)
	}
	if strings.Join(lines, "\n") != "Resolved 1 package\nInstalled whisperx" {
		t.Errorf("streamed lines = %v, want the installer's two lines", lines)
	}
}

func TestStartToolInstallFailureReportsExitError(t *testing.T) {
	scriptDir := t.TempDir()
	installerPath := filepath.Join(scriptDir, "uv")
	script := "#!/bin/sh\necho some error 1>&2\nexit 1\n"
	if err := os.WriteFile(installerPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake installer: %v", err)
	}

	job, err := StartToolInstall(context.Background(), installerPath, "tool", "install", "whisperx")
	if err != nil {
		t.Fatalf("StartToolInstall: %v", err)
	}
	for range job.Lines() {
	}
	if err := <-job.Wait(); err == nil {
		t.Fatalf("Wait() = nil, want a non-nil error for exit 1")
	}
}

func TestStartToolInstallCancelStopsProcess(t *testing.T) {
	scriptDir := t.TempDir()
	installerPath := filepath.Join(scriptDir, "uv")
	if err := os.WriteFile(installerPath, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatalf("write fake installer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	job, err := StartToolInstall(ctx, installerPath, "tool", "install", "whisperx")
	if err != nil {
		t.Fatalf("StartToolInstall: %v", err)
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
}

func TestFriendlyTranscribeErrorGatedRepo(t *testing.T) {
	out := "huggingface_hub.errors.GatedRepoError: 403 Client Error.\nAccess to model pyannote/speaker-diarization-community-1 is restricted and you are not in the authorized list."
	got := FriendlyTranscribeError(fmt.Errorf("exit status 1"), out)
	if !strings.Contains(got, "accept the model terms") || !strings.Contains(got, "huggingface.co/pyannote/speaker-diarization-community-1") {
		t.Errorf("FriendlyTranscribeError gated repo = %q, want guided accept-terms message", got)
	}
	if strings.Contains(got, "smaller model") {
		t.Errorf("gated repo error must not suggest a smaller model, got %q", got)
	}
}
