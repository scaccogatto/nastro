package records

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseMetadata(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantDur float64
		wantOK  bool
	}{
		{"valid", `{"duration_seconds": 42.5}`, 42.5, true},
		{"missing field defaults to zero", `{}`, 0, true},
		{"malformed json", `not json`, 0, false},
		{"empty", ``, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDur, gotOK := ParseMetadata([]byte(tt.content))
			if gotOK != tt.wantOK {
				t.Fatalf("ParseMetadata(%q) ok = %v, want %v", tt.content, gotOK, tt.wantOK)
			}
			if gotOK && gotDur != tt.wantDur {
				t.Errorf("ParseMetadata(%q) duration = %v, want %v", tt.content, gotDur, tt.wantDur)
			}
		})
	}
}

func TestParseDirName(t *testing.T) {
	tests := []struct {
		name     string
		dirName  string
		wantDate string // RFC3339, "" means wantOK == false
		wantSlug string
		wantOK   bool
	}{
		{
			name:     "timestamp with slug",
			dirName:  "2026-08-06-1430-cliente-eppi",
			wantDate: "2026-08-06T14:30:00Z",
			wantSlug: "cliente-eppi",
			wantOK:   true,
		},
		{
			name:     "timestamp only, no slug",
			dirName:  "2026-08-05-1000",
			wantDate: "2026-08-05T10:00:00Z",
			wantSlug: "",
			wantOK:   true,
		},
		{
			name:    "not a timestamp",
			dirName: ".lock",
			wantOK:  false,
		},
		{
			name:    "empty string",
			dirName: "",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			date, slug, ok := ParseDirName(tt.dirName)
			if ok != tt.wantOK {
				t.Fatalf("ParseDirName(%q) ok = %v, want %v", tt.dirName, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			wantDate, err := time.Parse(time.RFC3339, tt.wantDate)
			if err != nil {
				t.Fatalf("bad test fixture date: %v", err)
			}
			if !date.Equal(wantDate) {
				t.Errorf("ParseDirName(%q) date = %v, want %v", tt.dirName, date, wantDate)
			}
			if slug != tt.wantSlug {
				t.Errorf("ParseDirName(%q) slug = %q, want %q", tt.dirName, slug, tt.wantSlug)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		seconds float64
		want    string
	}{
		{0, "00:00"},
		{5, "00:05"},
		{65, "01:05"},
		{599, "09:59"},
		{3599, "59:59"},
		{3600, "1:00:00"},
		{3661, "1:01:01"},
		{7325, "2:02:05"},
	}

	for _, tt := range tests {
		got := FormatDuration(tt.seconds)
		if got != tt.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{int64(2.5 * 1024 * 1024), "2.5 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
	}

	for _, tt := range tests {
		got := FormatSize(tt.bytes)
		if got != tt.want {
			t.Errorf("FormatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestTranscriptPreview(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		maxLines int
		want     []string
	}{
		{"shorter than max", "one\ntwo\nthree", 15, []string{"one", "two", "three"}},
		{"exactly max", "a\nb", 2, []string{"a", "b"}},
		{"truncated", "a\nb\nc\nd", 2, []string{"a", "b"}},
		{"empty content", "", 15, []string{""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TranscriptPreview(tt.content, tt.maxLines)
			if len(got) != len(tt.want) {
				t.Fatalf("TranscriptPreview() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("TranscriptPreview()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()

	// record 1: full fixture with metadata and transcript
	dir1 := filepath.Join(root, "2026-08-05-1000-standup")
	mustMkdir(t, dir1)
	mustWriteFile(t, filepath.Join(dir1, "audio.m4a"), make([]byte, 2048))
	mustWriteJSON(t, filepath.Join(dir1, "metadata.json"), map[string]float64{"duration_seconds": 125.5})
	mustWriteFile(t, filepath.Join(dir1, "transcript.txt"), []byte("hello"))

	// record 2: no slug, no metadata (crash case), no transcript
	dir2 := filepath.Join(root, "2026-08-06-1430")
	mustMkdir(t, dir2)
	mustWriteFile(t, filepath.Join(dir2, "audio.m4a"), make([]byte, 100))

	// not a record dir: doesn't match the timestamp pattern, must be skipped
	mustMkdir(t, filepath.Join(root, "not-a-record"))

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan() error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Scan() returned %d records, want 2: %+v", len(got), got)
	}

	// newest first
	if got[0].ID != "2026-08-06-1430" {
		t.Errorf("got[0].ID = %q, want %q", got[0].ID, "2026-08-06-1430")
	}
	if got[0].SizeBytes != 100 {
		t.Errorf("got[0].SizeBytes = %d, want 100", got[0].SizeBytes)
	}
	if got[0].HasDuration {
		t.Errorf("got[0].HasDuration = true, want false (no metadata.json)")
	}
	if got[0].HasTranscript {
		t.Errorf("got[0].HasTranscript = true, want false")
	}

	if got[1].ID != "2026-08-05-1000-standup" {
		t.Errorf("got[1].ID = %q, want %q", got[1].ID, "2026-08-05-1000-standup")
	}
	if got[1].Slug != "standup" {
		t.Errorf("got[1].Slug = %q, want %q", got[1].Slug, "standup")
	}
	if !got[1].HasDuration || got[1].DurationSeconds != 125.5 {
		t.Errorf("got[1] duration = (%v, %v), want (true, 125.5)", got[1].HasDuration, got[1].DurationSeconds)
	}
	if got[1].SizeBytes != 2048 {
		t.Errorf("got[1].SizeBytes = %d, want 2048", got[1].SizeBytes)
	}
	if !got[1].HasTranscript {
		t.Errorf("got[1].HasTranscript = false, want true")
	}
}

func TestScanMissingRootDirReturnsEmpty(t *testing.T) {
	got, err := Scan(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("Scan() on missing dir error: %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Scan() on missing dir = %+v, want empty", got)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func mustWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	mustWriteFile(t, path, b)
}
