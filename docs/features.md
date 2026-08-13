# Features (auto-workshop 2026-08-06)

Use context: Italian Google Meet calls with clients from Ghostty/herdr, all local, self-contained transcription. Single user (for now).

## Must (v0.1)

| Feature | Note |
|---|---|
| Capture system audio + mic, mixed | via `nastro-tap`; macOS 14.4+ |
| `nastro record` headless | works without TUI: starts, shows timer, Ctrl+C stops and saves |
| Incremental write | file is valid even after crash/kill: never lose a call |
| Ordered output | `~/Recordings/nastro/<YYYY-MM-DD-HHmm>-<slug>/audio.m4a` |
| `nastro records` | list: date, slug, duration, size, transcript status (✓/-) |
| `nastro transcribe <id\|last>` | whisper-cli (default, whisper.cpp on Metal, no accounts, no diarization) with `-l it` by default, output `.txt` + `.srt` alongside audio. `transcriber = "whisperx"` in config opts into speaker diarization (`[SPEAKER_NN]` prefix, requires HuggingFace token: `hf_token` in config or `$HF_TOKEN`). Plug-and-play dependencies: whisper-cli/whisperx/uv found outside PATH too (`~/.local/bin`, Homebrew), and if missing nastro offers to install them (explicit confirmation, never automatic) |
| TOML config | `~/.config/nastro/config.toml`: output dir, model, language, backend (`transcriber`), `hf_token` |
| Timer + levels in TUI | feedback that recording is really happening (basic VU meter) |

## Should (v0.2)

| Feature | Note |
|---|---|
| Separate mic/system tracks | two files: enables "poor man's" diarization (me vs them) without ML |
| Post-recording hook | arbitrary command from config (e.g. auto-transcribe) |
| macOS notification at transcribe end | long transcriptions run in background |
| `records`: actions on list | enter=detail, `t`=transcribe, `o`=open in Finder, `d`=delete (with confirmation) |
| Download whisper model on first use | `nastro transcribe` fetches ggml if absent, with progress |

## Could (phase 2+)

| Feature | Note |
|---|---|
| Optional video (`record --video`) | ScreenCaptureKit; file grows, must be explicit |
| Export markdown | transcript + metadata ready for Obsidian/client notes |

## Won't (deliberate)

- AI summarization / cloud integrations: "self-contained" is by choice
- GUI, menu bar app: QuickRecorder already does that. Exception implemented: indicator ● in menu bar while capture is active (NSStatusItem in tap, best-effort, no app)
- Auto-start on meeting detection: YAGNI, and surveillance smell
- Notarization / signed distribution: personal use, personal `brew tap` suffices

## Known risks

1. **TCC**: screen/audio recording permission goes to Ghostty, not `nastro`. One-time prompt, document this
2. **macOS < 14.4**: no process tap → out of scope (machine is on Tahoe)
3. **Sleep during call**: implicit `caffeinate` during `record`
4. **Disk full**: check space at start of recording, warning below 1GB
