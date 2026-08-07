package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/scaccogatto/nastro/internal/config"
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

func TestUpdateEnterIsNoop(t *testing.T) {
	m := New(config.Config{}, []records.Record{{ID: "2026-08-06-1430-standup"}})

	newModel, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("Update(enter) cmd = %v, want nil", cmd)
	}
	if newModel.(Model).list.Index() != m.list.Index() {
		t.Errorf("Update(enter) changed list selection, want no-op")
	}
}

func TestUpdateRKeyStartsRecordingCmd(t *testing.T) {
	m := New(config.Config{}, nil)

	_, cmd := m.Update(tea.KeyPressMsg{Text: "r", Code: 'r'})
	if cmd == nil {
		t.Fatalf("Update(r) returned nil cmd, want a start-recording cmd")
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
