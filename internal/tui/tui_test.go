package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/record"
	"github.com/scaccogatto/nastro/internal/records"
)

func TestUpdateQuitsOnQ(t *testing.T) {
	m := New(config.Config{}, nil)

	_, cmd := m.Update(tea.KeyPressMsg{Text: "q", Code: 'q'})
	if cmd == nil {
		t.Fatalf("Update(q) returned nil cmd, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("Update(q) cmd() = %T, want tea.QuitMsg", cmd())
	}
}

func TestUpdateEnterOnEmptyListIsNoop(t *testing.T) {
	m := New(config.Config{}, nil)

	newModel, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("Update(enter) on empty list cmd = %v, want nil", cmd)
	}
	if newModel.(Model).mode != modeList {
		t.Errorf("Update(enter) on empty list mode = %v, want modeList", newModel.(Model).mode)
	}
}

func TestUpdateEnterOpensDetail(t *testing.T) {
	m := New(config.Config{}, []records.Record{{ID: "2026-08-06-1430-standup"}})

	newModel, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	nm := newModel.(Model)
	if nm.mode != modeDetail {
		t.Errorf("Update(enter) mode = %v, want modeDetail", nm.mode)
	}
	if nm.detailRec.ID != "2026-08-06-1430-standup" {
		t.Errorf("Update(enter) detailRec.ID = %q, want %q", nm.detailRec.ID, "2026-08-06-1430-standup")
	}
	if cmd == nil {
		t.Fatalf("Update(enter) returned nil cmd, want loadDetailCmd")
	}
}

func TestUpdateRKeyOpensNameForm(t *testing.T) {
	m := New(config.Config{}, nil)

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "r", Code: 'r'})
	nm := newModel.(Model)
	if nm.mode != modeNameForm {
		t.Errorf("Update(r) mode = %v, want modeNameForm", nm.mode)
	}
	if cmd == nil {
		t.Errorf("Update(r) returned nil cmd, want a focus cmd")
	}
}

func TestNameFormEscCancelsBackToList(t *testing.T) {
	m := Model{mode: modeNameForm}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "esc"})
	if cmd != nil {
		t.Errorf("Update(esc) in name form cmd = %v, want nil", cmd)
	}
	if newModel.(Model).mode != modeList {
		t.Errorf("Update(esc) in name form mode = %v, want modeList", newModel.(Model).mode)
	}
}

func TestNameFormEnterStartsRecording(t *testing.T) {
	m := Model{mode: modeNameForm, cfg: config.Config{OutputDir: t.TempDir()}}

	_, cmd := m.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("Update(enter) in name form returned nil cmd, want a start-recording cmd")
	}
}

func TestUpdateQKeyInRecordingAsksConfirmation(t *testing.T) {
	m := Model{mode: modeRecording}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "q", Code: 'q'})
	nm := newModel.(Model)
	if !nm.confirmingQuit {
		t.Errorf("Update(q) in recording mode: confirmingQuit = false, want true")
	}
	if cmd != nil {
		t.Errorf("Update(q) in recording mode cmd = %v, want nil (just asks confirmation)", cmd)
	}
}

func TestUpdateCtrlCInRecordingStopsInsteadOfQuitting(t *testing.T) {
	m := Model{mode: modeRecording}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "ctrl+c"})
	if cmd == nil {
		t.Fatalf("Update(ctrl+c) in recording mode returned nil cmd, want a stop cmd")
	}
	if msg := cmd(); func() bool { _, ok := msg.(tea.QuitMsg); return ok }() {
		t.Errorf("Update(ctrl+c) in recording mode returned tea.Quit, want it to stop-and-save instead")
	}
	if !newModel.(Model).stopRequested {
		t.Errorf("Update(ctrl+c) in recording mode: stopRequested = false, want true")
	}
}

func TestUpdateNKeyCancelsQuitConfirmation(t *testing.T) {
	m := Model{mode: modeRecording, confirmingQuit: true}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	if cmd != nil {
		t.Errorf("Update(n) cancelling confirmation cmd = %v, want nil", cmd)
	}
	if newModel.(Model).confirmingQuit {
		t.Errorf("Update(n) cancelling confirmation: confirmingQuit = true, want false")
	}
}

func TestUpdateYKeyConfirmsQuit(t *testing.T) {
	m := Model{mode: modeRecording, confirmingQuit: true}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "y", Code: 'y'})
	if cmd == nil {
		t.Fatalf("Update(y) confirming quit returned nil cmd, want a kill cmd")
	}
	if !newModel.(Model).killRequested {
		t.Errorf("Update(y) confirming quit: killRequested = false, want true")
	}
}

func TestRecStatusLine(t *testing.T) {
	tests := []struct {
		elapsed time.Duration
		want    string
	}{
		{0, "REC 00:00"},
		{14*time.Second + 32*time.Minute, "REC 32:14"},
		{90 * time.Minute, "REC 1:30:00"},
	}

	for _, tt := range tests {
		got := recStatusLine(tt.elapsed)
		if got != tt.want {
			t.Errorf("recStatusLine(%v) = %q, want %q", tt.elapsed, got, tt.want)
		}
	}
}

func TestRenderLevelBar(t *testing.T) {
	tests := []struct {
		level float64
		want  string
	}{
		{0, "▯▯▯▯▯▯▯▯"},
		{1, "▮▮▮▮▮▮▮▮"},
		{0.5, "▮▮▮▮▯▯▯▯"},
		{-1, "▯▯▯▯▯▯▯▯"},
		{2, "▮▮▮▮▮▮▮▮"},
	}

	for _, tt := range tests {
		got := renderLevelBar(tt.level)
		if got != tt.want {
			t.Errorf("renderLevelBar(%v) = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestAppendCapped(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		line  string
		max   int
		want  []string
	}{
		{"under cap", []string{"a"}, "b", 3, []string{"a", "b"}},
		{"at cap", []string{"a", "b"}, "c", 3, []string{"a", "b", "c"}},
		{"over cap drops oldest", []string{"a", "b", "c"}, "d", 3, []string{"b", "c", "d"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appendCapped(tt.lines, tt.line, tt.max)
			if len(got) != len(tt.want) {
				t.Fatalf("appendCapped() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("appendCapped()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFooterForKnownModesNonEmpty(t *testing.T) {
	modes := []screen{
		modeList, modeDetail, modeRecording, modeNameForm, modeDownloading,
		modeTranscribing, modeRecError, modeTranscribeMissingWhisper,
		modeTranscribeDownloadConfirm, modeTranscribeError,
	}
	for _, mode := range modes {
		if footerFor(mode) == "" {
			t.Errorf("footerFor(%v) = \"\", want non-empty", mode)
		}
	}
}

func TestDetailEscReturnsToList(t *testing.T) {
	m := Model{mode: modeDetail}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "esc"})
	if cmd != nil {
		t.Errorf("Update(esc) in detail cmd = %v, want nil", cmd)
	}
	if newModel.(Model).mode != modeList {
		t.Errorf("Update(esc) in detail mode = %v, want modeList", newModel.(Model).mode)
	}
}

func TestListDKeyAsksDeleteConfirmation(t *testing.T) {
	m := New(config.Config{}, []records.Record{{ID: "2026-08-06-1430-standup"}})

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "d", Code: 'd'})
	if cmd != nil {
		t.Errorf("Update(d) cmd = %v, want nil (just asks confirmation)", cmd)
	}
	if !newModel.(Model).confirmDelete {
		t.Errorf("Update(d): confirmDelete = false, want true")
	}
}

func TestListDeleteConfirmYDeletesAndReturnsToList(t *testing.T) {
	dir := t.TempDir()
	recDir := filepath.Join(dir, "2026-08-06-1430-standup")
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	m := New(config.Config{OutputDir: dir}, []records.Record{{ID: "2026-08-06-1430-standup"}})
	m.confirmDelete = true

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "y", Code: 'y'})
	nm := newModel.(Model)
	if nm.confirmDelete {
		t.Errorf("Update(y) confirming delete: confirmDelete = true, want false")
	}
	if cmd == nil {
		t.Fatalf("Update(y) confirming delete returned nil cmd, want deleteCmd")
	}

	msg, ok := cmd().(deletedMsg)
	if !ok {
		t.Fatalf("delete cmd() = %T, want deletedMsg", cmd())
	}
	if msg.err != nil {
		t.Errorf("deletedMsg.err = %v, want nil", msg.err)
	}
	if _, err := os.Stat(recDir); !os.IsNotExist(err) {
		t.Errorf("record dir still exists after delete")
	}
}

func TestDeleteConfirmNCancels(t *testing.T) {
	m := Model{mode: modeList, confirmDelete: true}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	if cmd != nil {
		t.Errorf("Update(n) cancelling delete cmd = %v, want nil", cmd)
	}
	if newModel.(Model).confirmDelete {
		t.Errorf("Update(n) cancelling delete: confirmDelete = true, want false")
	}
}

func TestListTKeyOnFreshRecordChecksPrereqsDirectly(t *testing.T) {
	m := New(config.Config{}, []records.Record{{ID: "2026-08-06-1430-standup"}})

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "t", Code: 't'})
	nm := newModel.(Model)
	if nm.confirmOverwrite {
		t.Errorf("Update(t) on fresh record: confirmOverwrite = true, want false")
	}
	if cmd == nil {
		t.Fatalf("Update(t) on fresh record returned nil cmd, want checkTranscribePrereqsCmd")
	}
}

func TestListTKeyOnTranscribedRecordAsksOverwrite(t *testing.T) {
	m := New(config.Config{}, []records.Record{{ID: "2026-08-06-1430-standup", HasTranscript: true}})

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "t", Code: 't'})
	nm := newModel.(Model)
	if !nm.confirmOverwrite {
		t.Errorf("Update(t) on transcribed record: confirmOverwrite = false, want true")
	}
	if cmd != nil {
		t.Errorf("Update(t) on transcribed record cmd = %v, want nil (just asks confirmation)", cmd)
	}
}

func TestOverwriteConfirmNCancels(t *testing.T) {
	m := Model{mode: modeList, confirmOverwrite: true}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	if cmd != nil {
		t.Errorf("Update(n) cancelling overwrite cmd = %v, want nil", cmd)
	}
	if newModel.(Model).confirmOverwrite {
		t.Errorf("Update(n) cancelling overwrite: confirmOverwrite = true, want false")
	}
}

func TestOverwriteConfirmYProceedsToPrereqCheck(t *testing.T) {
	m := Model{mode: modeList, confirmOverwrite: true}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "y", Code: 'y'})
	if newModel.(Model).confirmOverwrite {
		t.Errorf("Update(y) confirming overwrite: confirmOverwrite = true, want false")
	}
	if cmd == nil {
		t.Fatalf("Update(y) confirming overwrite returned nil cmd, want checkTranscribePrereqsCmd")
	}
}

func TestHandleTranscribePrereqWhisperMissing(t *testing.T) {
	m := Model{mode: modeList, transcribeReturn: modeList}

	newModel, cmd := m.Update(transcribePrereqMsg{whisperMissing: true})
	if newModel.(Model).mode != modeTranscribeMissingWhisper {
		t.Errorf("mode = %v, want modeTranscribeMissingWhisper", newModel.(Model).mode)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

func TestHandleTranscribePrereqModelMissing(t *testing.T) {
	m := Model{mode: modeList, transcribeReturn: modeList}

	newModel, cmd := m.Update(transcribePrereqMsg{modelMissing: true, modelPath: "/some/model.bin"})
	nm := newModel.(Model)
	if nm.mode != modeTranscribeDownloadConfirm {
		t.Errorf("mode = %v, want modeTranscribeDownloadConfirm", nm.mode)
	}
	if nm.pendingModelPath != "/some/model.bin" {
		t.Errorf("pendingModelPath = %q, want %q", nm.pendingModelPath, "/some/model.bin")
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

func TestHandleTranscribePrereqAllPresentStartsRun(t *testing.T) {
	m := Model{mode: modeList, transcribeReturn: modeList, cfg: config.Config{OutputDir: t.TempDir()}}

	_, cmd := m.Update(transcribePrereqMsg{})
	if cmd == nil {
		t.Fatalf("cmd = nil, want startTranscribeRunCmd")
	}
}

func TestDownloadConfirmNCancelsBackToReturnScreen(t *testing.T) {
	m := Model{mode: modeTranscribeDownloadConfirm, transcribeReturn: modeDetail}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	if cmd != nil {
		t.Errorf("Update(n) cmd = %v, want nil", cmd)
	}
	if newModel.(Model).mode != modeDetail {
		t.Errorf("Update(n) mode = %v, want modeDetail (transcribeReturn)", newModel.(Model).mode)
	}
}

func TestDownloadConfirmYStartsDownload(t *testing.T) {
	m := Model{
		mode:             modeTranscribeDownloadConfirm,
		cfg:              config.Config{WhisperModel: "tiny"},
		pendingModelPath: filepath.Join(t.TempDir(), "model.bin"),
	}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "y", Code: 'y'})
	nm := newModel.(Model)
	if nm.mode != modeDownloading {
		t.Errorf("Update(y) mode = %v, want modeDownloading", nm.mode)
	}
	if nm.downloadJob == nil {
		t.Errorf("Update(y): downloadJob = nil, want a started download job")
	}
	if cmd == nil {
		t.Errorf("Update(y) cmd = nil, want awaitDownloadCmd")
	}
	nm.downloadJob.Cancel() // avoid leaking the goroutine past the test
}

func TestHandleDownloadDoneSuccessStartsTranscribeRun(t *testing.T) {
	m := Model{mode: modeDownloading, cfg: config.Config{OutputDir: t.TempDir()}}

	newModel, cmd := m.Update(downloadDoneMsg{err: nil})
	if newModel.(Model).downloadJob != nil {
		t.Errorf("downloadJob = non-nil after done, want cleared")
	}
	if cmd == nil {
		t.Fatalf("cmd = nil, want startTranscribeRunCmd")
	}
}

func TestHandleDownloadDoneCanceled(t *testing.T) {
	m := Model{mode: modeDownloading, transcribeReturn: modeList}

	newModel, cmd := m.Update(downloadDoneMsg{err: context.Canceled})
	nm := newModel.(Model)
	if nm.mode != modeList {
		t.Errorf("mode = %v, want modeList (transcribeReturn)", nm.mode)
	}
	if nm.statusIsErr {
		t.Errorf("statusIsErr = true, want false for a user cancel")
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

func TestHandleDownloadDoneError(t *testing.T) {
	m := Model{mode: modeDownloading}

	newModel, _ := m.Update(downloadDoneMsg{err: errors.New("boom")})
	nm := newModel.(Model)
	if nm.mode != modeTranscribeError {
		t.Errorf("mode = %v, want modeTranscribeError", nm.mode)
	}
	if nm.transcribeErr == nil {
		t.Errorf("transcribeErr = nil, want the download error")
	}
}

func TestHandleTranscribeStartedError(t *testing.T) {
	m := Model{}

	newModel, cmd := m.Update(transcribeStartedMsg{err: errors.New("afconvert failed")})
	if newModel.(Model).mode != modeTranscribeError {
		t.Errorf("mode = %v, want modeTranscribeError", newModel.(Model).mode)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

func TestHandleTranscribeStartedOKEntersTranscribing(t *testing.T) {
	m := Model{}

	newModel, cmd := m.Update(transcribeStartedMsg{tmpWavPath: "/tmp/x.wav"})
	nm := newModel.(Model)
	if nm.mode != modeTranscribing {
		t.Errorf("mode = %v, want modeTranscribing", nm.mode)
	}
	if nm.transcribeTmpWav != "/tmp/x.wav" {
		t.Errorf("transcribeTmpWav = %q, want %q", nm.transcribeTmpWav, "/tmp/x.wav")
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want a batch of spinner tick + await")
	}
}

func TestHandleTranscribeDoneErrorShowsErrorScreen(t *testing.T) {
	m := Model{mode: modeTranscribing}

	newModel, cmd := m.Update(transcribeDoneMsg{err: errors.New("whisper-cli exploded")})
	nm := newModel.(Model)
	if nm.mode != modeTranscribeError {
		t.Errorf("mode = %v, want modeTranscribeError", nm.mode)
	}
	if nm.transcribeErr == nil {
		t.Errorf("transcribeErr = nil, want the whisper-cli error")
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

func TestHandleTranscribeDoneSuccessReturnsToList(t *testing.T) {
	m := Model{
		mode:             modeTranscribing,
		cfg:              config.Config{OutputDir: t.TempDir()},
		transcribeReturn: modeList,
		transcribeTarget: records.Record{ID: "2026-08-06-1430-standup"},
	}

	newModel, cmd := m.Update(transcribeDoneMsg{})
	nm := newModel.(Model)
	if nm.mode != modeList {
		t.Errorf("mode = %v, want modeList", nm.mode)
	}
	if nm.statusMsg != "transcribed 2026-08-06-1430-standup" {
		t.Errorf("statusMsg = %q, want %q", nm.statusMsg, "transcribed 2026-08-06-1430-standup")
	}
	if nm.statusIsErr {
		t.Errorf("statusIsErr = true, want false")
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want rescanCmd")
	}
}

func TestHandleTranscribeDoneSuccessReturnsToDetailRefreshesFlag(t *testing.T) {
	m := Model{
		mode:             modeTranscribing,
		cfg:              config.Config{OutputDir: t.TempDir()},
		transcribeReturn: modeDetail,
		transcribeTarget: records.Record{ID: "2026-08-06-1430-standup"},
		detailRec:        records.Record{ID: "2026-08-06-1430-standup", HasTranscript: false},
	}

	newModel, cmd := m.Update(transcribeDoneMsg{})
	nm := newModel.(Model)
	if nm.mode != modeDetail {
		t.Errorf("mode = %v, want modeDetail", nm.mode)
	}
	if !nm.detailRec.HasTranscript {
		t.Errorf("detailRec.HasTranscript = false, want true")
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want loadDetailCmd")
	}
}

func TestTranscribeLineMsgCapsLines(t *testing.T) {
	m := Model{mode: modeTranscribing, transcribeLines: []string{"a", "b", "c"}}

	newModel, cmd := m.Update(transcribeLineMsg{line: "d"})
	nm := newModel.(Model)
	want := []string{"b", "c", "d"}
	if len(nm.transcribeLines) != len(want) {
		t.Fatalf("transcribeLines = %+v, want %+v", nm.transcribeLines, want)
	}
	for i := range want {
		if nm.transcribeLines[i] != want[i] {
			t.Errorf("transcribeLines[%d] = %q, want %q", i, nm.transcribeLines[i], want[i])
		}
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want awaitTranscribeCmd re-issued")
	}
}

func TestLevelMsgUpdatesLevelAndReissuesAwait(t *testing.T) {
	m := Model{mode: modeRecording}
	lvl := record.Level{System: 0.5, HasSystem: true}

	newModel, cmd := m.Update(levelMsg{lvl: lvl})
	nm := newModel.(Model)
	if nm.level != lvl {
		t.Errorf("level = %+v, want %+v", nm.level, lvl)
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want awaitOrTickCmd re-issued")
	}
}

func TestDeletedMsgReloadsListWithStatus(t *testing.T) {
	m := Model{cfg: config.Config{OutputDir: t.TempDir()}}

	_, cmd := m.Update(deletedMsg{id: "2026-08-06-1430-standup"})
	if cmd == nil {
		t.Fatalf("cmd = nil, want rescanCmd")
	}
	msg, ok := cmd().(recordsReloadedMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want recordsReloadedMsg", cmd())
	}
	if msg.status != "deleted 2026-08-06-1430-standup" {
		t.Errorf("status = %q, want %q", msg.status, "deleted 2026-08-06-1430-standup")
	}
	if msg.isErr {
		t.Errorf("isErr = true, want false")
	}
}

func TestHyperlink(t *testing.T) {
	tests := []struct {
		text, path, want string
	}{
		{"x", "/tmp/plain", "\x1b]8;;file:///tmp/plain\x1b\\x\x1b]8;;\x1b\\"},
		{"y", "/tmp/con spazio", "\x1b]8;;file:///tmp/con%20spazio\x1b\\y\x1b]8;;\x1b\\"},
	}
	for _, tt := range tests {
		if got := hyperlink(tt.text, tt.path); got != tt.want {
			t.Errorf("hyperlink(%q, %q) = %q, want %q", tt.text, tt.path, got, tt.want)
		}
	}
}

func TestWindowSizeReservesFooterAndStatus(t *testing.T) {
	m := New(config.Config{}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := updated.(Model).list.Height()
	h, v := appStyle.GetFrameSize()
	_ = h
	want := 24 - v - chromeLines
	if got != want {
		t.Errorf("list.Height() after WindowSizeMsg = %d, want %d (chrome reserved)", got, want)
	}
}
