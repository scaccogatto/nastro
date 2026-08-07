package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/scaccogatto/nastro/internal/records"
)

func TestUpdateQuitsOnQ(t *testing.T) {
	m := New(nil)

	_, cmd := m.Update(tea.KeyPressMsg{Text: "q", Code: 'q'})
	if cmd == nil {
		t.Fatalf("Update(q) returned nil cmd, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("Update(q) cmd() = %T, want tea.QuitMsg", cmd())
	}
}

func TestUpdateEnterIsNoop(t *testing.T) {
	m := New([]records.Record{{ID: "2026-08-06-1430-standup"}})

	newModel, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("Update(enter) cmd = %v, want nil", cmd)
	}
	if newModel.(Model).list.Index() != m.list.Index() {
		t.Errorf("Update(enter) changed list selection, want no-op")
	}
}
