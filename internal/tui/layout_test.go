package tui

import "testing"

func TestClampWidth(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"fits exactly", "hello", 5, "hello"},
		{"shorter than width", "hi", 10, "hi"},
		{"needs truncation", "hello world", 8, "hello w…"},
		{"zero width", "hello", 0, ""},
		{"negative width", "hello", -1, ""},
		{"width one", "hello", 1, "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampWidth(tt.s, tt.width); got != tt.want {
				t.Errorf("clampWidth(%q, %d) = %q, want %q", tt.s, tt.width, got, tt.want)
			}
		})
	}
}

func TestTruncateMiddle(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"fits exactly", "/tmp/short", 10, "/tmp/short"},
		{"shorter than width", "/tmp", 20, "/tmp"},
		{"needs truncation", "/Users/alex/Recordings/nastro/2026-08-07-1430-cliente-eppi", 20, "/Users/al…iente-eppi"},
		{"zero width", "/tmp/x", 0, ""},
		{"negative width", "/tmp/x", -5, ""},
		{"width one", "/tmp/x", 1, "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateMiddle(tt.s, tt.width)
			if got != tt.want {
				t.Errorf("truncateMiddle(%q, %d) = %q, want %q", tt.s, tt.width, got, tt.want)
			}
			if len([]rune(got)) > tt.width && tt.width > 0 {
				t.Errorf("truncateMiddle(%q, %d) = %q, longer than width", tt.s, tt.width, got)
			}
		})
	}
}

func TestTruncatedPathLine(t *testing.T) {
	short := truncatedPathLine("saved ", "/tmp/x", 40)
	if want := "saved " + hyperlink("/tmp/x", "/tmp/x"); short != want {
		t.Errorf("truncatedPathLine short path = %q, want %q", short, want)
	}

	long := "/Users/alex/Recordings/nastro/2026-08-07-1430-cliente-eppi-molto-lungo"
	got := truncatedPathLine("saved ", long, 30)
	// The hyperlink target must stay the untruncated path even though the
	// displayed text is shortened.
	if want := "saved " + hyperlink(truncateMiddle(long, 30-len("saved ")), long); got != want {
		t.Errorf("truncatedPathLine long path = %q, want %q", got, want)
	}
}
