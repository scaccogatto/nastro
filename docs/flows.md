# Flows and Screens

## Commands

```
nastro                      # TUI (dashboard → records)
nastro record [options]     # record, also headless
nastro records [--plain]    # list (--plain for script/pipe)
nastro transcribe <id|last> # transcribe
```

Every TUI flow has a headless CLI equivalent: TUI is a view, not the only entry point.

## Recording states

```
idle ──record──▶ recording ──stop/Ctrl+C──▶ saved ──transcribe──▶ transcribed
                     │
                 crash/kill ──▶ saved (file valid: incremental write)
```

## Screen: record (during call)

```
┌ nastro ─────────────────────────────┐
│  ● REC  00:14:32                    │
│  system ▮▮▮▮▮▮▯▯  mic ▮▮▮▯▯▯▯▯     │
│                                     │
│  2026-08-06-1430-client-eppi        │
│  ~/Recordings/nastro/…  128 MB      │
│                                     │
│  s/q/ctrl+c stop & save ·           │
│  x discard                          │
└─────────────────────────────────────┘
```

`[p]ause` is not implemented (backlog, not in real TUI).

`record` options: `--name <slug>` (default: timestamp), `--mic-only` / `--system-only`. TUI form (screen before starting) exposes both fields:

```
recording name, enter for timestamp

▌
mode: mixed ▸ (tab to change)
→ ~/Recordings/nastro

enter confirm · tab mode · esc cancel
```

`tab` cycles capture mode `mixed → mic-only → system-only → mixed`; line below shows file destination. `--split` (separate tracks) and `--video` (phase 2) are aspirational: not yet implemented in CLI or TUI.

## Screen: records

```
┌ nastro · recordings ──────────────────────────────────┐
│ transcribing 2 · queued 1                             │
│                                                       │
│ ▸ 2026-08-06-1430  client-eppi     42m  128MB   45%   │
│   2026-08-05-1000  standup         12m   38MB  queued │
│   2026-08-04-1500  presales-baxi   55m  170MB    ✓    │
│                                                       │
│ r rec · enter detail · t transcribe · o finder ·     │
│ d delete · / filter · ? help · q/ctrl+c quit         │
└───────────────────────────────────────────────────────┘
```

While filter (`/`) is active, every key (including `q`) goes to filter input: `enter` applies, `esc` cancels. Shortcuts become active again when filter is applied or closed.

`?` opens help overlay with all keybindings for screen, useful paths (output dir, config, whisper model) and guarantee "recordings survive crashes: audio is written incrementally" — any key closes it.

With many records, pagination is numeric (`2/125`, `paginator.Arabic`) instead of dots.

`--plain`: one line per record, tab-separated → composable with fzf/zoxide/script.

The last column is a fixed-width (6 char) live status, replacing the plain transcript checkmark whenever a record has a background transcribe job: `-` (no transcript, no job), `✓` (transcribed), `queued`, `NN%` (whisper-cli, once `--print-progress` reports in), or an animated spinner (percent unknown — the common case for whisperx, and whisper-cli before its first progress line). A status strip above the list summarizes active jobs ("transcribing 2 · queued 1"), gone once there are none; a job's completion briefly shows "transcribed &lt;id&gt;" there instead, then a rescan refreshes the row's ✓.

### Deletion (`d`)

`d` asks confirmation naming the record, then moves its directory to `~/.Trash` (deduplicated with timestamp suffix if collision): recoverable like any trashed file. Status after deletion: `moved to Trash: <id>`. If move fails (Trash unavailable/not writable), explicit fallback to permanent delete, with status declaring it (`trash unavailable, deleted permanently: <id>`).

## Flow: transcribe

Backend selected from config (`transcriber`): **whisper-cli** (default, whisper.cpp on Metal, no diarization) or **whisperx** (opt-in, speaker diarization).

1. `nastro transcribe last` (or `t` from list)
2. Check prerequisites of configured backend. nastro is plug-and-play on dependencies: binaries (`whisperx`, `whisper-cli`, `uv`, `brew`) are searched not just on process PATH (often incomplete for a graphical terminal) but also in `~/.local/bin` and Homebrew bins (`/opt/homebrew/bin`, `/usr/local/bin`).
   - **whisperx**: `whisperx` binary not found but `uv` present → confirmation screen ("whisperx is not installed. Install it now with uv? (~2 GB, a few minutes) [y/n]" in TUI, `[y/N]` from stdin in CLI); on confirmation, `uv tool install whisperx` streaming (spinner + tail), cancellable (kill process; no explicit cleanup needed, uv registers tool shim only on successful install). On success, auto-continues with prerequisite re-check and transcription. If `uv` also missing → guided message ("brew install uv", then "uv tool install whisperx"). Then HuggingFace token (`hf_token` in config, or `$HF_TOKEN` if field empty) otherwise guided message with link to create one, accept gated diarization model terms, and where to paste in TOML — only step that stays manual (access grant, not installable)
   - **whisper-cli**: `whisper-cli` binary not found but `brew` present → same confirmation/streaming with `brew install whisper-cpp`; if `brew` also missing, current guided message. Then ggml model absent → download with progress and estimated size (once), cancellable (whisperx manages own models, no explicit download in nastro)
   Installations start only on explicit user confirmation, never automatic. This prereq/install/download step is still a single, modal flow (one at a time) — the concurrency below only applies once a job is past it and actually running.
3. Job admitted into the background registry (TUI only; the CLI stays synchronous, one job — see below) and its screen shown: "preparing audio…" (afconvert), or "queued…" if `max_parallel_transcriptions` is already saturated
4. Backend in subprocess, once a slot is free:
   - **whisper-cli** (with `--print-progress`): progress bar + percentage in TUI once first progress line arrives, spinner as fallback until then (or on build without flag)
   - **whisperx**: no reliable overall percentage; spinner + tail of output, honest phase text when a known stage marker is recognized ("aligning…", "diarizing…") instead of a fabricated number
   Same progression visible on stdout in headless. The full combined stdout/stderr of every run is also appended to `<record dir>/transcribe.log` (crash-safe, not part of the transcript, not scanned/listed); the on-screen tail is filtered (blank lines, Python warnings, internal traceback frames, repeated third-party progress bars dropped) but a failure's error message still gets the same captured detail as before — filtering is display-only.
5. Output: `transcript.txt` + `transcript.srt` in record directory. With whisperx, each line/block prefixed by detected speaker (e.g. `[SPEAKER_00] text...`), grouping consecutive segments of same speaker; with whisper-cli, no prefix (no diarization)
6. macOS notification at job end (should)

Config defaults: `transcriber = "whisper-cli"` (whisperx recommended opt-in for speaker labels), `model = "large-v3-turbo"`, `lang = "it"`, `max_parallel_transcriptions = 2`.

A failure (afconvert or transcription backend) never exposes raw error ("afconvert: exit status 1" bare): translated to actionable message (likely cause, suggestion to reduce model if failure looks like memory, otherwise captured detail), both in TUI and CLI.

### Background jobs, navigation, and cancellation (TUI)

Once a job is running (past the prereq/install/download step), it's a background job, not a blocking screen:

- **Multiple, concurrent**: up to `max_parallel_transcriptions` (default 2, floored at 1) run at once, keyed by record ID. Beyond the cap, a new job queues (FIFO) and starts automatically once a slot frees — on the next completion or cancellation, regardless of arrival order.
- **Free navigation**: `esc` (or `ctrl+c`) on a job's own screen goes back to the list *without* canceling it — the job keeps running in the background. Reach a job's screen again with `t` on its record (from the list or detail) or by opening the detail of a record currently being processed; neither duplicates the job.
- **Cancel is deliberate**: only "c" on the job's own screen, with a "cancel transcription of &lt;slug&gt;? [y/n]" confirmation. A still-queued job is torn down immediately (nothing running yet); a converting/running one has its subprocess killed, same partial-output cleanup as before, scoped to that job alone — the others are untouched.
- **Quitting with jobs running**: `q`/`ctrl+c` on the list asks "N transcriptions running, quit anyway? they will be canceled [y/n]" — jobs die with the app (every job's context is explicitly canceled before quitting, not left to process exit).
- **Visible everywhere**: the list's status column (see above) and status strip, and each job's own screen (name, phase, progress bar/percentage or spinner, elapsed time, last 2-3 filtered output lines).

## First run (minimal onboarding)

1. `nastro record` → macOS asks permission (to Ghostty): clear message about what to approve
2. No wizard: config generated with defaults on first run, edit TOML

## Decided edge cases

| Case | Behavior |
|---|---|
| Ctrl+C / q / s during record | Clean stop = save. `x` explicit to discard (confirmation y/n) |
| Mac sleep | `caffeinate` for duration of recording |
| Two concurrent `record` | Rejected with clear error (lockfile) |
| Disk < 1GB | Warning before start |
| Transcribe of already-transcribed record | Asks confirmation overwrite, naming record |
| Ctrl+C / esc on a job's own screen | Back to list, job keeps running in the background (not a cancel) |
| `c` on a job's own screen | Cancel that job only (confirmation y/n): backend/afconvert killed, clean partial output, back to source screen |
| Quit (`q`/ctrl+c on the list) with jobs running | Confirmation y/n naming the count; on yes, every job is explicitly canceled, then the app quits |
| Crash/kill of transcription process (not requested by nastro) | Same behavior as explicit cancel: existing transcript never touched before new is complete (whisperx writes only intermediate JSON in temp dir, never near `transcript.txt/.srt`; promotion is atomic and only on successful parse end) |
| Empty record (0 byte / <2s) | Not saved, dir removed |
| `d` (delete) | Moves to `~/.Trash` (recoverable); fallback to permanent delete only if move fails, declared in status |

## herdr integration (documentation, not feature)

Suggested keybind in `~/.config/herdr/config.toml`: popup `nastro record` on `alt+r`, pane `nastro` for list. Add to dotfiles when binary exists.
