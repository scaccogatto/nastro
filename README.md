<div align="center">

<img src=".github/logo.svg" alt="nastro" width="220" />

**Record your calls from the terminal. No bots, no cloud.**

System audio + mic capture, local files, on-device transcription — speaker labels one flag away.

[![CI](https://github.com/scaccogatto/nastro/actions/workflows/ci.yml/badge.svg)](https://github.com/scaccogatto/nastro/actions/workflows/ci.yml) [![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

<img src=".github/demo.gif" alt="nastro demo" width="800" />

</div>

---

Every call recorder wants to join your meeting as a bot, or ship your audio to someone's cloud. nastro does neither: it taps macOS system audio directly (CoreAudio process tap), writes a local file, and transcribes it on your machine — only when you ask.

```
nastro                      # TUI: browse, record, transcribe
nastro record               # headless recording, Ctrl+C = save
nastro records --plain      # scriptable list (tab-separated)
nastro transcribe last      # on-device transcript (diarization opt-in)
```

## Why nastro

- **No bot in the call.** Capture happens at the OS level. Nothing joins your meeting, nothing is visible to other participants.
- **No cloud.** Audio and transcripts never leave your machine. Transcription runs locally (whisper.cpp or WhisperX).
- **Who said what, one flag away.** Set `transcriber = "whisperx"` for diarized output: `[SPEAKER_00]`, `[SPEAKER_01]` — grouped by speaking turn, with SRT timestamps.
- **Never lose a call.** Audio is written incrementally: the file is valid even after a crash or `kill -9`. Transcripts are written atomically: an interrupted run never destroys an existing one. Deletes go to the Trash.
- **Terminal-native.** A fast single-binary TUI that inherits your terminal theme, with a headless CLI twin for scripting. Paths are OSC 8 hyperlinks — cmd+click opens Finder.

## Quickstart

```sh
brew install scaccogatto/tap/nastro
nastro
```

Or from source:

```sh
git clone https://github.com/scaccogatto/nastro
cd nastro && make build
./bin/nastro
```

Press `r`, name the recording (or just hit enter), and talk. `s` stops and saves. `t` transcribes.

On first recording, macOS will ask for **Screen & System Audio Recording** permission — the grant goes to your terminal app.

## Transcription

nastro is plug-and-play about its dependencies: it finds `whisper-cli`/`whisperx` even outside your `PATH`, and offers to install what's missing (via `brew`/`uv`, always with explicit confirmation).

The default backend is **whisper.cpp**, running on Metal: fast, no accounts. nastro installs it all itself, with confirmation — `brew install whisper-cpp`, then the ggml model, downloaded with progress on first use.

Want who-said-what? Set `transcriber = "whisperx"` for diarized, speaker-labeled output (`[SPEAKER_00]`, `[SPEAKER_01]`, grouped by speaking turn). It needs one manual step nastro can't do for you: a HuggingFace token with the [pyannote model terms](https://huggingface.co/pyannote/speaker-diarization-community-1) accepted — it's an access grant, not a download. Put it in your config as `hf_token` (or `$HF_TOKEN`). If anything is missing, the TUI walks you through it.

## Keys

| Key | Action |
|---|---|
| `r` | record (name + capture mode via `tab`) |
| `s` / `q` / `ctrl+c` | stop & save (while recording) |
| `x` | discard recording (with confirmation) |
| `enter` | detail view with transcript preview |
| `t` | transcribe |
| `o` | reveal in Finder |
| `d` | delete → Trash |
| `/` | filter |
| `?` | help |

## Configuration

`~/.config/nastro/config.toml` — everything has a sane default:

```toml
output_dir = "~/Recordings/nastro"
transcriber = "whisper-cli"     # or "whisperx" for speaker labels
whisper_model = "large-v3-turbo"
lang = "it"                     # transcription language
hf_token = ""                   # HuggingFace token for diarization
```

## How it works

Two binaries, one job each:

- **`nastro`** (Go, Bubble Tea) — the TUI/CLI, orchestration, transcription pipeline
- **`nastro-tap`** (Swift) — audio capture via CoreAudio process tap + mic, incremental AAC writing, a red ● in the menu bar while recording

The filesystem is the source of truth: one directory per recording (`audio.m4a`, `metadata.json`, `transcript.txt/.srt`), no database. Everything the TUI does, the CLI does headless.

## Requirements

- macOS 14.4+ (CoreAudio process tap API)
- Go + Swift toolchain to build from source

## Roadmap

Separate mic/system tracks, optional video capture, markdown export. Design docs live in [docs/](docs/).

## License

[MIT](LICENSE)
