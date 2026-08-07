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

	line := renderRecordLine(r, 80)
	for _, want := range []string{"cliente-eppi", "2026-08-06 14:30", "42:00", "128.0 MB", "✓"} {
		if !strings.Contains(line, want) {
			t.Errorf("renderRecordLine(...) = %q, missing %q", line, want)
		}
	}
}

func TestRenderRecordLineNoTranscriptNoDuration(t *testing.T) {
	r := records.Record{ID: "2026-08-06-1000"}
	line := renderRecordLine(r, 80)
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
		line := renderRecordLine(r, width)
		if got := len([]rune(line)); got > width {
			t.Errorf("renderRecordLine(..., %d) = %q (%d runes), overflows width", width, line, got)
		}
	}
}
