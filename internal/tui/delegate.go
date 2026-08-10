package tui

import (
	"fmt"
	"io"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/scaccogatto/nastro/internal/records"
)

// recordMarker is the selection indicator: a shape, not just a color, so
// selection reads even without color (accessibility: severity/selection
// should never depend on color alone).
const (
	recordMarkerSelected = "▸ "
	recordMarkerNormal   = "  "
)

// recordDelegate is a compact, 1-line-per-record list.ItemDelegate,
// replacing bubbles' 2-line DefaultDelegate: "name  date  duration  size
// status" in aligned columns (see docs/flows.md's records screen). jobs is
// the Model's transcribeJobs registry, shared by reference (see New's doc
// comment) so the status column stays live without re-wiring the delegate.
type recordDelegate struct {
	jobs map[string]*transcribeJobState
}

func (recordDelegate) Height() int  { return 1 }
func (recordDelegate) Spacing() int { return 0 }

func (recordDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d recordDelegate) Render(w io.Writer, m list.Model, index int, it list.Item) {
	ri, ok := it.(item)
	if !ok {
		return
	}

	selected := index == m.Index() && m.FilterState() != list.Filtering
	marker := recordMarkerNormal
	style := normalRow
	if selected {
		marker = recordMarkerSelected
		style = selectedRow
	}

	line := renderRecordLine(ri.r, d.jobs[ri.r.ID], time.Now(), m.Width()-len([]rune(marker)))
	fmt.Fprint(w, style.Render(marker+line)) //nolint:errcheck
}

// renderRecordLine formats one record as a single, column-aligned line that
// fits within width columns, truncating the name (slug or ID) first since
// it's the only variable-length field. st is the record's active job (nil
// if none), now the wall-clock time used to animate a live spinner cell.
func renderRecordLine(r records.Record, st *transcribeJobState, now time.Time, width int) string {
	name := recordDisplayName(r)
	date := r.Date.Format("2006-01-02 15:04")
	duration := "-"
	if r.HasDuration {
		duration = records.FormatDuration(r.DurationSeconds)
	}
	size := records.FormatSize(r.SizeBytes)
	status := transcribeStatusText(r, st, now)

	const (
		dateW   = 16 // len("2006-01-02 15:04")
		durW    = 7
		sizeW   = 8
		statusW = 6 // fixed, so "queued"/"NN%"/"-"/"✓" never shift the columns after it
		gap     = 2
	)
	fixed := dateW + durW + sizeW + statusW + gap*4
	nameW := width - fixed
	if nameW < 3 {
		nameW = 3
	}
	name = ansi.Truncate(name, nameW, "…")

	line := fmt.Sprintf("%-*s  %s  %*s  %*s  %*s", nameW, name, date, durW, duration, sizeW, size, statusW, status)
	return clampWidth(line, width)
}
