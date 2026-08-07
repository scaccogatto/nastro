package transcribe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func TestModelDownloadURL(t *testing.T) {
	got := ModelDownloadURL("large-v3-turbo")
	want := "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin"
	if got != want {
		t.Errorf("ModelDownloadURL() = %q, want %q", got, want)
	}
}

func TestDownloadPercent(t *testing.T) {
	tests := []struct {
		name       string
		downloaded int64
		total      int64
		want       float64
	}{
		{"halfway", 50, 100, 0.5},
		{"complete", 100, 100, 1},
		{"unknown total", 50, 0, 0},
		{"negative total", 50, -1, 0},
		{"overshoot clamps to 1", 150, 100, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DownloadPercent(tt.downloaded, tt.total)
			if got != tt.want {
				t.Errorf("DownloadPercent(%d, %d) = %v, want %v", tt.downloaded, tt.total, got, tt.want)
			}
		})
	}
}

func TestStartModelDownloadSuccess(t *testing.T) {
	content := strings.Repeat("nastro-model-bytes", 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		_, _ = w.Write([]byte(content))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "model.bin")
	job := StartModelDownload(srv.URL, dest)

	var lastPct float64
	for p := range job.Progress() {
		lastPct = DownloadPercent(p.Downloaded, p.Total)
	}
	if err := <-job.Wait(); err != nil {
		t.Fatalf("download error: %v", err)
	}
	if lastPct != 1 {
		t.Errorf("last progress tick = %v, want 1 (complete)", lastPct)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != content {
		t.Errorf("downloaded content mismatch: got %d bytes, want %d", len(got), len(content))
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part file still exists after successful download")
	}
}

func TestStartModelDownloadCancelCleansUpPartFile(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("some-bytes-before-cancel"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-block // keep the connection open until the test cancels
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	dest := filepath.Join(t.TempDir(), "model.bin")
	job := StartModelDownload(srv.URL, dest)

	// Wait for at least one progress tick so we know the .part file exists,
	// then cancel.
	<-job.Progress()
	job.Cancel()

	err := <-job.Wait()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("download error = %v, want context.Canceled", err)
	}

	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("destination file exists after cancel")
	}
	if _, statErr := os.Stat(dest + ".part"); !os.IsNotExist(statErr) {
		t.Errorf(".part file still exists after cancel")
	}
}
