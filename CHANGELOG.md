# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Releases up to v0.2.2 are described in their [GitHub release notes](https://github.com/scaccogatto/nastro/releases).

## [Unreleased]

### Added

- `nastro help` (also `-h`, `--help`) prints usage, and `nastro version` (also `-v`, `--version`) prints the build version. `make` injects it from `git describe`, or from `make VERSION=...` when building a release tarball.
- Downloaded ggml models are verified against their SHA-256 (the huggingface.co LFS oid) for every model published by ggerganov/whisper.cpp. A mismatch fails the download and leaves nothing in place. Custom model names skip the check.

### Changed

- **Breaking:** `lang` now defaults to `"auto"` (whisper-cli `-l auto`, whisperx language detection) instead of `"it"`. Set `lang = "it"` in `~/.config/nastro/config.toml` to keep forcing Italian.
- Known ggml models are downloaded from a pinned huggingface.co commit instead of the moving `main` branch.
- Unknown commands print the full usage.
- Builds require Go 1.26.9 or later (the `go` directive in go.mod), which fixes reachable standard library vulnerabilities in `net/http` and `crypto/tls`.

### Security

- New recording directories (and the output directory, when nastro creates it) are created `0700`, and transcripts, `transcribe.log`, the lockfile and the recording itself (`nastro-tap` runs with umask `077`) are `0600`. Other accounts on a shared Mac can no longer read them. Existing directories keep their permissions: `chmod -R go-rwx ~/Recordings/nastro` tightens them.
- The HuggingFace token reaches whisperx through the `HF_TOKEN` environment variable instead of `--hf_token`, so it no longer shows up in `ps` output for the length of a diarization run.

[Unreleased]: https://github.com/scaccogatto/nastro/compare/v0.2.2...HEAD
