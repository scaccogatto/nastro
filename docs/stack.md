# Stack

## Decision

| Component | Choice | Role |
|---|---|---|
| TUI/CLI | **Go + Bubble Tea v2** | `nastro` binary, all interface and orchestration |
| UI Components | Bubbles v2 (list, spinner, timer, help, key) | records/record screens |
| Styling | Lip Gloss | theme (Catppuccin palette, consistent with rest of setup) |
| Options form | huh | `record` options in TUI |
| Audio capture | **Swift + CoreAudio process tap** (`nastro-tap` binary) | system audio + mic, macOS 14.4+ |
| Transcription | **whisper.cpp** (`whisper-cli`, subprocess) | local, ggml models, `-l it` |
| Video (phase 2) | ScreenCaptureKit (extension to `nastro-tap`) | optional |

## Why Bubble Tea

- **Elm Architecture** (Model → Update → View): pure FP, side effects confined to `tea.Cmd`. Consistent with personal rule "at equal maturity, choose the FP option".
- Maximum maturity in the field: 44K⭐, daily pushes, charmbracelet ecosystem (bubbles/lipgloss/huh) covers exactly the planned screens (verified on context7: custom-delegate lists, spinner/timer, subprocess execution with `tea.ExecProcess`).
- Single static binary: trivial brew distribution, runs cleanly in a herdr pane.
- Go: learnable in a day from TS; TUI code is almost all declarative patterns.

## Evaluated alternatives (2026-08-06, stars and push activity verified)

| Framework | Status | Verdict |
|---|---|---|
| ratatui (Rust) | 22.1K⭐, push today | Most robust, but Rust toolchain absent on machine and steeper curve. Runner-up |
| opentui (TS) | 12.9K⭐, push today, 214 open issues | Native language (TS), but young and unstable API. Re-evaluate in a year |
| ink (React/TS) | 39.6K⭐, active | Great for interactive output, weak for full-screen apps |
| textual (Python) | 36.9K⭐, push Jul 2026 | Mature and well-documented, but Python not in daily workflow |

Community sentiment (run /last30days 2026-08-06): no fresh direct comparison in the window; TUI library builders cite "ratatui and bubbles" as references to study (r/tui). Reddit partial due to rate-limit: weak signal, decision made on structural factors.

## Architecture (design, not implementation)

```
nastro (Go, TUI+CLI)
  ├─ exec → nastro-tap (Swift)     # capture; progress on stdout (JSON lines)
  │           └─ writes .m4a/.wav incrementally to ~/Recordings/nastro/<slug>/
  └─ exec → whisper-cli            # transcription; .txt/.srt alongside audio
```

- **Filesystem as source of truth**: one directory per recording, no database. `records` = directory scan.
- Config: `~/.config/nastro/config.toml` (output dir, whisper model, language, format).
- TCC constraint: recording permission goes to host terminal (Ghostty), one-time prompt. Document in README; acceptable for personal use, known distribution limit.

## Capture sample rates

`kAudioTapPropertyFormat` reports 48 kHz whatever the tapped device actually runs at. Measured on a 44.1 kHz output device: tap format 48000, aggregate device nominal rate 44100, real IOProc delivery ~44100 frames/s, both before and after the aggregate device exists. Trusting the tap format wrote 44.1 kHz audio into a file labelled 48 kHz (pitch +9%), and in mixed mode made `SampleMixer` drain 960 frames per 20 ms tick from a buffer filling with 882, so `AudioRingBuffer` zero-filled the shortfall: ~45000 holes in a 50 minute recording, transcription reduced to hallucinated filler.

Rules now applied in `nastro-tap`:

| Rule | Why |
|---|---|
| The aggregate device's `kAudioDevicePropertyNominalSampleRate` is the source of truth for tap data, not the tap format | the tap format is a fixed default, not the stream's real rate |
| Every source is resampled into the file's format at ingestion (`FormatConverter`) | mic and system audio run on independent clocks and rates, and a Bluetooth headset moves between them |
| Delivered frames are counted against the wall clock, and the assumed rate is corrected when off by more than 2% | no declared rate is trusted blindly, including the one above |
| Output device changes, device rate changes and mic route changes rebuild capture in place | the file's format is fixed for the life of the recording, so the converters absorb the change |
| The mixer emits the frames the wall clock owes, not a fixed count per tick | a `DispatchSourceTimer` is not a sample clock; the file's duration must match real time |
| The mic tap is reinstalled with `format: nil`, and the engine is rebuilt from scratch if it will not restart | passing a format read a moment earlier raises an uncatchable NSException that kills the capture process, and a wedged engine means no microphone for the rest of the recording |
