# Stack

## Decisione

| Componente | Scelta | Ruolo |
|---|---|---|
| TUI/CLI | **Go + Bubble Tea v2** | binario `nastro`, tutta l'interfaccia e l'orchestrazione |
| Componenti UI | Bubbles v2 (list, spinner, timer, help, key) | schermate records/record |
| Stile | Lip Gloss | tema (palette Catppuccin, coerente col resto del setup) |
| Form opzioni | huh | opzioni di `record` in TUI |
| Cattura audio | **Swift + CoreAudio process tap** (binario `nastro-tap`) | audio di sistema + mic, macOS 14.4+ |
| Trascrizione | **whisper.cpp** (`whisper-cli`, subprocess) | locale, modelli ggml, `-l it` |
| Video (fase 2) | ScreenCaptureKit (estensione di `nastro-tap`) | opzionale |

## Perché Bubble Tea

- **Elm Architecture** (Model → Update → View): FP puro, side effect confinati nei `tea.Cmd`. Coerente con la regola personale "a parità di maturità scegli l'opzione FP".
- Maturità massima della rosa: 44K⭐, push quotidiani, ecosistema charmbracelet (bubbles/lipgloss/huh) che copre esattamente le schermate previste (verificato su context7: list con delegate custom, spinner/timer, exec di subprocess con `tea.ExecProcess`).
- Binario statico singolo: distribuzione brew banale, gira pulito in un pane herdr.
- Go: apprendibile in giornata venendo da TS; il codice TUI è quasi tutto pattern dichiarativi.

## Alternative valutate (2026-08-06, stelle e push verificati)

| Framework | Stato | Verdetto |
|---|---|---|
| ratatui (Rust) | 22.1K⭐, push oggi | Il più robusto, ma paga il toolchain Rust assente sulla macchina e curva più ripida. Secondo classificato |
| opentui (TS) | 12.9K⭐, push oggi, 214 issue aperte | Il linguaggio di casa (TS), ma giovane e API instabile. Da rivalutare tra un anno |
| ink (React/TS) | 39.6K⭐, attivo | Ottimo per output interattivi, debole per app full-screen |
| textual (Python) | 36.9K⭐, push lug 2026 | Maturo e documentatissimo, ma Python non è nel flusso quotidiano |

Sentiment community (run /last30days 2026-08-06): nessun confronto diretto fresco nella finestra; chi costruisce librerie TUI cita "ratatui e bubbles" come riferimenti da studiare (r/tui). Reddit parziale per rate-limit: segnale debole, decisione presa su fattori strutturali.

## Architettura (design, non implementazione)

```
nastro (Go, TUI+CLI)
  ├─ exec → nastro-tap (Swift)     # cattura; progress su stdout (JSON lines)
  │           └─ scrive .m4a/.wav incrementale in ~/Recordings/nastro/<slug>/
  └─ exec → whisper-cli            # trascrizione; .txt/.srt accanto all'audio
```

- **Filesystem come source of truth**: una directory per registrazione, niente database. `records` = scan della dir.
- Config: `~/.config/nastro/config.toml` (dir output, modello whisper, lingua, formato).
- Vincolo TCC: il permesso di registrazione va al terminale ospite (Ghostty), prompt una tantum. Documentare nel README; accettabile per uso personale, è il limite noto per la distribuzione.
