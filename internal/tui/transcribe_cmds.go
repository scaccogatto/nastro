package tui

import (
	"context"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/records"
	"github.com/scaccogatto/nastro/internal/transcribe"
)

// transcribePrereqMsg reports whether the configured backend (whisper-cli
// or whisperx) is ready to transcribe, before actually starting a run.
type transcribePrereqMsg struct {
	missingMsg   string // non-empty: guided message, route to modeTranscribeMissingPrereq
	modelMissing bool
	modelPath    string
}

// checkTranscribePrereqsCmd checks the configured backend's prerequisites in
// one round-trip, so the TUI can route to the right screen (message, model
// download offer, or straight to transcribing).
func checkTranscribePrereqsCmd(cfg config.Config) tea.Cmd {
	return func() tea.Msg {
		status := transcribe.CheckPrereqs(cfg)
		return transcribePrereqMsg{missingMsg: status.MissingMsg, modelMissing: status.ModelMissing, modelPath: status.ModelPath}
	}
}

// downloadTickMsg carries a progress update from an in-flight model
// download.
type downloadTickMsg struct{ pct float64 }

// downloadDoneMsg is delivered once, when the download finishes (err nil)
// or fails (including context.Canceled, from an explicit cancel).
type downloadDoneMsg struct{ err error }

// awaitDownloadCmd is the model-download heartbeat: same awaitOrTick shape
// as recording and transcribing, re-issued after every tick.
func awaitDownloadCmd(job *transcribe.DownloadJob) tea.Cmd {
	return func() tea.Msg {
		for {
			select {
			case err := <-job.Wait():
				return downloadDoneMsg{err: err}
			case p, ok := <-job.Progress():
				if !ok {
					continue // closed right as Wait() becomes ready; loop picks it up
				}
				return downloadTickMsg{pct: transcribe.DownloadPercent(p.Downloaded, p.Total)}
			}
		}
	}
}

// transcribeStartedMsg is delivered once afconvert + whisper-cli have been
// kicked off (or failed to start, including via cancellation: err wraps
// context.Canceled then).
type transcribeStartedMsg struct {
	job        *transcribe.Job
	tmpWavPath string
	err        error
}

// startTranscribeRunCmd converts rec's audio to WAV and starts whisper-cli
// over it. tmpWavPath is only removed once the whisper-cli job is observed
// to finish (see the Model's transcribeDoneMsg handling), since it's still
// being read by the subprocess until then. Canceling ctx aborts whichever of
// the two subprocesses is running.
func startTranscribeRunCmd(ctx context.Context, cfg config.Config, rec records.Record) tea.Cmd {
	return func() tea.Msg {
		home, err := os.UserHomeDir()
		if err != nil {
			return transcribeStartedMsg{err: err}
		}

		recordDir := filepath.Join(cfg.OutputDir, rec.ID)
		tmpWav, err := os.CreateTemp("", "nastro-transcribe-*.wav")
		if err != nil {
			return transcribeStartedMsg{err: err}
		}
		tmpWavPath := tmpWav.Name()
		tmpWav.Close()

		if err := transcribe.ConvertToWav(ctx, filepath.Join(recordDir, "audio.m4a"), tmpWavPath); err != nil {
			os.Remove(tmpWavPath)
			return transcribeStartedMsg{err: err}
		}

		job, err := transcribe.StartTranscribeBackend(ctx, cfg, home, filepath.Join(recordDir, "transcript"), tmpWavPath)
		if err != nil {
			os.Remove(tmpWavPath)
			return transcribeStartedMsg{err: err}
		}
		return transcribeStartedMsg{job: job, tmpWavPath: tmpWavPath}
	}
}

// transcribeLineMsg carries one line of whisper-cli's combined stdout/stderr.
type transcribeLineMsg struct{ line string }

// transcribeDoneMsg is delivered once, when whisper-cli exits.
type transcribeDoneMsg struct{ err error }

// awaitTranscribeCmd is whisper-cli's heartbeat, mirroring awaitCmd.
func awaitTranscribeCmd(job *transcribe.Job) tea.Cmd {
	return func() tea.Msg {
		for {
			select {
			case err := <-job.Wait():
				return transcribeDoneMsg{err: err}
			case line, ok := <-job.Lines():
				if !ok {
					continue // closed right as Wait() becomes ready; loop picks it up
				}
				return transcribeLineMsg{line: line}
			}
		}
	}
}
