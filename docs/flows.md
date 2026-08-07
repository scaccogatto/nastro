# Flussi e schermate

## Comandi

```
nastro                      # TUI (dashboard → records)
nastro record [opzioni]     # registra, anche headless
nastro records [--plain]    # lista (--plain per script/pipe)
nastro transcribe <id|last> # trascrivi
```

Ogni flusso TUI ha l'equivalente CLI headless: la TUI è una vista, non l'unico ingresso.

## Stati di una registrazione

```
idle ──record──▶ recording ──stop/Ctrl+C──▶ saved ──transcribe──▶ transcribed
                     │
                 crash/kill ──▶ saved (file valido: scrittura incrementale)
```

## Schermata: record (durante la call)

```
┌ nastro ─────────────────────────────┐
│  ● REC  00:14:32                    │
│  sistema ▮▮▮▮▮▮▯▯  mic ▮▮▮▯▯▯▯▯     │
│                                     │
│  2026-08-06-1430-cliente-eppi       │
│  ~/Recordings/nastro/…  128 MB      │
│                                     │
│  s stop & save · x discard ·        │
│  ctrl+c stop & save                 │
└─────────────────────────────────────┘
```

`[p]ausa` non è implementata (backlog, non nella TUI reale).

Opzioni di `record`: `--name <slug>` (default: timestamp, unico campo esposto anche dal form della TUI), `--mic-only` / `--system-only` (solo CLI per ora, nessun equivalente TUI). `--split` (tracce separate) e `--video` (fase 2) sono aspirazionali: non ancora implementate né in CLI né in TUI.

## Schermata: records

```
┌ nastro · registrazioni ─────────────────────────┐
│ ▸ 2026-08-06-1430  cliente-eppi   42m  128MB  ✓ │
│   2026-08-05-1000  standup        12m   38MB  - │
│   2026-08-04-1500  presales-baxi  55m  170MB  ✓ │
│                                                 │
│ r rec · enter dettaglio · t transcribe ·        │
│ o finder · d elimina · / filtra · q/ctrl+c esci │
└─────────────────────────────────────────────────┘
```

Mentre il filtro (`/`) è attivo, ogni tasto (incluso `q`) va all'input del
filtro: `enter` applica, `esc` annulla. Le scorciatoie tornano attive solo a
filtro applicato o chiuso.

`--plain`: una riga per record, tab-separated → componibile con fzf/zoxide/script.

## Flusso: transcribe

1. `nastro transcribe last` (o `t` dalla lista)
2. Modello assente → download ggml con progress e size stimata (una tantum), annullabile
3. Conversione audio (afconvert): spinner "preparing audio…", annullabile
4. whisper-cli in subprocess: spinner "transcribing…", progress in TUI (o stdout se headless), annullabile
5. Output: `transcript.txt` + `transcript.srt` nella dir del record
6. Notifica macOS a fine job (should)

Default da config: `model = "large-v3-turbo"`, `lang = "it"`.

## Primo avvio (onboarding minimo)

1. `nastro record` → macOS chiede il permesso (a Ghostty): messaggio chiaro su cosa approvare
2. Nessun wizard: config generata coi default al primo run, si edita il TOML

## Edge case decisi

| Caso | Comportamento |
|---|---|
| Ctrl+C / q / s durante record | Stop pulito = salva. `x` esplicito per scartare (con conferma y/n) |
| Sleep del Mac | `caffeinate` per la durata della registrazione |
| Due `record` contemporanei | Rifiutato con errore chiaro (lockfile) |
| Disco < 1GB | Warning prima di partire |
| Transcribe di un record già trascritto | Chiede conferma overwrite, nominando il record |
| Ctrl+C / esc durante transcribe | Annulla il job (whisper-cli/afconvert), pulisce l'output parziale, torna alla schermata di provenienza |
| Record vuoto (0 byte / <2s) | Non salvato, dir rimossa |

## Integrazione herdr (documentazione, non feature)

Keybind suggerito in `~/.config/herdr/config.toml`: popup `nastro record` su `alt+r`, pane `nastro` per la lista. Da aggiungere ai dotfiles quando esisterà il binario.
