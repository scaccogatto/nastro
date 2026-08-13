package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/scaccogatto/nastro/internal/records"
	"github.com/scaccogatto/nastro/internal/transcribe"
)

// jobPhase is the visible stage of a background transcribe job.
type jobPhase int

const (
	jobQueued     jobPhase = iota // waiting for a free slot (max_parallel_transcriptions)
	jobConverting                 // afconvert running, before the backend has produced any output
	jobRunning                    // the backend (whisper-cli/whisperx) running
)

// jobLineCap is how many of a job's most recent output lines are kept, for
// both its filtered (isNoiseLine-dropped) on-screen tail and its unfiltered
// error-detail buffer.
const jobLineCap = 3

// transcribeJobState is one record's in-flight (or queued) transcribe job.
// The Model keeps one of these per record ID in transcribeJobs -- see its
// doc comment on the Model struct for the registry as a whole.
type transcribeJobState struct {
	rec      records.Record
	returnTo screen // where to navigate back to once this job finishes, if its screen is still focused
	phase    jobPhase
	// phaseLabel overrides jobRunning's default headline ("transcribing…")
	// when whisperx reports a more honest stage marker -- see
	// transcribe.WhisperXPhaseLabel. Empty uses the default.
	phaseLabel string
	// percent is 0..100 once known (whisper-cli's --print-progress);
	// -1 for the whole run otherwise (whisperx never reports one).
	percent int
	start   time.Time

	// lines is the filtered tail shown on the job's own screen; rawLines is
	// the unfiltered tail handed to FriendlyTranscribeError on failure (see
	// transcribe.IsNoiseLine's doc comment for why display and error detail
	// deliberately differ).
	lines    []string
	rawLines []string

	cancel     context.CancelFunc
	tmpWavPath string
	job        *transcribe.Job
}

// runningJobCount returns how many jobs currently count against
// cfg.ResolvedMaxParallel() -- everything except still-queued ones.
func (m Model) runningJobCount() int {
	n := 0
	for _, st := range m.transcribeJobs {
		if st.phase != jobQueued {
			n++
		}
	}
	return n
}

// jobStatusSummary renders the list screen's status strip while jobs are
// active ("transcribing 2 · queued 1"), "" once there are none.
func (m Model) jobStatusSummary() string {
	var running, queued int
	for _, st := range m.transcribeJobs {
		if st.phase == jobQueued {
			queued++
		} else {
			running++
		}
	}
	switch {
	case running == 0 && queued == 0:
		return ""
	case queued == 0:
		return fmt.Sprintf("transcribing %d", running)
	case running == 0:
		return fmt.Sprintf("queued %d", queued)
	default:
		return fmt.Sprintf("transcribing %d · queued %d", running, queued)
	}
}

// admitTranscribeJob registers rec's job in the registry -- starting it
// immediately if a slot is free (per cfg.ResolvedMaxParallel()), or leaving
// it queued (FIFO, via transcribeQueue) otherwise -- and focuses the
// transcribing screen on it either way, so the user sees its state (running
// or "queued…") right away. m.transcribeReturn (set by startTranscribe)
// becomes this job's own returnTo.
func (m Model) admitTranscribeJob(rec records.Record) (Model, tea.Cmd) {
	if m.transcribeJobs == nil {
		m.transcribeJobs = map[string]*transcribeJobState{}
	}
	m.transcribeTarget = rec
	m.mode = modeTranscribing
	m.confirmCancelTranscribe = false

	st := &transcribeJobState{rec: rec, returnTo: m.transcribeReturn, percent: -1, start: time.Now()}
	m.transcribeJobs[rec.ID] = st
	if m.runningJobCount() >= m.cfg.ResolvedMaxParallel() {
		st.phase = jobQueued
		m.transcribeQueue = append(m.transcribeQueue, rec.ID)
		return m, nil
	}
	return m.startQueuedJob(rec.ID)
}

// startQueuedJob transitions id's job to running: afconvert + the backend,
// in the background. Callers must already have verified a slot is free.
func (m Model) startQueuedJob(id string) (Model, tea.Cmd) {
	st, ok := m.transcribeJobs[id]
	if !ok {
		return m, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.phase = jobConverting
	st.start = time.Now()
	st.cancel = cancel
	return m, tea.Batch(m.transcribeSpinner.Tick, startTranscribeRunCmd(ctx, m.cfg, st.rec))
}

// promoteNextQueued starts the next FIFO-queued job, if any and if a slot
// is actually free -- called once a job finishes or is canceled.
func (m Model) promoteNextQueued() (Model, tea.Cmd) {
	if len(m.transcribeQueue) == 0 || m.runningJobCount() >= m.cfg.ResolvedMaxParallel() {
		return m, nil
	}
	id := m.transcribeQueue[0]
	m.transcribeQueue = m.transcribeQueue[1:]
	return m.startQueuedJob(id)
}

// removeTranscribeJob drops id from the registry and its queue position (if
// it was still queued).
func (m Model) removeTranscribeJob(id string) Model {
	delete(m.transcribeJobs, id)
	for i, qid := range m.transcribeQueue {
		if qid == id {
			m.transcribeQueue = append(m.transcribeQueue[:i], m.transcribeQueue[i+1:]...)
			break
		}
	}
	return m
}

// cancelTranscribeJob cancels id's job. A still-queued one (nothing running
// yet) is torn down immediately, synchronously; a converting/running one is
// canceled via its context, and finishTranscribeJob runs once its
// transcribeStartedMsg/transcribeDoneMsg reports the cancellation back --
// same path as any other run outcome, so the teardown logic lives in one
// place.
func (m Model) cancelTranscribeJob(id string) (Model, tea.Cmd) {
	st, ok := m.transcribeJobs[id]
	if !ok {
		return m, nil
	}
	if st.phase == jobQueued {
		return m.finishTranscribeJob(id, context.Canceled)
	}
	if st.cancel != nil {
		st.cancel()
	}
	return m, nil
}

// finishTranscribeJob tears down id's job (registry entry + temp WAV),
// promotes the next queued job into its freed slot, and routes the UI: if
// id's screen is the one currently focused, navigate to its returnTo
// (refreshing the detail screen's flag on success) and clear any pending
// cancel confirmation; otherwise the job merely leaves a status line and a
// silent rescan behind, without disturbing whatever screen the user has
// since moved to. A stale id (already removed, e.g. a duplicate message) is
// a no-op.
func (m Model) finishTranscribeJob(id string, err error) (Model, tea.Cmd) {
	st, ok := m.transcribeJobs[id]
	if !ok {
		return m, nil
	}
	rec, returnTo, rawLines := st.rec, st.returnTo, st.rawLines
	if st.tmpWavPath != "" {
		os.Remove(st.tmpWavPath)
	}
	m = m.removeTranscribeJob(id)

	var promoteCmd tea.Cmd
	m, promoteCmd = m.promoteNextQueued()

	focused := m.mode == modeTranscribing && m.transcribeTarget.ID == id

	switch {
	case errors.Is(err, context.Canceled):
		m.statusMsg = "transcription canceled: " + recordDisplayName(rec)
		m.statusIsErr = false
		if focused {
			m.mode = returnTo
			m.confirmCancelTranscribe = false
		}
		return m, tea.Batch(promoteCmd, rescanCmd(m.cfg.OutputDir, m.statusMsg, false))

	case err != nil:
		if focused {
			m.transcribeErr = err
			m.transcribeErrDetail = strings.Join(rawLines, "\n")
			m.mode = modeTranscribeError
			return m, promoteCmd
		}
		m.statusMsg = "transcription failed: " + recordDisplayName(rec)
		m.statusIsErr = true
		return m, promoteCmd

	default:
		var status string
		status, m.tipShown = transcribedStatus(rec.ID, m.cfg.Transcriber, m.tipShown)
		m.statusMsg = status
		m.statusIsErr = false
		if focused && returnTo == modeDetail && m.detailRec.ID == rec.ID {
			m.detailRec.HasTranscript = true
			m.mode = modeDetail
			return m, tea.Batch(promoteCmd, loadDetailCmd(m.detailRec, m.cfg.OutputDir))
		}
		if focused {
			m.mode = returnTo
		}
		return m, tea.Batch(promoteCmd, rescanCmd(m.cfg.OutputDir, status, false))
	}
}

// transcribedStatus renders the list's "transcribed <id>" status line for a
// successful job, appending the whisperx upsell tip (transcribe.TranscribeTip)
// the first time a whisper-cli job completes this session -- tipShown tracks
// that across calls (Model.tipShown), so the tip is never repeated even
// though every whisper-cli completion routes through here. No tip for
// whisperx (it already diarizes) or once tipShown is already true; either
// way nextTipShown just echoes tipShown back unchanged.
func transcribedStatus(id, transcriber string, tipShown bool) (status string, nextTipShown bool) {
	status = "transcribed " + id
	if transcriber == "whisperx" || tipShown {
		return status, tipShown
	}
	return status + " · " + transcribe.TranscribeTip, true
}

// listSpinnerFrames animates the list's fixed-width status column for a job
// whose progress is unknown (whisperx, or whisper-cli before its first
// --print-progress line). Driven by wall-clock time rather than a ticked
// spinner.Model: the list redraws on every keypress/second-tick already, so
// a time-based frame index animates smoothly without a dedicated ticker per
// row.
var listSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func listSpinnerFrame(t time.Time) string {
	return listSpinnerFrames[int(t.UnixMilli()/120)%len(listSpinnerFrames)]
}

// transcribeStatusText renders a record's live status for the list's fixed
// status column: "queued", "NN%", an animated spinner (phase converting, or
// running with an unknown percent), "✓" (already transcribed, no active
// job), or "-" (neither). st is nil when r has no active job.
func transcribeStatusText(r records.Record, st *transcribeJobState, now time.Time) string {
	if st == nil {
		if r.HasTranscript {
			return "✓"
		}
		return "-"
	}
	if st.phase == jobQueued {
		return "queued"
	}
	if st.phase == jobRunning && st.percent >= 0 {
		return fmt.Sprintf("%d%%", st.percent)
	}
	return listSpinnerFrame(now)
}

// jobHeadline renders the transcribing screen's first line for st: its
// phase, a progress bar once a percentage is known, and elapsed time.
func (m Model) jobHeadline(st *transcribeJobState, elapsed time.Duration) string {
	elapsedStr := records.FormatDuration(elapsed.Seconds())
	switch st.phase {
	case jobQueued:
		return "queued… " + elapsedStr
	case jobConverting:
		return m.transcribeSpinner.View() + " preparing audio... " + elapsedStr
	default:
		if st.percent >= 0 {
			return fmt.Sprintf("%s  %s", m.transcribeProgress.ViewAs(float64(st.percent)/100), elapsedStr)
		}
		label := st.phaseLabel
		if label == "" {
			label = "transcribing…"
		}
		return m.transcribeSpinner.View() + " " + label + " " + elapsedStr
	}
}
