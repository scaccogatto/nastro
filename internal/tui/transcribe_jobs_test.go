package tui

import (
	"context"
	"testing"
	"time"

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/records"
	"github.com/scaccogatto/nastro/internal/transcribe"
)

func rec(id string) records.Record { return records.Record{ID: id} }

// TestAdmitTranscribeJobStartsImmediatelyUnderCap covers the common case:
// a slot is free, so the job starts converting right away and focuses its
// screen.
func TestAdmitTranscribeJobStartsImmediatelyUnderCap(t *testing.T) {
	m := New(config.Config{OutputDir: t.TempDir(), MaxParallelTranscriptions: 2}, nil)

	nm, cmd := m.admitTranscribeJob(rec("a"))
	if nm.mode != modeTranscribing {
		t.Errorf("mode = %v, want modeTranscribing", nm.mode)
	}
	st, ok := nm.transcribeJobs["a"]
	if !ok {
		t.Fatalf("transcribeJobs[a] missing")
	}
	if st.phase != jobConverting {
		t.Errorf("phase = %v, want jobConverting", st.phase)
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want a batch of spinner tick + startTranscribeRunCmd")
	}
	if st.cancel != nil {
		st.cancel() // avoid leaking the context past the test
	}
}

// TestQueueAdmitsUpToMaxParallelThenQueues is the core registry test: with
// max_parallel_transcriptions = 2, a third job queues instead of starting.
func TestQueueAdmitsUpToMaxParallelThenQueues(t *testing.T) {
	m := New(config.Config{OutputDir: t.TempDir(), MaxParallelTranscriptions: 2}, nil)

	m, _ = m.admitTranscribeJob(rec("a"))
	m, _ = m.admitTranscribeJob(rec("b"))
	m, cmd := m.admitTranscribeJob(rec("c"))

	if got := m.transcribeJobs["a"].phase; got != jobConverting {
		t.Errorf("a.phase = %v, want jobConverting", got)
	}
	if got := m.transcribeJobs["b"].phase; got != jobConverting {
		t.Errorf("b.phase = %v, want jobConverting", got)
	}
	if got := m.transcribeJobs["c"].phase; got != jobQueued {
		t.Errorf("c.phase = %v, want jobQueued", got)
	}
	if cmd != nil {
		t.Errorf("cmd for the queued job = %v, want nil (nothing started yet)", cmd)
	}
	if got := m.runningJobCount(); got != 2 {
		t.Errorf("runningJobCount() = %d, want 2", got)
	}
	if len(m.transcribeQueue) != 1 || m.transcribeQueue[0] != "c" {
		t.Errorf("transcribeQueue = %+v, want [c]", m.transcribeQueue)
	}

	for _, id := range []string{"a", "b"} {
		if cancel := m.transcribeJobs[id].cancel; cancel != nil {
			cancel()
		}
	}
}

// TestQueuePromotesNextOnCompletionOutOfOrder: with 2 running + 1 queued,
// finishing the *second*-started job (not the first) still promotes the
// queued one -- completion order shouldn't matter, only slot availability.
func TestQueuePromotesNextOnCompletionOutOfOrder(t *testing.T) {
	m := New(config.Config{OutputDir: t.TempDir(), MaxParallelTranscriptions: 2}, nil)
	m, _ = m.admitTranscribeJob(rec("a"))
	m, _ = m.admitTranscribeJob(rec("b"))
	m, _ = m.admitTranscribeJob(rec("c"))
	m.transcribeJobs["a"].cancel = func() {}
	m.transcribeJobs["b"].cancel = func() {}

	// "b" (started second) finishes first.
	m, cmd := m.finishTranscribeJob("b", nil)
	if _, ok := m.transcribeJobs["b"]; ok {
		t.Errorf("b still in registry after finishing")
	}
	if st, ok := m.transcribeJobs["c"]; !ok || st.phase != jobConverting {
		t.Errorf("c did not get promoted after b finished; transcribeJobs[c] = %+v, ok=%v", m.transcribeJobs["c"], ok)
	}
	if len(m.transcribeQueue) != 0 {
		t.Errorf("transcribeQueue = %+v, want empty after promotion", m.transcribeQueue)
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want the promoted job's start cmd")
	}

	// "a" finishing afterward must not disturb "c", already running.
	m, _ = m.finishTranscribeJob("a", nil)
	if st, ok := m.transcribeJobs["c"]; !ok || st.phase != jobConverting {
		t.Errorf("c disturbed by an unrelated completion: %+v, ok=%v", st, ok)
	}

	if cancel := m.transcribeJobs["c"].cancel; cancel != nil {
		cancel()
	}
}

// TestCancelOneJobDoesNotAffectOthers: canceling one of several concurrent
// jobs leaves the others completely untouched.
func TestCancelOneJobDoesNotAffectOthers(t *testing.T) {
	m := New(config.Config{OutputDir: t.TempDir(), MaxParallelTranscriptions: 2}, nil)
	m, _ = m.admitTranscribeJob(rec("a"))
	m, _ = m.admitTranscribeJob(rec("b"))

	aCanceled := false
	m.transcribeJobs["a"].cancel = func() { aCanceled = true }
	bCancel := m.transcribeJobs["b"].cancel

	m, _ = m.cancelTranscribeJob("a")
	if !aCanceled {
		t.Errorf("a's cancel func was not called")
	}
	if _, ok := m.transcribeJobs["b"]; !ok {
		t.Errorf("b removed from registry by canceling a")
	}
	if m.transcribeJobs["b"].phase != jobConverting {
		t.Errorf("b.phase = %v, want jobConverting (untouched)", m.transcribeJobs["b"].phase)
	}

	if bCancel != nil {
		bCancel()
	}
}

// TestCancelQueuedJobRemovesImmediately: a queued job has no subprocess to
// wait for, so canceling it removes it from the registry synchronously.
func TestCancelQueuedJobRemovesImmediately(t *testing.T) {
	m := New(config.Config{OutputDir: t.TempDir(), MaxParallelTranscriptions: 1}, nil)
	m, _ = m.admitTranscribeJob(rec("a"))
	m, _ = m.admitTranscribeJob(rec("b")) // queues, a already holds the one slot
	if m.transcribeJobs["b"].phase != jobQueued {
		t.Fatalf("setup: b.phase = %v, want jobQueued", m.transcribeJobs["b"].phase)
	}

	m.transcribeTarget = rec("b")
	m.mode = modeTranscribing
	m, cmd := m.cancelTranscribeJob("b")
	if _, ok := m.transcribeJobs["b"]; ok {
		t.Errorf("b still in registry after canceling a queued job")
	}
	if len(m.transcribeQueue) != 0 {
		t.Errorf("transcribeQueue = %+v, want empty", m.transcribeQueue)
	}
	if m.mode != modeList {
		t.Errorf("mode = %v, want modeList (focused job's screen navigates back)", m.mode)
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want rescanCmd")
	}

	if cancel := m.transcribeJobs["a"].cancel; cancel != nil {
		cancel()
	}
}

// TestFinishTranscribeJobIgnoresUnknownID covers "messages with the wrong
// id are ignored": a stale/duplicate message for a job no longer in the
// registry must not panic or mutate unrelated state.
func TestFinishTranscribeJobIgnoresUnknownID(t *testing.T) {
	m := New(config.Config{OutputDir: t.TempDir()}, nil)
	m.statusMsg = "untouched"

	nm, cmd := m.finishTranscribeJob("does-not-exist", context.Canceled)
	if nm.statusMsg != "untouched" {
		t.Errorf("statusMsg = %q, want unchanged", nm.statusMsg)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil for an unknown job id", cmd)
	}
}

func TestHandleTranscribeLineIgnoresUnknownID(t *testing.T) {
	m := New(config.Config{}, nil)

	nm, cmd := m.handleTranscribeLine(transcribeLineMsg{id: "does-not-exist", line: "hello"})
	if len(nm.transcribeJobs) != 0 {
		t.Errorf("transcribeJobs = %+v, want untouched", nm.transcribeJobs)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil for an unknown job id", cmd)
	}
}

func TestHandleTranscribeStartedIgnoresUnknownID(t *testing.T) {
	m := New(config.Config{}, nil)

	nm, cmd := m.handleTranscribeStarted(transcribeStartedMsg{id: "does-not-exist"})
	if len(nm.transcribeJobs) != 0 {
		t.Errorf("transcribeJobs = %+v, want untouched", nm.transcribeJobs)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil for an unknown job id", cmd)
	}
}

// TestFocusedJobFinishReturnsToOwnScreenOnly: a job finishing while the
// user is watching a *different* screen (e.g. the list, or another job's
// screen) must not yank navigation -- only a status line + silent rescan.
func TestUnfocusedJobFinishLeavesModeAlone(t *testing.T) {
	m := New(config.Config{OutputDir: t.TempDir(), Transcriber: "whisperx", MaxParallelTranscriptions: 2}, nil)
	m, _ = m.admitTranscribeJob(rec("a"))
	m, _ = m.admitTranscribeJob(rec("b"))
	m.transcribeJobs["a"].cancel = func() {}
	m.transcribeJobs["b"].cancel = func() {}

	// User is watching "a"'s screen; "b" finishes in the background.
	m.mode = modeTranscribing
	m.transcribeTarget = rec("a")

	nm, _ := m.finishTranscribeJob("b", nil)
	if nm.mode != modeTranscribing || nm.transcribeTarget.ID != "a" {
		t.Errorf("mode/target = %v/%s, want unchanged (still watching a)", nm.mode, nm.transcribeTarget.ID)
	}
	if nm.statusMsg != "transcribed b" {
		t.Errorf("statusMsg = %q, want %q", nm.statusMsg, "transcribed b")
	}
	if _, ok := nm.transcribeJobs["a"]; !ok {
		t.Errorf("a removed from registry by b's completion")
	}

	if cancel := nm.transcribeJobs["a"].cancel; cancel != nil {
		cancel()
	}
}

func TestJobStatusSummary(t *testing.T) {
	tests := []struct {
		name string
		jobs map[string]*transcribeJobState
		want string
	}{
		{"none", nil, ""},
		{"only running", map[string]*transcribeJobState{"a": {phase: jobRunning}, "b": {phase: jobConverting}}, "transcribing 2"},
		{"only queued", map[string]*transcribeJobState{"a": {phase: jobQueued}}, "queued 1"},
		{"mixed", map[string]*transcribeJobState{"a": {phase: jobRunning}, "b": {phase: jobQueued}}, "transcribing 1 · queued 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{transcribeJobs: tt.jobs}
			if got := m.jobStatusSummary(); got != tt.want {
				t.Errorf("jobStatusSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTranscribedStatus(t *testing.T) {
	tests := []struct {
		name          string
		transcriber   string
		tipShown      bool
		wantStatus    string
		wantNextShown bool
	}{
		{"whisper-cli first completion adds the tip", "whisper-cli", false, "transcribed a · " + transcribe.TranscribeTip, true},
		{"whisper-cli second completion stays plain", "whisper-cli", true, "transcribed a", true},
		{"whisperx never shows the tip", "whisperx", false, "transcribed a", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, nextShown := transcribedStatus("a", tt.transcriber, tt.tipShown)
			if status != tt.wantStatus {
				t.Errorf("transcribedStatus() status = %q, want %q", status, tt.wantStatus)
			}
			if nextShown != tt.wantNextShown {
				t.Errorf("transcribedStatus() nextTipShown = %v, want %v", nextShown, tt.wantNextShown)
			}
		})
	}
}

// TestFinishTranscribeJobTipOncePerSession: the whisperx upsell tip appears
// on the first whisper-cli job's completion and not on the second, within
// the same Model (session).
func TestFinishTranscribeJobTipOncePerSession(t *testing.T) {
	m := New(config.Config{OutputDir: t.TempDir(), Transcriber: "whisper-cli", MaxParallelTranscriptions: 2}, nil)
	m, _ = m.admitTranscribeJob(rec("a"))
	m, _ = m.admitTranscribeJob(rec("b"))

	m, _ = m.finishTranscribeJob("a", nil)
	if want := "transcribed a · " + transcribe.TranscribeTip; m.statusMsg != want {
		t.Errorf("first completion statusMsg = %q, want %q", m.statusMsg, want)
	}
	if !m.tipShown {
		t.Errorf("tipShown = false after first whisper-cli completion, want true")
	}

	m, _ = m.finishTranscribeJob("b", nil)
	if want := "transcribed b"; m.statusMsg != want {
		t.Errorf("second completion statusMsg = %q, want %q (no repeated tip)", m.statusMsg, want)
	}
}

// TestFinishTranscribeJobNoTipForWhisperX: whisperx already diarizes, so its
// completions never show the upsell tip.
func TestFinishTranscribeJobNoTipForWhisperX(t *testing.T) {
	m := New(config.Config{OutputDir: t.TempDir(), Transcriber: "whisperx", MaxParallelTranscriptions: 2}, nil)
	m, _ = m.admitTranscribeJob(rec("a"))

	m, _ = m.finishTranscribeJob("a", nil)
	if want := "transcribed a"; m.statusMsg != want {
		t.Errorf("statusMsg = %q, want %q", m.statusMsg, want)
	}
	if m.tipShown {
		t.Errorf("tipShown = true for whisperx completion, want false")
	}
}

func TestListSpinnerFrameCycles(t *testing.T) {
	seen := map[string]bool{}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range len(listSpinnerFrames) {
		seen[listSpinnerFrame(base.Add(time.Duration(i)*120*time.Millisecond))] = true
	}
	if len(seen) != len(listSpinnerFrames) {
		t.Errorf("listSpinnerFrame cycled through %d distinct frames over one period, want %d", len(seen), len(listSpinnerFrames))
	}
}
