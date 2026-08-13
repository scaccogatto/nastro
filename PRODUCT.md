# Product

<!-- impeccable:product-schema 1 -->

## Platform

terminal (TUI) — outside web/ios/android/adaptive schema values: nastro is a terminal app (Go/Bubble Tea) for macOS, no web or native mobile surface.

## Users

Developers who live in the terminal and do calls (Meet/Zoom/Teams) with clients or team, wanting local recording + transcription without bot in call nor cloud. First user and design partner: the author (freelancer, Italian calls with clients, herdr/Ghostty workflow). Confirmed destination: **public open source** (public GitHub, brew tap, curated docs for third parties).

## Product Purpose

Record calls (system audio + microphone, optionally video in future) from terminal, in local files, and transcribe them on-device with whisper.cpp when user asks. Success: never lose a call, never depend on external service, transcription usable in Italian.

## Positioning

Only terminal call recorder: no bot entering call (capture via CoreAudio process tap), no cloud (files and transcription stay on machine), terminal interface with headless equivalent for scripting. Neighbors are either GUI (QuickRecorder), or bot/cloud (Fireflies, tl;dv), or continuous streams (Screenpipe).

## Operating Context

- macOS 14.4+ (CoreAudio process tap); TCC capture permission goes to host terminal, not the binary
- Typically runs in a herdr pane inside Ghostty; paths in TUI are clickable OSC 8 links
- Filesystem as source of truth: one directory per recording in `~/Recordings/nastro/`, no database
- Transcription: whisper.cpp (`whisper-cli`) + local ggml model by default (fast, zero accounts); WhisperX with pyannote diarization as guided opt-in (`transcriber = "whisperx"`, HF token). Default model `large-v3-turbo`, default language `it`
- Config: `~/.config/nastro/config.toml`, defaults in memory, never written by tool

## Capabilities and Constraints

- Commands: `nastro` (TUI), `record`, `records --plain`, `transcribe <id|last>`; every TUI flow has headless CLI equivalent
- Two-binary architecture: `nastro` (Go) orchestrating + `nastro-tap` (Swift) for capture; communication via stdout (levels `level sys=.. mic=..`) and exit code (2 = missing permission)
- Guarantees: incremental write (file valid even after crash), periodic metadata (crash loses ≤5s of duration), lockfile with staleness detection, caffeinate anti-sleep, records <2s discarded
- **UI language: English, decided and applied** (2026-08-07). Italian remains only as default transcription language
- Confirmed Won't: no AI summarization/cloud, no GUI (exception: indicator ● NSStatusItem best-effort), no auto-start on meeting detection
- Video: phase 2 declared, not implemented

## Brand Commitments

Name `nastro` (Italian for "tape", verified free on GitHub/crates/npm/brew, docs/naming.md); helper binary `nastro-tap`. No logo or visual identity defined.

## Evidence on Hand

- docs/: naming.md, stack.md, features.md (MoSCoW), flows.md (screens, states, edge cases) — design decisions dated 2026-08-06/07
- Working codebase with 66 green Go tests (including -race) and verified real e2e (valid afinfo recording, Italian transcription on Metal)
- No third-party users, testimonials, or benchmarks: do not fabricate

## Product Principles

1. **Never lose a call**: every technical choice privileges recorded file integrity (incremental write, clean stops, explicit discards)
2. **Local by choice, not by limit**: no cloud and no bot are positioning, not gaps to fill
3. **TUI as view, CLI as contract**: everything feasible on screen is feasible headless and composable in scripts
4. **Capture is the job, rest is cosmetics**: auxiliary features (menu bar indicator, VU meter) degrade silently, never at cost of recording
5. **One real user before a thousand hypothetical**: features grow from author's real workflow, then generalize for open source
