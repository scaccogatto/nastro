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
