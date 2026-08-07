// Package transcribe implements `nastro transcribe <id|last>`: resolving the
// target record and driving whisper-cli via afconvert.
package transcribe

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/records"
)

// ResolveRecord picks the record matching idOrLast: "last" is the most
// recently created record, anything else must match a dirname exactly.
func ResolveRecord(recs []records.Record, idOrLast string) (records.Record, error) {
	if idOrLast == "last" {
		if len(recs) == 0 {
			return records.Record{}, fmt.Errorf("record not found: last")
		}
		latest := recs[0]
		for _, r := range recs[1:] {
			if r.Date.After(latest.Date) {
				latest = r
			}
		}
		return latest, nil
	}

	for _, r := range recs {
		if r.ID == idOrLast {
			return r, nil
		}
	}
	return records.Record{}, fmt.Errorf("record not found: %s", idOrLast)
}

// ModelPath returns the expected on-disk path of a whisper.cpp ggml model.
func ModelPath(homeDir, model string) string {
	return filepath.Join(homeDir, ".cache", "whisper", "ggml-"+model+".bin")
}

// --- everything below is side-effecting orchestration: subprocess exec,
// filesystem, stdin prompts. Deliberately outside TDD scope. ---

// Run resolves idOrLast against cfg.OutputDir and transcribes it with
// whisper-cli, converting the source audio to WAV via afconvert first.
func Run(cfg config.Config, idOrLast string) error {
	recs, err := records.Scan(cfg.OutputDir)
	if err != nil {
		return fmt.Errorf("scan records: %w", err)
	}
	rec, err := ResolveRecord(recs, idOrLast)
	if err != nil {
		return err
	}

	if _, err := exec.LookPath("whisper-cli"); err != nil {
		return fmt.Errorf("whisper-cli not found. Install it with: brew install whisper-cpp")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	modelPath := ModelPath(home, cfg.WhisperModel)
	if _, err := os.Stat(modelPath); err != nil {
		return fmt.Errorf("whisper model not found at %s\ndownload it from: https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-%s.bin", modelPath, cfg.WhisperModel)
	}

	recordDir := filepath.Join(cfg.OutputDir, rec.ID)
	transcriptPath := filepath.Join(recordDir, "transcript.txt")
	if _, err := os.Stat(transcriptPath); err == nil {
		fmt.Print("record already transcribed. Overwrite? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("aborted, nothing changed")
			return nil
		}
	}

	audioPath := filepath.Join(recordDir, "audio.m4a")
	tmpWav, err := os.CreateTemp("", "nastro-transcribe-*.wav")
	if err != nil {
		return fmt.Errorf("create temp wav: %w", err)
	}
	tmpWavPath := tmpWav.Name()
	tmpWav.Close()
	defer os.Remove(tmpWavPath)

	convert := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@16000", "-c", "1", audioPath, tmpWavPath)
	convert.Stderr = os.Stderr
	if err := convert.Run(); err != nil {
		return fmt.Errorf("afconvert: %w", err)
	}

	transcribeOut := filepath.Join(recordDir, "transcript")
	whisper := exec.Command("whisper-cli", "-m", modelPath, "-l", cfg.Lang, "-otxt", "-osrt", "-of", transcribeOut, "-f", tmpWavPath)
	whisper.Stdout = os.Stdout
	whisper.Stderr = os.Stderr
	if err := whisper.Run(); err != nil {
		return fmt.Errorf("whisper-cli: %w", err)
	}

	return nil
}
