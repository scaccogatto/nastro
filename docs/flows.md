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

Backend selezionato da config (`transcriber`): **whisperx** (default, con
speaker diarization) o **whisper-cli** (nessuna diarization, fallback).

1. `nastro transcribe last` (o `t` dalla lista)
2. Controllo prerequisiti del backend configurato. nastro è plug-and-play
   sulle dipendenze: i binari (`whisperx`, `whisper-cli`, `uv`, `brew`) non
   vengono cercati solo sul PATH del processo (spesso incompleto per un
   terminale grafico) ma anche in `~/.local/bin` e nei bin di Homebrew
   (`/opt/homebrew/bin`, `/usr/local/bin`).
   - **whisperx**: binario `whisperx` non trovato ma `uv` sì → schermata di
     conferma ("whisperx is not installed. Install it now with uv? (~2 GB,
     a few minutes) [y/n]" in TUI, `[y/N]` da stdin in CLI); su conferma,
     `uv tool install whisperx` in streaming (spinner + ultime righe),
     annullabile (kill del processo; nessun cleanup esplicito serve, uv
     registra lo shim del tool solo a install riuscita). A successo,
     prosegue automaticamente con la ri-verifica dei prerequisiti e la
     trascrizione. Se manca anche `uv` → messaggio guidato ("brew install
     uv", poi "uv tool install whisperx"). Poi token HuggingFace (`hf_token`
     in config, o `$HF_TOKEN` se il campo è vuoto) altrimenti messaggio
     guidato con link per crearne uno, accettare i termini del modello di
     diarization gated, e dove incollarlo nel TOML -- l'unico passo che resta
     manuale (access-grant, non installabile)
   - **whisper-cli**: binario `whisper-cli` non trovato ma `brew` sì →
     stessa conferma/streaming con `brew install whisper-cpp`; se manca
     anche `brew`, messaggio guidato attuale. Poi modello ggml assente →
     download con progress e size stimata (una tantum), annullabile
     (whisperx gestisce i propri modelli da sé, nessun download esplicito
     in nastro)
   Le installazioni partono solo su conferma esplicita dell'utente, mai in
   automatico.
3. Conversione audio (afconvert): spinner "preparing audio…", annullabile
4. Backend in subprocess:
   - **whisper-cli** (con `--print-progress`): barra di progresso +
     percentuale in TUI una volta arrivata la prima riga di progresso,
     spinner come fallback finché non arriva (o su build senza la flag)
   - **whisperx**: nessuna percentuale affidabile; spinner + ultime righe
     di output (stessa UI, niente barra di progresso)
   Stessa progressione visibile su stdout in headless. Annullabile.
5. Output: `transcript.txt` + `transcript.srt` nella dir del record. Con
   whisperx, ogni riga/blocco è prefissato dallo speaker rilevato (es.
   `[SPEAKER_00] testo...`), raggruppando segmenti consecutivi dello stesso
   speaker; con whisper-cli, nessun prefisso (nessuna diarization)
6. Notifica macOS a fine job (should)

Default da config: `transcriber = "whisperx"`, `model = "large-v3-turbo"`,
`lang = "it"`.

Un fallimento (afconvert o il backend di trascrizione) non espone mai
l'errore grezzo ("afconvert: exit status 1" nudo): viene tradotto in un
messaggio azionabile (causa più probabile, suggerimento di ridurre il
modello se il fallimento sembra di memoria, altrimenti il dettaglio
catturato), sia in TUI che da CLI.

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
| Ctrl+C / esc durante transcribe | Annulla il job (backend/afconvert), pulisce l'output parziale, torna alla schermata di provenienza |
| Crash/kill del processo di trascrizione (non richiesto da nastro) | Stesso comportamento di un annullamento esplicito: il transcript preesistente non viene mai toccato prima che il nuovo sia completo (whisperx scrive solo un JSON intermedio in una dir temporanea, mai vicino a `transcript.txt/.srt`; la promozione è atomica e avviene solo a fine parsing riuscito) |
| Record vuoto (0 byte / <2s) | Non salvato, dir rimossa |
| `d` (elimina) | Sposta in `~/.Trash` (recuperabile); fallback a cancellazione permanente solo se lo spostamento fallisce, dichiarato nello status |

## Integrazione herdr (documentazione, non feature)

Keybind suggerito in `~/.config/herdr/config.toml`: popup `nastro record` su `alt+r`, pane `nastro` per la lista. Da aggiungere ai dotfiles quando esisterà il binario.
