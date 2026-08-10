package tui

import (
	"testing"

	"github.com/scaccogatto/nastro/internal/records"
)

func TestRecordDisplayName(t *testing.T) {
	tests := []struct {
		name string
		r    records.Record
		want string
	}{
		{"slug wins", records.Record{ID: "2026-08-06-1430", Slug: "standup"}, "standup"},
		{"falls back to id", records.Record{ID: "2026-08-06-1430"}, "2026-08-06-1430"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recordDisplayName(tt.r); got != tt.want {
				t.Errorf("recordDisplayName(%+v) = %q, want %q", tt.r, got, tt.want)
			}
		})
	}
}

func TestConfirmDeletePrompt(t *testing.T) {
	tests := []struct {
		name string
		r    records.Record
		want string
	}{
		{
			"with duration",
			records.Record{ID: "2026-08-06-1430", Slug: "cliente-eppi", HasDuration: true, DurationSeconds: 2520},
			"delete cliente-eppi (42:00)? [y/n]",
		},
		{
			"without duration falls back to id",
			records.Record{ID: "2026-08-06-1430"},
			"delete 2026-08-06-1430? [y/n]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := confirmDeletePrompt(tt.r); got != tt.want {
				t.Errorf("confirmDeletePrompt(%+v) = %q, want %q", tt.r, got, tt.want)
			}
		})
	}
}

func TestConfirmOverwritePrompt(t *testing.T) {
	r := records.Record{ID: "2026-08-06-1430", Slug: "cliente-eppi"}
	want := "cliente-eppi is already transcribed, overwrite? [y/n]"
	if got := confirmOverwritePrompt(r); got != want {
		t.Errorf("confirmOverwritePrompt(%+v) = %q, want %q", r, got, want)
	}
}

func TestConfirmCancelTranscribePrompt(t *testing.T) {
	r := records.Record{ID: "2026-08-06-1430", Slug: "cliente-eppi"}
	want := "cancel transcription of cliente-eppi? [y/n]"
	if got := confirmCancelTranscribePrompt(r); got != want {
		t.Errorf("confirmCancelTranscribePrompt(%+v) = %q, want %q", r, got, want)
	}
}

func TestConfirmQuitPrompt(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{1, "1 transcription running, quit anyway? they will be canceled [y/n]"},
		{2, "2 transcriptions running, quit anyway? they will be canceled [y/n]"},
	}
	for _, tt := range tests {
		if got := confirmQuitPrompt(tt.n); got != tt.want {
			t.Errorf("confirmQuitPrompt(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestIsConfirmYes(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"y", true},
		{"Y", true},
		{"n", false},
		{"N", false},
		{"esc", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isConfirmYes(tt.key); got != tt.want {
			t.Errorf("isConfirmYes(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestFormatDownloadPrompt(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"large-v3-turbo", "model large-v3-turbo missing (~1.6 GB). Download now? [y/n]"},
		{"tiny", "model tiny missing (~78 MB). Download now? [y/n]"},
		{"some-unknown-model", "model some-unknown-model missing. Download now? [y/n]"},
	}
	for _, tt := range tests {
		if got := formatDownloadPrompt(tt.model); got != tt.want {
			t.Errorf("formatDownloadPrompt(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}
