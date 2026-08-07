// Package records scans an output directory for nastro recordings and
// formats them for display. The filesystem is the source of truth: each
// subdirectory of the output dir is one record, no database involved.
package records

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// dirTimeLayout is the timestamp prefix every record directory starts with.
const dirTimeLayout = "2006-01-02-1504"

// Record describes one recording, derived from its directory.
type Record struct {
	ID              string
	Date            time.Time
	Slug            string
	DurationSeconds float64
	HasDuration     bool
	SizeBytes       int64
	HasTranscript   bool
}

// ParseDirName splits a record directory name into its timestamp and slug.
// ok is false when name doesn't start with a valid dirTimeLayout timestamp.
func ParseDirName(name string) (date time.Time, slug string, ok bool) {
	tsLen := len(dirTimeLayout)
	if len(name) < tsLen {
		return time.Time{}, "", false
	}

	date, err := time.Parse(dirTimeLayout, name[:tsLen])
	if err != nil {
		return time.Time{}, "", false
	}
	if len(name) == tsLen {
		return date, "", true
	}
	if name[tsLen] != '-' {
		return time.Time{}, "", false
	}
	return date, name[tsLen+1:], true
}

// Scan reads rootDir and builds one Record per valid record subdirectory,
// sorted newest first. Non-directories and directories whose name isn't a
// valid record timestamp are skipped.
func Scan(rootDir string) ([]Record, error) {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var out []Record
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		date, slug, ok := ParseDirName(e.Name())
		if !ok {
			continue
		}
		out = append(out, buildRecord(filepath.Join(rootDir, e.Name()), e.Name(), date, slug))
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out, nil
}

func buildRecord(dirPath, id string, date time.Time, slug string) Record {
	rec := Record{ID: id, Date: date, Slug: slug}

	if info, err := os.Stat(filepath.Join(dirPath, "audio.m4a")); err == nil {
		rec.SizeBytes = info.Size()
	}

	if b, err := os.ReadFile(filepath.Join(dirPath, "metadata.json")); err == nil {
		var meta struct {
			DurationSeconds float64 `json:"duration_seconds"`
		}
		if json.Unmarshal(b, &meta) == nil {
			rec.DurationSeconds = meta.DurationSeconds
			rec.HasDuration = true
		}
	}

	if _, err := os.Stat(filepath.Join(dirPath, "transcript.txt")); err == nil {
		rec.HasTranscript = true
	}

	return rec
}

// FormatDuration renders seconds as mm:ss, or h:mm:ss past an hour.
func FormatDuration(seconds float64) string {
	total := int64(seconds + 0.5)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// FormatSize renders bytes as a human-readable size (B/KB/MB/GB/TB).
func FormatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}

	units := []string{"KB", "MB", "GB", "TB"}
	val := float64(bytes)
	idx := -1
	for val >= 1024 && idx < len(units)-1 {
		val /= 1024
		idx++
	}
	return fmt.Sprintf("%.1f %s", val, units[idx])
}

// PlainLine renders r as a tab-separated line for `nastro records --plain`.
func PlainLine(r Record) string {
	duration := "-"
	if r.HasDuration {
		duration = FormatDuration(r.DurationSeconds)
	}
	transcript := "-"
	if r.HasTranscript {
		transcript = "✓"
	}
	return strings.Join([]string{
		r.ID,
		r.Date.Format("2006-01-02 15:04"),
		r.Slug,
		duration,
		FormatSize(r.SizeBytes),
		transcript,
	}, "\t")
}
