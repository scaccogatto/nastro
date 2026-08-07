package tui

import "testing"

func TestNextCaptureModeCycles(t *testing.T) {
	tests := []struct {
		from, want captureMode
	}{
		{captureMixed, captureMicOnly},
		{captureMicOnly, captureSystemOnly},
		{captureSystemOnly, captureMixed},
	}
	for _, tt := range tests {
		if got := nextCaptureMode(tt.from); got != tt.want {
			t.Errorf("nextCaptureMode(%v) = %v, want %v", tt.from, got, tt.want)
		}
	}
}

func TestCaptureModeLabel(t *testing.T) {
	tests := []struct {
		mode captureMode
		want string
	}{
		{captureMixed, "mixed"},
		{captureMicOnly, "mic-only"},
		{captureSystemOnly, "system-only"},
	}
	for _, tt := range tests {
		if got := captureModeLabel(tt.mode); got != tt.want {
			t.Errorf("captureModeLabel(%v) = %q, want %q", tt.mode, got, tt.want)
		}
	}
}

func TestCaptureModeOptions(t *testing.T) {
	tests := []struct {
		mode                   captureMode
		wantMic, wantSystemOnl bool
	}{
		{captureMixed, false, false},
		{captureMicOnly, true, false},
		{captureSystemOnly, false, true},
	}
	for _, tt := range tests {
		micOnly, systemOnly := captureModeOptions(tt.mode)
		if micOnly != tt.wantMic || systemOnly != tt.wantSystemOnl {
			t.Errorf("captureModeOptions(%v) = (%v, %v), want (%v, %v)", tt.mode, micOnly, systemOnly, tt.wantMic, tt.wantSystemOnl)
		}
	}
}

func TestAbbreviateHome(t *testing.T) {
	tests := []struct {
		name, path, homeDir, want string
	}{
		{"under home", "/Users/alex/Recordings/nastro", "/Users/alex", "~/Recordings/nastro"},
		{"exactly home", "/Users/alex", "/Users/alex", "~"},
		{"unrelated path", "/tmp/other", "/Users/alex", "/tmp/other"},
		{"empty homeDir leaves path alone", "/Users/alex/Recordings", "", "/Users/alex/Recordings"},
		{"prefix but not a path boundary", "/Users/alexander/Recordings", "/Users/alex", "/Users/alexander/Recordings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := abbreviateHome(tt.path, tt.homeDir); got != tt.want {
				t.Errorf("abbreviateHome(%q, %q) = %q, want %q", tt.path, tt.homeDir, got, tt.want)
			}
		})
	}
}
