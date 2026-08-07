package transcribe

import (
	"testing"
	"time"

	"github.com/scaccogatto/nastro/internal/records"
)

func TestResolveRecord(t *testing.T) {
	older := records.Record{ID: "2026-08-05-1000-standup", Date: time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)}
	newer := records.Record{ID: "2026-08-06-1430-cliente-eppi", Date: time.Date(2026, 8, 6, 14, 30, 0, 0, time.UTC)}
	all := []records.Record{older, newer}

	tests := []struct {
		name     string
		records  []records.Record
		idOrLast string
		want     records.Record
		wantErr  bool
	}{
		{
			name:     "last picks most recent regardless of slice order",
			records:  all,
			idOrLast: "last",
			want:     newer,
		},
		{
			name:     "exact id match",
			records:  all,
			idOrLast: "2026-08-05-1000-standup",
			want:     older,
		},
		{
			name:     "unknown id errors",
			records:  all,
			idOrLast: "does-not-exist",
			wantErr:  true,
		},
		{
			name:     "last on empty slice errors",
			records:  nil,
			idOrLast: "last",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveRecord(tt.records, tt.idOrLast)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveRecord() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveRecord() unexpected error: %v", err)
			}
			if got.ID != tt.want.ID {
				t.Errorf("ResolveRecord() = %q, want %q", got.ID, tt.want.ID)
			}
		})
	}
}

func TestModelPath(t *testing.T) {
	got := ModelPath("/home/user", "large-v3-turbo")
	want := "/home/user/.cache/whisper/ggml-large-v3-turbo.bin"
	if got != want {
		t.Errorf("ModelPath() = %q, want %q", got, want)
	}
}
