package tui

import (
	"fmt"

	"github.com/scaccogatto/nastro/internal/records"
	"github.com/scaccogatto/nastro/internal/transcribe"
)

// recordDisplayName is how a record is named in the UI: its slug, falling
// back to its bare (timestamp) ID when it has none.
func recordDisplayName(r records.Record) string {
	if r.Slug != "" {
		return r.Slug
	}
	return r.ID
}

// confirmDeletePrompt formats the delete confirmation, naming the record and
// its duration (when known) so the prompt identifies its victim.
func confirmDeletePrompt(r records.Record) string {
	name := recordDisplayName(r)
	if r.HasDuration {
		return fmt.Sprintf("delete %s (%s)? [y/n]", name, records.FormatDuration(r.DurationSeconds))
	}
	return fmt.Sprintf("delete %s? [y/n]", name)
}

// confirmOverwritePrompt formats the transcribe-overwrite confirmation,
// naming the record.
func confirmOverwritePrompt(r records.Record) string {
	return fmt.Sprintf("%s is already transcribed, overwrite? [y/n]", recordDisplayName(r))
}

// confirmDiscardPrompt formats the recording-discard confirmation.
func confirmDiscardPrompt() string {
	return "discard this recording? [y/n]"
}

// isConfirmYes reports whether key confirms a [y/n] prompt: only "y"/"Y"
// count as yes, anything else (including no key at all) cancels.
func isConfirmYes(key string) bool {
	return key == "y" || key == "Y"
}

// approxModelSizes are rough on-disk sizes for whisper.cpp ggml models, so
// the download confirmation can give an honest heads-up before committing a
// user to a multi-GB download. Best-effort: an unlisted model just omits the
// size.
var approxModelSizes = map[string]string{
	"large-v3-turbo": "~1.6 GB",
	"large-v3":       "~3.1 GB",
	"medium":         "~1.5 GB",
	"small":          "~488 MB",
	"base":           "~148 MB",
	"tiny":           "~78 MB",
}

// formatDownloadPrompt formats the model-download confirmation, including an
// approximate size when model is a known one.
func formatDownloadPrompt(model string) string {
	if size, ok := approxModelSizes[model]; ok {
		return fmt.Sprintf("model %s missing (%s). Download now? [y/n]", model, size)
	}
	return fmt.Sprintf("model %s missing. Download now? [y/n]", model)
}

// formatToolInstallPrompt formats the missing-tool install confirmation
// (whisperx via uv, whisper-cli via brew), naming the tool, its installer,
// and an optional size/time hint.
func formatToolInstallPrompt(offer transcribe.InstallOffer) string {
	if offer.SizeHint != "" {
		return fmt.Sprintf("%s is not installed. Install it now with %s? (%s) [y/n]", offer.Tool, offer.Installer, offer.SizeHint)
	}
	return fmt.Sprintf("%s is not installed. Install it now with %s? [y/n]", offer.Tool, offer.Installer)
}
