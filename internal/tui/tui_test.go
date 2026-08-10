package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/record"
	"github.com/scaccogatto/nastro/internal/records"
	"github.com/scaccogatto/nastro/internal/transcribe"
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

func TestUpdateXKeyInRecordingAsksConfirmation(t *testing.T) {
	m := Model{mode: modeRecording}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	nm := newModel.(Model)
	if !nm.confirmingQuit {
		t.Errorf("Update(x) in recording mode: confirmingQuit = false, want true")
	}
	if cmd != nil {
		t.Errorf("Update(x) in recording mode cmd = %v, want nil (just asks confirmation)", cmd)
	}
}

func TestUpdateQKeyInRecordingStopsAndSaves(t *testing.T) {
	m := Model{mode: modeRecording}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "q", Code: 'q'})
	if cmd == nil {
		t.Fatalf("Update(q) in recording mode returned nil cmd, want a stop cmd")
	}
	if msg := cmd(); func() bool { _, ok := msg.(tea.QuitMsg); return ok }() {
		t.Errorf("Update(q) in recording mode returned tea.Quit, want it to stop-and-save instead")
	}
	nm := newModel.(Model)
	if !nm.stopRequested {
		t.Errorf("Update(q) in recording mode: stopRequested = false, want true")
	}
	if nm.confirmingQuit {
		t.Errorf("Update(q) in recording mode: confirmingQuit = true, want false (q no longer discards)")
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

func TestUpdateCapitalYKeyConfirmsQuit(t *testing.T) {
	m := Model{mode: modeRecording, confirmingQuit: true}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "Y", Code: 'Y'})
	if cmd == nil {
		t.Fatalf("Update(Y) confirming quit returned nil cmd, want a kill cmd")
	}
	if !newModel.(Model).killRequested {
		t.Errorf("Update(Y) confirming quit: killRequested = false, want true")
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
		modeTranscribing, modeRecError, modeTranscribeMissingPrereq,
		modeTranscribeDownloadConfirm, modeTranscribeError, modeHelp,
	}
	for _, mode := range modes {
		for _, compact := range []bool{false, true} {
			if footerFor(mode, compact) == "" {
				t.Errorf("footerFor(%v, %v) = \"\", want non-empty", mode, compact)
			}
		}
	}
}

func TestFilteringForwardsAllKeysToList(t *testing.T) {
	m := New(config.Config{}, []records.Record{{ID: "2026-08-06-1430-standup"}})
	m.list.SetFilterState(list.Filtering)

	for _, key := range []string{"r", "q"} {
		newModel, cmd := m.Update(tea.KeyPressMsg{Text: key, Code: rune(key[0])})
		nm := newModel.(Model)
		if nm.mode != modeList {
			t.Errorf("Update(%q) while filtering: mode = %v, want modeList", key, nm.mode)
		}
		if cmd != nil {
			if _, ok := cmd().(tea.QuitMsg); ok {
				t.Errorf("Update(%q) while filtering returned tea.Quit, want the filter to consume it", key)
			}
		}
	}
}

func TestFooterWhileFilteringShowsApplyCancel(t *testing.T) {
	m := New(config.Config{}, nil)
	m.list.SetFilterState(list.Filtering)
	if got := m.footer(); got != "enter apply · esc cancel" {
		t.Errorf("footer() while filtering = %q, want %q", got, "enter apply · esc cancel")
	}
}

func TestEmptyListShowsInviteNotNoItems(t *testing.T) {
	m := New(config.Config{}, nil)
	v := m.View().Content
	if !strings.Contains(v, "no recordings yet") {
		t.Errorf("View() on empty list missing invite, got:\n%s", v)
	}
	if strings.Contains(v, "No items") {
		t.Errorf("View() on empty list still contains bubbles' default \"No items\", got:\n%s", v)
	}
}

func TestDetailEscReturnsToList(t *testing.T) {
	m := Model{mode: modeDetail, cfg: config.Config{OutputDir: t.TempDir()}}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "esc"})
	if cmd == nil {
		t.Fatalf("Update(esc) in detail cmd = nil, want rescanCmd so the list's checkmark stays fresh (H4)")
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
	// A fake home, so trashDir() (homeDir + ".Trash") lands in a tempdir
	// instead of the real ~/.Trash.
	home := t.TempDir()
	recDir := filepath.Join(dir, "2026-08-06-1430-standup")
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	m := New(config.Config{OutputDir: dir}, []records.Record{{ID: "2026-08-06-1430-standup"}})
	m.homeDir = home
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
	if msg.fellBack {
		t.Errorf("deletedMsg.fellBack = true, want false (Trash was available)")
	}
	if _, err := os.Stat(recDir); !os.IsNotExist(err) {
		t.Errorf("record dir still exists after delete")
	}
	if _, err := os.Stat(filepath.Join(home, ".Trash", "2026-08-06-1430-standup")); err != nil {
		t.Errorf("record dir not found in Trash: %v", err)
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

	newModel, cmd := m.Update(transcribePrereqMsg{missingMsg: "whisper-cli not found"})
	nm := newModel.(Model)
	if nm.mode != modeTranscribeMissingPrereq {
		t.Errorf("mode = %v, want modeTranscribeMissingPrereq", nm.mode)
	}
	if nm.transcribeMissingMsg != "whisper-cli not found" {
		t.Errorf("transcribeMissingMsg = %q, want %q", nm.transcribeMissingMsg, "whisper-cli not found")
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

// --- P2: tool-install flow (whisperx via uv, whisper-cli via brew), sharing
// modeTranscribeDownloadConfirm/modeDownloading with the model download
// above. ---

func TestHandleTranscribePrereqInstallOffered(t *testing.T) {
	m := Model{mode: modeList, transcribeReturn: modeList}
	offer := &transcribe.InstallOffer{Tool: "whisperx", Installer: "uv", InstallerPath: "/opt/homebrew/bin/uv", Args: []string{"tool", "install", "whisperx"}}

	newModel, cmd := m.Update(transcribePrereqMsg{install: offer})
	nm := newModel.(Model)
	if nm.mode != modeTranscribeDownloadConfirm {
		t.Errorf("mode = %v, want modeTranscribeDownloadConfirm", nm.mode)
	}
	if nm.pendingInstall != offer {
		t.Errorf("pendingInstall = %+v, want %+v", nm.pendingInstall, offer)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

func TestDownloadConfirmYWithPendingInstallStartsInstall(t *testing.T) {
	offer := &transcribe.InstallOffer{Tool: "whisperx", Installer: "uv", InstallerPath: "/opt/homebrew/bin/uv", Args: []string{"tool", "install", "whisperx"}}
	m := Model{mode: modeTranscribeDownloadConfirm, pendingInstall: offer, transcribeSpinner: spinner.New()}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "y", Code: 'y'})
	nm := newModel.(Model)
	if nm.mode != modeDownloading {
		t.Errorf("Update(y) mode = %v, want modeDownloading", nm.mode)
	}
	if nm.installCancel == nil {
		t.Errorf("installCancel = nil, want a cancel func")
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want a batch of spinner tick + startToolInstallCmd")
	}
	nm.installCancel() // avoid leaking the context past the test
}

func TestDownloadConfirmNClearsPendingInstall(t *testing.T) {
	offer := &transcribe.InstallOffer{Tool: "whisperx"}
	m := Model{mode: modeTranscribeDownloadConfirm, pendingInstall: offer, transcribeReturn: modeDetail}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	nm := newModel.(Model)
	if nm.mode != modeDetail {
		t.Errorf("Update(n) mode = %v, want modeDetail (transcribeReturn)", nm.mode)
	}
	if nm.pendingInstall != nil {
		t.Errorf("pendingInstall = %+v, want nil after declining", nm.pendingInstall)
	}
	if cmd != nil {
		t.Errorf("Update(n) cmd = %v, want nil", cmd)
	}
}

func TestHandleInstallStartedError(t *testing.T) {
	m := Model{mode: modeDownloading, pendingInstall: &transcribe.InstallOffer{Tool: "whisperx"}}

	newModel, cmd := m.Update(installStartedMsg{err: errors.New("exec: \"uv\": executable file not found")})
	nm := newModel.(Model)
	if nm.mode != modeTranscribeError {
		t.Errorf("mode = %v, want modeTranscribeError", nm.mode)
	}
	if nm.pendingInstall != nil {
		t.Errorf("pendingInstall = %+v, want nil after a start failure", nm.pendingInstall)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

func TestHandleInstallStartedOK(t *testing.T) {
	m := Model{mode: modeDownloading}

	newModel, cmd := m.Update(installStartedMsg{job: nil})
	if cmd == nil {
		t.Errorf("cmd = nil, want awaitInstallCmd")
	}
	_ = newModel
}

func TestHandleInstallDoneSuccessReChecksPrereqs(t *testing.T) {
	m := Model{
		mode:           modeDownloading,
		cfg:            config.Config{Transcriber: "whisperx"},
		pendingInstall: &transcribe.InstallOffer{Tool: "whisperx"},
	}

	newModel, cmd := m.Update(installDoneMsg{err: nil})
	nm := newModel.(Model)
	if nm.installJob != nil {
		t.Errorf("installJob = non-nil after done, want cleared")
	}
	if nm.pendingInstall != nil {
		t.Errorf("pendingInstall = %+v, want cleared after a successful install", nm.pendingInstall)
	}
	if cmd == nil {
		t.Fatalf("cmd = nil, want checkTranscribePrereqsCmd (re-check, e.g. HF token/model)")
	}
}

func TestHandleInstallDoneCanceled(t *testing.T) {
	m := Model{mode: modeDownloading, transcribeReturn: modeList, pendingInstall: &transcribe.InstallOffer{Tool: "whisperx"}}

	newModel, cmd := m.Update(installDoneMsg{err: context.Canceled})
	nm := newModel.(Model)
	if nm.mode != modeList {
		t.Errorf("mode = %v, want modeList (transcribeReturn)", nm.mode)
	}
	if nm.statusMsg != "install canceled" {
		t.Errorf("statusMsg = %q, want %q", nm.statusMsg, "install canceled")
	}
	if nm.statusIsErr {
		t.Errorf("statusIsErr = true, want false for a user cancel")
	}
	if nm.pendingInstall != nil {
		t.Errorf("pendingInstall = %+v, want cleared", nm.pendingInstall)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

func TestHandleInstallDoneError(t *testing.T) {
	m := Model{mode: modeDownloading, pendingInstall: &transcribe.InstallOffer{Tool: "whisperx"}}

	newModel, _ := m.Update(installDoneMsg{err: errors.New("exit status 1")})
	nm := newModel.(Model)
	if nm.mode != modeTranscribeError {
		t.Errorf("mode = %v, want modeTranscribeError", nm.mode)
	}
	if nm.transcribeErr == nil {
		t.Errorf("transcribeErr = nil, want the install error")
	}
}

func TestInstallLineMsgCapsLines(t *testing.T) {
	m := Model{mode: modeDownloading, installLines: []string{"a", "b", "c"}}

	newModel, cmd := m.Update(installLineMsg{line: "d"})
	nm := newModel.(Model)
	want := []string{"b", "c", "d"}
	if len(nm.installLines) != len(want) {
		t.Fatalf("installLines = %+v, want %+v", nm.installLines, want)
	}
	for i := range want {
		if nm.installLines[i] != want[i] {
			t.Errorf("installLines[%d] = %q, want %q", i, nm.installLines[i], want[i])
		}
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want awaitInstallCmd re-issued")
	}
}

func TestUpdateKeyDownloadingCtrlCCancelsInstall(t *testing.T) {
	canceled := false
	m := Model{mode: modeDownloading, installCancel: func() { canceled = true }}

	_, cmd := m.Update(tea.KeyPressMsg{Text: "ctrl+c"})
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Errorf("Update(ctrl+c) in modeDownloading returned tea.Quit, want it to cancel the install instead")
		}
	}
	if !canceled {
		t.Errorf("Update(ctrl+c) in modeDownloading: installCancel not called")
	}
}

// TestSpinnerTicksDuringInstall is the P2 regression for the spinner gate:
// modeTranscribing isn't the only screen that animates the spinner anymore --
// modeDownloading does too, while a tool install (not a model download) is
// running.
func TestSpinnerTicksDuringInstall(t *testing.T) {
	m := Model{mode: modeDownloading, installJob: &transcribe.Job{}, transcribeSpinner: spinner.New()}

	_, cmd := m.Update(spinner.TickMsg{})
	if cmd == nil {
		t.Errorf("spinner.TickMsg during install: cmd = nil, want the spinner's re-tick cmd")
	}
}

// TestSpinnerIgnoredDuringPlainModelDownload guards the other side: a plain
// model download (no installJob) still shows its own progress bar, not the
// spinner -- ticking it would be silently wasted work.
func TestSpinnerIgnoredDuringPlainModelDownload(t *testing.T) {
	m := Model{mode: modeDownloading, transcribeSpinner: spinner.New()}

	_, cmd := m.Update(spinner.TickMsg{})
	if cmd != nil {
		t.Errorf("spinner.TickMsg during plain model download: cmd = %v, want nil", cmd)
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
	// mode is already modeTranscribing by the time transcribeStartedMsg
	// arrives: startTranscribingScreen sets it eagerly (before ConvertToWav
	// even runs) so the spinner shows "preparing audio…" with no dead gap.
	m := Model{mode: modeTranscribing, transcribePhase: transcribePreparing}

	newModel, cmd := m.Update(transcribeStartedMsg{tmpWavPath: "/tmp/x.wav"})
	nm := newModel.(Model)
	if nm.mode != modeTranscribing {
		t.Errorf("mode = %v, want modeTranscribing", nm.mode)
	}
	if nm.transcribeTmpWav != "/tmp/x.wav" {
		t.Errorf("transcribeTmpWav = %q, want %q", nm.transcribeTmpWav, "/tmp/x.wav")
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want awaitTranscribeCmd")
	}
}

func TestStartTranscribingScreenEntersPreparingPhase(t *testing.T) {
	m := Model{cfg: config.Config{OutputDir: t.TempDir()}}

	nm, cmd := m.startTranscribingScreen(records.Record{ID: "2026-08-06-1430-standup"})
	if nm.mode != modeTranscribing {
		t.Errorf("mode = %v, want modeTranscribing", nm.mode)
	}
	if nm.transcribePhase != transcribePreparing {
		t.Errorf("transcribePhase = %v, want transcribePreparing", nm.transcribePhase)
	}
	if nm.transcribeCancel == nil {
		t.Errorf("transcribeCancel = nil, want a cancel func")
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want a batch of spinner tick + startTranscribeRunCmd")
	}
	nm.transcribeCancel() // avoid leaking the context past the test
}

func TestUpdateKeyTranscribingEscCancels(t *testing.T) {
	canceled := false
	m := Model{mode: modeTranscribing, transcribeCancel: func() { canceled = true }}

	_, cmd := m.Update(tea.KeyPressMsg{Text: "esc"})
	if cmd != nil {
		t.Errorf("Update(esc) in transcribing cmd = %v, want nil", cmd)
	}
	if !canceled {
		t.Errorf("Update(esc) in transcribing: transcribeCancel not called")
	}
}

func TestUpdateKeyTranscribingCtrlCCancelsInsteadOfQuitting(t *testing.T) {
	canceled := false
	m := Model{mode: modeTranscribing, transcribeCancel: func() { canceled = true }}

	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "ctrl+c"})
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Errorf("Update(ctrl+c) in transcribing returned tea.Quit, want it to cancel instead")
		}
	}
	if !canceled {
		t.Errorf("Update(ctrl+c) in transcribing: transcribeCancel not called")
	}
	if newModel.(Model).mode != modeTranscribing {
		t.Errorf("Update(ctrl+c) in transcribing: mode = %v, want modeTranscribing (unchanged until the job reports back)", newModel.(Model).mode)
	}
}

func TestHandleTranscribeDoneCanceledReturnsToListWithRescan(t *testing.T) {
	m := Model{
		mode:             modeTranscribing,
		cfg:              config.Config{OutputDir: t.TempDir()},
		transcribeReturn: modeList,
		transcribeCancel: func() {},
	}

	newModel, cmd := m.Update(transcribeDoneMsg{err: context.Canceled})
	nm := newModel.(Model)
	if nm.mode != modeList {
		t.Errorf("mode = %v, want modeList (transcribeReturn)", nm.mode)
	}
	if nm.statusMsg != "transcription canceled" {
		t.Errorf("statusMsg = %q, want %q", nm.statusMsg, "transcription canceled")
	}
	if nm.statusIsErr {
		t.Errorf("statusIsErr = true, want false for a user cancel")
	}
	if nm.transcribeCancel != nil {
		t.Errorf("transcribeCancel not cleared after done")
	}
	// H4: canceling back to the list must rescan, so a stale ✓ (or its
	// absence) doesn't linger.
	if cmd == nil {
		t.Fatalf("cmd = nil, want rescanCmd")
	}
	msg, ok := cmd().(recordsReloadedMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want recordsReloadedMsg", cmd())
	}
	if msg.status != "transcription canceled" {
		t.Errorf("recordsReloadedMsg.status = %q, want %q", msg.status, "transcription canceled")
	}
}

func TestHandleTranscribeStartedCanceledDuringPreparing(t *testing.T) {
	m := Model{mode: modeTranscribing, transcribeReturn: modeDetail, transcribeCancel: func() {}}

	newModel, cmd := m.Update(transcribeStartedMsg{err: context.Canceled})
	nm := newModel.(Model)
	if nm.mode != modeDetail {
		t.Errorf("mode = %v, want modeDetail (transcribeReturn)", nm.mode)
	}
	if nm.statusMsg != "transcription canceled" {
		t.Errorf("statusMsg = %q, want %q", nm.statusMsg, "transcription canceled")
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

func TestHandleTranscribeStartedCanceledReturnsToListWithRescan(t *testing.T) {
	m := Model{
		mode:             modeTranscribing,
		cfg:              config.Config{OutputDir: t.TempDir()},
		transcribeReturn: modeList,
		transcribeCancel: func() {},
	}

	newModel, cmd := m.Update(transcribeStartedMsg{err: context.Canceled})
	nm := newModel.(Model)
	if nm.mode != modeList {
		t.Errorf("mode = %v, want modeList (transcribeReturn)", nm.mode)
	}
	// H4: canceling back to the list must rescan.
	if cmd == nil {
		t.Fatalf("cmd = nil, want rescanCmd")
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
		t.Errorf("cmd = nil, want awaitCmd re-issued")
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
	if msg.status != "moved to Trash: 2026-08-06-1430-standup" {
		t.Errorf("status = %q, want %q", msg.status, "moved to Trash: 2026-08-06-1430-standup")
	}
	if msg.isErr {
		t.Errorf("isErr = true, want false")
	}
}

func TestFormatSavedStatus(t *testing.T) {
	got := formatSavedStatus(2527, "/Users/alex/Recordings/nastro/2026-08-07-1430-cliente-eppi", "/Users/alex", 200)
	wantDisplay := "~/Recordings/nastro/2026-08-07-1430-cliente-eppi"
	want := "✓ saved 42:07 · " + hyperlink(wantDisplay, "/Users/alex/Recordings/nastro/2026-08-07-1430-cliente-eppi")
	if got != want {
		t.Errorf("formatSavedStatus() = %q, want %q", got, want)
	}
}

func TestFormatSavedStatusTruncatesDisplayButKeepsRealHref(t *testing.T) {
	dir := "/Users/alex/Recordings/nastro/2026-08-07-1430-cliente-eppi-molto-lungo"
	got := formatSavedStatus(60, dir, "/Users/alex", 40)
	if !strings.Contains(got, "…") {
		t.Errorf("formatSavedStatus() = %q, want a truncated (…) display path at this width", got)
	}
	// The OSC 8 href must carry the real, untruncated path even though the
	// visible text is shortened.
	if !strings.Contains(got, dir) {
		t.Errorf("formatSavedStatus() = %q, href does not point at the real path %q", got, dir)
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

// TestFooterShowsConfirmPromptDuringConfirm is H5: while a y/n confirmation
// is active (delete, transcribe-overwrite, discard-recording), the footer
// must say so instead of showing the underlying mode's usual footer (which
// would wrongly advertise e.g. "q quit" while q actually cancels).
func TestFooterShowsConfirmPromptDuringConfirm(t *testing.T) {
	want := "y confirm · any other key cancel"
	tests := []struct {
		name string
		m    Model
	}{
		{"confirmDelete in list", Model{mode: modeList, confirmDelete: true}},
		{"confirmOverwrite in detail", Model{mode: modeDetail, confirmOverwrite: true}},
		{"confirmingQuit in recording", Model{mode: modeRecording, confirmingQuit: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.footer(); got != want {
				t.Errorf("footer() = %q, want %q", got, want)
			}
		})
	}
}

func TestComposeFooterKeepsFirstAndLastWhenNarrow(t *testing.T) {
	entries := []string{"r rec", "enter open", "t transcribe", "d delete", "? help", "q quit"}
	got := composeFooter(entries, 12)
	if !strings.HasPrefix(got, "r rec") {
		t.Errorf("composeFooter(narrow) = %q, want it to keep the first entry (r rec)", got)
	}
	if !strings.Contains(got, "q") {
		t.Errorf("composeFooter(narrow) = %q, want the quit key (q) to survive narrowing, not be the first thing clamped away", got)
	}
}

func TestListCompactFooterUsesWholeWordsNotAbbreviations(t *testing.T) {
	got := footerFor(modeList, true)
	for _, abbrev := range []string{"txs", "fndr", "flt"} {
		if strings.Contains(got, abbrev) {
			t.Errorf("footerFor(modeList, compact) = %q, still contains consonant-abbreviation %q (H6)", got, abbrev)
		}
	}
	if !strings.Contains(got, "q quit") {
		t.Errorf("footerFor(modeList, compact) = %q, want it to still show the quit key", got)
	}
}

func TestUpdateKeyDownloadingCtrlCCancels(t *testing.T) {
	m := Model{mode: modeDownloading, downloadJob: transcribe.StartModelDownload("http://127.0.0.1:1/nope", filepath.Join(t.TempDir(), "model.bin"))}
	_, cmd := m.Update(tea.KeyPressMsg{Text: "ctrl+c"})
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Errorf("Update(ctrl+c) in modeDownloading returned tea.Quit, want it to cancel the download instead")
		}
	}
	if err := <-m.downloadJob.Wait(); !errors.Is(err, context.Canceled) {
		t.Errorf("downloadJob outcome after ctrl+c = %v, want context.Canceled", err)
	}
}

func TestNameFormCtrlCCancelsBackToList(t *testing.T) {
	m := Model{mode: modeNameForm}
	newModel, cmd := m.Update(tea.KeyPressMsg{Text: "ctrl+c"})
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Errorf("Update(ctrl+c) in name form returned tea.Quit, want it to cancel instead")
		}
	}
	if newModel.(Model).mode != modeList {
		t.Errorf("Update(ctrl+c) in name form mode = %v, want modeList", newModel.(Model).mode)
	}
}

// TestNewProgressUsesANSIPaletteNoTruecolor is H2: the progress bars must
// use the shared ANSI accent/muted colors, not bubbles' default truecolor
// gradient (which shows up as ESC[38;2;... codes).
func TestNewProgressUsesANSIPaletteNoTruecolor(t *testing.T) {
	p := newProgress()
	p.SetWidth(20)
	got := p.ViewAs(0.5)
	if strings.Contains(got, "38;2") || strings.Contains(got, "48;2") {
		t.Errorf("newProgress().ViewAs() = %q, contains a truecolor escape code", got)
	}
}
