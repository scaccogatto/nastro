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
// or whisperx) is ready to transcribe rec, before actually starting a run.
// rec travels with the message (rather than being read back off the Model)
// so a reply can never be misattributed to whatever record happens to be
// m.transcribeTarget by the time it arrives -- list navigation is free
// while jobs run in the background, so a second prereq check can well be
// in flight for a different record before the first one's reply lands.
type transcribePrereqMsg struct {
	rec          records.Record
	missingMsg   string                   // non-empty: guided message, route to modeTranscribeMissingPrereq
	install      *transcribe.InstallOffer // non-nil: offer to install the missing binary
	modelMissing bool
	modelPath    string
}

// checkTranscribePrereqsCmd checks the configured backend's prerequisites in
// one round-trip, so the TUI can route to the right screen (message, tool
// install offer, model download offer, or straight to transcribing) for rec.
func checkTranscribePrereqsCmd(cfg config.Config, rec records.Record) tea.Cmd {
	return func() tea.Msg {
		status := transcribe.CheckPrereqs(cfg)
		return transcribePrereqMsg{rec: rec, missingMsg: status.MissingMsg, install: status.Install, modelMissing: status.ModelMissing, modelPath: status.ModelPath}
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

// installStartedMsg is delivered once a tool-install subprocess (`uv tool
// install whisperx` / `brew install whisper-cpp`) has been kicked off, or
// failed to start. A separate started/running split -- mirroring
// transcribeStartedMsg -- so cmd.Start() never blocks bubbletea's single
// update goroutine.
type installStartedMsg struct {
	job *transcribe.Job
	err error
}

// startToolInstallCmd starts offer's installer command in the background.
func startToolInstallCmd(ctx context.Context, offer transcribe.InstallOffer) tea.Cmd {
	return func() tea.Msg {
		job, err := transcribe.StartToolInstall(ctx, offer.InstallerPath, offer.Args...)
		return installStartedMsg{job: job, err: err}
	}
}

// installLineMsg carries one line of the installer's combined stdout/stderr.
type installLineMsg struct{ line string }

// installDoneMsg is delivered once, when the installer exits.
type installDoneMsg struct{ err error }

// awaitInstallCmd is the tool-install heartbeat, mirroring awaitTranscribeCmd.
func awaitInstallCmd(job *transcribe.Job) tea.Cmd {
	return func() tea.Msg {
		for {
			select {
			case err := <-job.Wait():
				return installDoneMsg{err: err}
			case line, ok := <-job.Lines():
				if !ok {
					continue // closed right as Wait() becomes ready; loop picks it up
				}
				return installLineMsg{line: line}
			}
		}
	}
}

// transcribeStartedMsg is delivered once afconvert + the backend have been
// kicked off (or failed to start, including via cancellation: err wraps
// context.Canceled then) for id's job.
type transcribeStartedMsg struct {
	id         string
	job        *transcribe.Job
	tmpWavPath string
	err        error
}

// startTranscribeRunCmd converts rec's audio to WAV and starts the
// configured backend over it. tmpWavPath is only removed once the job is
// observed to finish (see finishTranscribeJob), since it's still being read
// by the subprocess until then. Canceling ctx aborts whichever of the two
// subprocesses is running.
func startTranscribeRunCmd(ctx context.Context, cfg config.Config, rec records.Record) tea.Cmd {
	return func() tea.Msg {
		home, err := os.UserHomeDir()
		if err != nil {
			return transcribeStartedMsg{id: rec.ID, err: err}
		}

		recordDir := filepath.Join(cfg.OutputDir, rec.ID)
		tmpWav, err := os.CreateTemp("", "nastro-transcribe-*.wav")
		if err != nil {
			return transcribeStartedMsg{id: rec.ID, err: err}
		}
		tmpWavPath := tmpWav.Name()
		tmpWav.Close()

		if err := transcribe.ConvertToWav(ctx, filepath.Join(recordDir, "audio.m4a"), tmpWavPath); err != nil {
			os.Remove(tmpWavPath)
			return transcribeStartedMsg{id: rec.ID, err: err}
		}

		job, err := transcribe.StartTranscribeBackend(ctx, cfg, home, filepath.Join(recordDir, "transcript"), tmpWavPath)
		if err != nil {
			os.Remove(tmpWavPath)
			return transcribeStartedMsg{id: rec.ID, err: err}
		}
		return transcribeStartedMsg{id: rec.ID, job: job, tmpWavPath: tmpWavPath}
	}
}

// transcribeLineMsg carries one line of id's job's combined stdout/stderr.
type transcribeLineMsg struct {
	id   string
	line string
}

// transcribeDoneMsg is delivered once, when id's job exits.
type transcribeDoneMsg struct {
	id  string
	err error
}

// awaitTranscribeCmd is id's job heartbeat, mirroring awaitCmd. One instance
// runs per in-flight job -- bubbletea runs every returned Cmd in its own
// goroutine, so N concurrent jobs simply mean N concurrent loops here.
func awaitTranscribeCmd(id string, job *transcribe.Job) tea.Cmd {
	return func() tea.Msg {
		for {
			select {
			case err := <-job.Wait():
				return transcribeDoneMsg{id: id, err: err}
			case line, ok := <-job.Lines():
				if !ok {
					continue // closed right as Wait() becomes ready; loop picks it up
				}
				return transcribeLineMsg{id: id, line: line}
			}
		}
	}
}
