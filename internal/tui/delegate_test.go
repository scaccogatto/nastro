package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/scaccogatto/nastro/internal/records"
)

func TestRenderRecordLine(t *testing.T) {
	r := records.Record{
		ID:              "2026-08-06-1430",
		Slug:            "cliente-eppi",
		Date:            time.Date(2026, 8, 6, 14, 30, 0, 0, time.UTC),
		DurationSeconds: 2520,
		HasDuration:     true,
		SizeBytes:       128 << 20,
		HasTranscript:   true,
	}

	line := renderRecordLine(r, nil, time.Now(), 80)
	for _, want := range []string{"cliente-eppi", "2026-08-06 14:30", "42:00", "128.0 MB", "✓"} {
		if !strings.Contains(line, want) {
			t.Errorf("renderRecordLine(...) = %q, missing %q", line, want)
		}
	}
}

func TestRenderRecordLineNoTranscriptNoDuration(t *testing.T) {
	r := records.Record{ID: "2026-08-06-1000"}
	line := renderRecordLine(r, nil, time.Now(), 80)
	if !strings.Contains(line, "2026-08-06-1000") {
		t.Errorf("renderRecordLine(...) = %q, want id as name fallback", line)
	}
	if !strings.HasSuffix(strings.TrimRight(line, " "), "-") {
		t.Errorf("renderRecordLine(...) = %q, want trailing '-' for missing transcript", line)
	}
}

func TestRenderRecordLineFitsWidth(t *testing.T) {
	r := records.Record{
		ID:   "2026-08-05-1500",
		Slug: "presales-baxi-con-nome-molto-lungo-che-non-finisce-mai",
	}
	for _, width := range []int{20, 40, 56, 100} {
		line := renderRecordLine(r, nil, time.Now(), width)
		if got := len([]rune(line)); got > width {
			t.Errorf("renderRecordLine(..., %d) = %q (%d runes), overflows width", width, line, got)
		}
	}
}

// TestRenderRecordLineJobStatus covers the live status column: queued,
// running with a known percent, and running/converting with an unknown one
// (an animated spinner cell) all take precedence over the plain "-"/"✓"
// shown when no job is active.
func TestRenderRecordLineJobStatus(t *testing.T) {
	r := records.Record{ID: "2026-08-06-1000", HasTranscript: true}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		st   *transcribeJobState
		want string
	}{
		{"queued", &transcribeJobState{phase: jobQueued}, "queued"},
		{"running with percent", &transcribeJobState{phase: jobRunning, percent: 45}, "45%"},
		{"converting, no percent yet", &transcribeJobState{phase: jobConverting, percent: -1}, listSpinnerFrame(now)},
		{"running, unknown percent (whisperx)", &transcribeJobState{phase: jobRunning, percent: -1}, listSpinnerFrame(now)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// r.HasTranscript is true (a previous run) in every case: an
			// active job's status always overrides the plain "✓".
			line := renderRecordLine(r, tt.st, now, 80)
			if !strings.Contains(line, tt.want) {
				t.Errorf("renderRecordLine(...) = %q, want it to contain %q", line, tt.want)
			}
		})
	}
}

func TestTranscribeStatusTextNoJobFallsBackToTranscriptFlag(t *testing.T) {
	now := time.Now()
	if got := transcribeStatusText(records.Record{HasTranscript: true}, nil, now); got != "✓" {
		t.Errorf("transcribeStatusText(no job, transcribed) = %q, want %q", got, "✓")
	}
	if got := transcribeStatusText(records.Record{}, nil, now); got != "-" {
		t.Errorf("transcribeStatusText(no job, not transcribed) = %q, want %q", got, "-")
	}
}
