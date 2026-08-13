package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	got := Default("/home/user")

	want := Config{
		OutputDir:                 filepath.Join("/home/user", "Recordings", "nastro"),
		WhisperModel:              "large-v3-turbo",
		Lang:                      "it",
		Transcriber:               "whisper-cli",
		MaxParallelTranscriptions: 2,
	}
	if got != want {
		t.Errorf("Default(%q) = %+v, want %+v", "/home/user", got, want)
	}
}

func TestLoadFrom(t *testing.T) {
	tests := []struct {
		name    string
		toml    string // "" means no file written
		homeDir string
		want    Config
		wantErr bool
	}{
		{
			name:    "missing file falls back to defaults",
			toml:    "",
			homeDir: "/home/user",
			want:    Default("/home/user"),
		},
		{
			name:    "empty file falls back to defaults",
			toml:    "",
			homeDir: "/home/user",
			want:    Default("/home/user"),
		},
		{
			name:    "partial override merges over defaults",
			toml:    `lang = "en"`,
			homeDir: "/home/user",
			want: Config{
				OutputDir:                 filepath.Join("/home/user", "Recordings", "nastro"),
				WhisperModel:              "large-v3-turbo",
				Lang:                      "en",
				Transcriber:               "whisper-cli",
				MaxParallelTranscriptions: 2,
			},
		},
		{
			name: "full override",
			toml: `
output_dir = "/custom/dir"
whisper_model = "small"
lang = "fr"
transcriber = "whisper-cli"
hf_token = "hf_secret"
max_parallel_transcriptions = 4
`,
			homeDir: "/home/user",
			want: Config{
				OutputDir:                 "/custom/dir",
				WhisperModel:              "small",
				Lang:                      "fr",
				Transcriber:               "whisper-cli",
				HFToken:                   "hf_secret",
				MaxParallelTranscriptions: 4,
			},
		},
		{
			name:    "malformed toml returns error",
			toml:    "not = [valid",
			homeDir: "/home/user",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")

			switch tt.name {
			case "missing file falls back to defaults":
				// don't write the file at all
			default:
				if err := os.WriteFile(path, []byte(tt.toml), 0o644); err != nil {
					t.Fatalf("os.WriteFile: %v", err)
				}
			}

			got, err := LoadFrom(path, tt.homeDir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("LoadFrom() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFrom() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("LoadFrom() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolvedHFToken(t *testing.T) {
	tests := []struct {
		name      string
		cfgToken  string
		envToken  string
		wantToken string
	}{
		{"config field wins over env", "hf_config", "hf_env", "hf_config"},
		{"empty field falls back to env", "", "hf_env", "hf_env"},
		{"both empty", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HF_TOKEN", tt.envToken)
			cfg := Config{HFToken: tt.cfgToken}
			if got := cfg.ResolvedHFToken(); got != tt.wantToken {
				t.Errorf("ResolvedHFToken() = %q, want %q", got, tt.wantToken)
			}
		})
	}
}

func TestResolvedMaxParallel(t *testing.T) {
	tests := []struct {
		name string
		val  int
		want int
	}{
		{"configured value", 4, 4},
		{"zero floors to 1", 0, 1},
		{"negative floors to 1", -3, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{MaxParallelTranscriptions: tt.val}
			if got := cfg.ResolvedMaxParallel(); got != tt.want {
				t.Errorf("ResolvedMaxParallel() = %d, want %d", got, tt.want)
			}
		})
	}
}
