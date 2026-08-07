package tui

import (
	"fmt"
	"io"

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
// transcript" in aligned columns (see docs/flows.md's records screen).
type recordDelegate struct{}

func (recordDelegate) Height() int  { return 1 }
func (recordDelegate) Spacing() int { return 0 }

func (recordDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (recordDelegate) Render(w io.Writer, m list.Model, index int, it list.Item) {
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

	line := renderRecordLine(ri.r, m.Width()-len([]rune(marker)))
	fmt.Fprint(w, style.Render(marker+line)) //nolint:errcheck
}

// renderRecordLine formats one record as a single, column-aligned line that
// fits within width columns, truncating the name (slug or ID) first since
// it's the only variable-length field.
func renderRecordLine(r records.Record, width int) string {
	name := recordDisplayName(r)
	date := r.Date.Format("2006-01-02 15:04")
	duration := "-"
	if r.HasDuration {
		duration = records.FormatDuration(r.DurationSeconds)
	}
	size := records.FormatSize(r.SizeBytes)
	transcript := "-"
	if r.HasTranscript {
		transcript = "✓"
	}

	const (
		dateW = 16 // len("2006-01-02 15:04")
		durW  = 7
		sizeW = 8
		gap   = 2
	)
	fixed := dateW + durW + sizeW + 1 + gap*4
	nameW := width - fixed
	if nameW < 3 {
		nameW = 3
	}
	name = ansi.Truncate(name, nameW, "…")

	line := fmt.Sprintf("%-*s  %s  %*s  %*s  %s", nameW, name, date, durW, duration, sizeW, size, transcript)
	return clampWidth(line, width)
}
