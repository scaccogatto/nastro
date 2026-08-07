// Package config loads nastro's configuration from ~/.config/nastro/config.toml,
// falling back to in-memory defaults when the file is absent. It never writes
// the file.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config holds nastro's runtime settings.
type Config struct {
	OutputDir    string `toml:"output_dir"`
	WhisperModel string `toml:"whisper_model"`
	Lang         string `toml:"lang"`
	// Transcriber selects the transcribe backend: "whisperx" (default,
	// speaker diarization) or "whisper-cli" (no diarization).
	Transcriber string `toml:"transcriber"`
	// HFToken is the HuggingFace access token whisperx needs to download
	// pyannote's gated diarization models. See ResolvedHFToken for the
	// $HF_TOKEN environment fallback. Never logged.
	HFToken string `toml:"hf_token"`
}

// Default returns the built-in defaults, rooted at homeDir.
func Default(homeDir string) Config {
	return Config{
		OutputDir:    filepath.Join(homeDir, "Recordings", "nastro"),
		WhisperModel: "large-v3-turbo",
		Lang:         "it",
		Transcriber:  "whisperx",
	}
}

// ResolvedHFToken returns c.HFToken, falling back to the $HF_TOKEN
// environment variable only when the config field itself is empty -- an
// explicit config value always wins over the environment.
func (c Config) ResolvedHFToken() string {
	if c.HFToken != "" {
		return c.HFToken
	}
	return os.Getenv("HF_TOKEN")
}

// LoadFrom reads the TOML file at path and merges it over the defaults for
// homeDir. A missing file is not an error: the defaults are returned as-is.
func LoadFrom(path, homeDir string) (Config, error) {
	cfg := Default(homeDir)

	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, err
	}
	return cfg, nil
}

// Load resolves the real config path under the user's home directory and
// loads it. Thin, untested wrapper around LoadFrom.
func Load() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	return LoadFrom(filepath.Join(home, ".config", "nastro", "config.toml"), home)
}
