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
│  s/q/ctrl+c stop & save ·           │
│  x discard                          │
└─────────────────────────────────────┘
```

`[p]ausa` non è implementata (backlog, non nella TUI reale).

Opzioni di `record`: `--name <slug>` (default: timestamp), `--mic-only` / `--system-only`. Il form della TUI (schermata prima di avviare) espone entrambi i campi:

```
recording name, enter for timestamp

▌
mode: mixed ▸ (tab to change)
→ ~/Recordings/nastro

enter confirm · tab mode · esc cancel
```

`tab` cicla la modalità di cattura `mixed → mic-only → system-only → mixed`; la riga sotto mostra dove finirà il file. `--split` (tracce separate) e `--video` (fase 2) sono aspirazionali: non ancora implementate né in CLI né in TUI.

## Schermata: records

```
┌ nastro · registrazioni ──────────────────────────────────┐
│ ▸ 2026-08-06-1430  cliente-eppi   42m  128MB  ✓          │
│   2026-08-05-1000  standup        12m   38MB  -          │
│   2026-08-04-1500  presales-baxi  55m  170MB  ✓          │
│                                                           │
│ r rec · enter dettaglio · t transcribe · o finder ·      │
│ d elimina · / filtra · ? help · q/ctrl+c esci            │
└───────────────────────────────────────────────────────────┘
```

Mentre il filtro (`/`) è attivo, ogni tasto (incluso `q`) va all'input del
filtro: `enter` applica, `esc` annulla. Le scorciatoie tornano attive solo a
filtro applicato o chiuso.

`?` apre un overlay di help con tutti i keybinding per schermata, i path utili
(output dir, config, modello whisper) e la garanzia "recordings survive
crashes: audio is written incrementally" -- qualsiasi tasto lo chiude.

Con molti record la paginazione è numerica (`2/125`, `paginator.Arabic`) invece
dei puntini.

`--plain`: una riga per record, tab-separated → componibile con fzf/zoxide/script.

### Cancellazione (`d`)

`d` chiede conferma nominando il record, poi sposta la sua directory in
`~/.Trash` (nome deduplicato con un suffisso timestamp in caso di collisione):
recuperabile da lì come qualunque altro file cestinato. Status dopo la
cancellazione: `moved to Trash: <id>`. Se lo spostamento fallisce (Trash non
disponibile/non scrivibile), fallback esplicito a cancellazione permanente,
con status che lo dichiara (`trash unavailable, deleted permanently: <id>`).

## Flusso: transcribe

1. `nastro transcribe last` (o `t` dalla lista)
2. Modello assente → download ggml con progress e size stimata (una tantum), annullabile
3. Conversione audio (afconvert): spinner "preparing audio…", annullabile
4. whisper-cli in subprocess (con `--print-progress`): barra di progresso + percentuale in TUI una volta arrivata la prima riga di progresso, spinner come fallback finché non arriva (o su build di whisper-cli senza la flag); stessa progressione visibile su stdout in headless. Annullabile.
5. Output: `transcript.txt` + `transcript.srt` nella dir del record
6. Notifica macOS a fine job (should)

Default da config: `model = "large-v3-turbo"`, `lang = "it"`.

Un fallimento (afconvert o whisper-cli) non espone mai l'errore grezzo
("afconvert: exit status 1" nudo): viene tradotto in un messaggio azionabile
(causa più probabile, suggerimento di ridurre il modello se il fallimento
sembra di memoria, altrimenti il dettaglio catturato), sia in TUI che da CLI.

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
| `d` (elimina) | Sposta in `~/.Trash` (recuperabile); fallback a cancellazione permanente solo se lo spostamento fallisce, dichiarato nello status |

## Integrazione herdr (documentazione, non feature)

Keybind suggerito in `~/.config/herdr/config.toml`: popup `nastro record` su `alt+r`, pane `nastro` per la lista. Da aggiungere ai dotfiles quando esisterà il binario.
