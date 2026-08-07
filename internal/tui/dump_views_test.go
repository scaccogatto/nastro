package tui

// TestDumpViews renders every screen of the TUI to stdout so reviewers can
// inspect real layout output without a pty. Run with:
//
//	go test ./internal/tui/ -run TestDumpViews -v
//
// It is a review harness, not an assertion; it never fails.

import (
	"fmt"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/record"
	"github.com/scaccogatto/nastro/internal/records"
)

func dumpRecs() []records.Record {
	return []records.Record{
		{ID: "2026-08-07-1430-cliente-eppi", Date: time.Date(2026, 8, 7, 14, 30, 0, 0, time.UTC), Slug: "cliente-eppi", DurationSeconds: 2520, HasDuration: true, SizeBytes: 128 << 20, HasTranscript: true},
		{ID: "2026-08-06-1000", Date: time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC), DurationSeconds: 720, HasDuration: true, SizeBytes: 38 << 20},
		{ID: "2026-08-05-1500-presales-baxi-con-nome-molto-lungo-che-non-finisce-mai", Date: time.Date(2026, 8, 5, 15, 0, 0, 0, time.UTC), Slug: "presales-baxi-con-nome-molto-lungo-che-non-finisce-mai", SizeBytes: 170 << 20},
	}
}

// dumpRecordingModel builds a recording-screen Model with a fake in-progress
// session: 42m7s in, both level bars active, a plausible on-disk size.
// record.Session's exported fields are enough for the recording view, which
// never touches its process-management internals.
func dumpRecordingModel(cfg config.Config) Model {
	m := New(cfg, dumpRecs())
	m.mode = modeRecording
	m.sess = &record.Session{
		RecordDir: "/tmp/dump/2026-08-07-1430-cliente-eppi",
		Start:     time.Now().Add(-42*time.Minute - 7*time.Second),
	}
	m.level = record.Level{System: 0.6, HasSystem: true, Mic: 0.3, HasMic: true}
	m.recSize = 128 << 20
	return m
}

func render(t *testing.T, label string, m Model, w, h int) {
	t.Helper()
	um, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	fmt.Printf("\n========== %s (%dx%d) ==========\n%s\n", label, w, h, um.(Model).View().Content)
}

func TestDumpViews(t *testing.T) {
	cfg := config.Config{OutputDir: "/tmp/dump", WhisperModel: "large-v3-turbo", Lang: "it"}

	render(t, "list populated", New(cfg, dumpRecs()), 100, 30)
	render(t, "list populated", New(cfg, dumpRecs()), 80, 24)
	render(t, "list EMPTY (first run)", New(cfg, nil), 100, 30)
	render(t, "list narrow terminal", New(cfg, dumpRecs()), 60, 15)

	render(t, "recording", dumpRecordingModel(cfg), 100, 30)
	render(t, "recording narrow", dumpRecordingModel(cfg), 60, 15)

	m := New(cfg, dumpRecs())
	m.statusMsg = "saved /tmp/dump/2026-08-07-1430-cliente-eppi"
	render(t, "list with ok status", m, 100, 30)

	ms := New(cfg, dumpRecs())
	ms.homeDir = "/Users/alex"
	ms.savedStatusDir = "/Users/alex/Recordings/nastro/2026-08-07-1430-cliente-eppi"
	ms.savedStatusDuration = 2527
	render(t, "list with saved status (peak-end)", ms, 100, 30)

	me := New(cfg, dumpRecs())
	me.statusMsg = "error deleting x: permission denied"
	me.statusIsErr = true
	render(t, "list with error status", me, 100, 30)

	md := New(cfg, dumpRecs())
	md.confirmDelete = true
	render(t, "list confirm delete", md, 100, 30)

	lf := New(cfg, dumpRecs())
	lf.list.SetFilterState(list.Filtering)
	render(t, "list filtering", lf, 100, 30)

	rd := dumpRecordingModel(cfg)
	rd.confirmingQuit = true
	render(t, "recording (confirm discard)", rd, 100, 30)

	nf := New(cfg, dumpRecs())
	nf.mode = modeNameForm
	render(t, "name form (mixed, default)", nf, 100, 30)

	nfm := New(cfg, dumpRecs())
	nfm.mode = modeNameForm
	nfm.nameFormMode = captureMicOnly
	render(t, "name form (mode cycled via tab: mic-only)", nfm, 100, 30)

	tr := New(cfg, dumpRecs())
	tr.mode = modeTranscribing
	tr.transcribeTarget = dumpRecs()[0]
	tr.transcribePhase = transcribeRunning
	tr.transcribeStart = time.Now().Add(-37 * time.Second)
	tr.transcribeHasPct = true
	tr.transcribePct = 0.45
	render(t, "transcribing (progress bar)", tr, 100, 30)

	trs := New(cfg, dumpRecs())
	trs.mode = modeTranscribing
	trs.transcribeTarget = dumpRecs()[1]
	trs.transcribePhase = transcribePreparing
	trs.transcribeStart = time.Now().Add(-3 * time.Second)
	render(t, "transcribing (preparing audio, spinner fallback)", trs, 100, 30)

	dw := New(cfg, dumpRecs())
	dw.mode = modeDownloading
	dw.transcribeTarget = dumpRecs()[2]
	dw.downloadPct = 0.62
	render(t, "downloading (progress)", dw, 100, 30)

	hp := New(cfg, dumpRecs())
	hp.mode = modeHelp
	hp.homeDir = "/Users/alex"
	render(t, "help overlay", hp, 100, 30)
	render(t, "help overlay (must scroll, standard height)", hp, 80, 24)

	dt := New(cfg, dumpRecs())
	dt.mode = modeDetail
	dt.detailRec = dumpRecs()[0]
	dt.detailPath = "/tmp/dump/2026-08-07-1430-cliente-eppi"
	dt.detailPreview = []string{"Buongiorno a tutti, iniziamo la call.", "Il punto principale oggi è la migrazione.", "Perfetto, procediamo così."}
	render(t, "detail with preview", dt, 100, 30)

	dl := New(cfg, dumpRecs())
	dl.mode = modeTranscribeDownloadConfirm
	dl.transcribeTarget = dumpRecs()[2]
	render(t, "download confirm", dl, 100, 30)

	mw := New(cfg, dumpRecs())
	mw.mode = modeTranscribeMissingWhisper
	render(t, "missing whisper-cli", mw, 100, 30)

	te := New(cfg, dumpRecs())
	te.mode = modeTranscribeError
	te.transcribeErr = fmt.Errorf("afconvert: exit status 1")
	render(t, "transcribe error", te, 100, 30)

	re := New(cfg, dumpRecs())
	re.mode = modeRecError
	re.recErr = fmt.Errorf("macOS needs to grant audio-capture permission to your terminal app.\nOpen System Settings > Privacy & Security > Screen & System Audio Recording,\nenable it for your terminal, then re-run this command.")
	render(t, "record error (TCC)", re, 100, 30)
}
