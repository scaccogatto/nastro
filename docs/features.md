# Features (auto-workshop 2026-08-06)

Contesto d'uso: call Google Meet in italiano con clienti, da Ghostty/herdr, tutto locale, trascrizione in autonomia. Un solo utente (per ora).

## Must (v0.1)

| Feature | Note |
|---|---|
| Cattura audio sistema + mic, mixati | via `nastro-tap`; macOS 14.4+ |
| `nastro record` headless | funziona anche senza TUI: parte, mostra timer, Ctrl+C ferma e salva |
| Scrittura incrementale | il file è valido anche dopo crash/kill: mai perdere una call |
| Output ordinato | `~/Recordings/nastro/<YYYY-MM-DD-HHmm>-<slug>/audio.m4a` |
| `nastro records` | lista: data, slug, durata, size, stato transcript (✓/-) |
| `nastro transcribe <id\|last>` | whisperx (default) con speaker diarization, `-l it` di default, output `.txt` + `.srt` con prefisso `[SPEAKER_NN]` accanto all'audio; richiede un token HuggingFace (`hf_token` in config o `$HF_TOKEN`). `transcriber = "whisper-cli"` in config torna al backend senza diarization |
| Config TOML | `~/.config/nastro/config.toml`: output dir, modello, lingua, backend (`transcriber`), `hf_token` |
| Timer + livelli in TUI | feedback che sta davvero registrando (VU meter basico) |

## Should (v0.2)

| Feature | Note |
|---|---|
| Tracce separate mic/sistema | due file: abilita diarization "povera" (io vs loro) senza ML |
| Hook post-recording | comando arbitrario da config (es. transcribe automatico) |
| Notifica macOS a fine transcribe | le trascrizioni lunghe girano in background |
| `records`: azioni sulla lista | invio=dettaglio, `t`=transcribe, `o`=apri in Finder, `d`=elimina (con conferma) |
| Download modello whisper al primo uso | `nastro transcribe` scarica il ggml se assente, con progress |

## Could (fase 2+)

| Feature | Note |
|---|---|
| Video opzionale (`record --video`) | ScreenCaptureKit; il file cresce, va reso esplicito |
| Export markdown | transcript + metadati pronti per Obsidian/note cliente |

## Won't (deliberato)

- Summarization AI / integrazioni cloud: la conversione è "in autonomia" per scelta
- GUI, menu bar app: esiste già QuickRecorder per quello. Eccezione implementata: indicatore ● in menu bar mentre la cattura è attiva (NSStatusItem nel tap, best-effort, nessuna app)
- Auto-start su rilevamento meeting: YAGNI, e odore di sorveglianza
- Notarizzazione/distribuzione firmata: uso personale, `brew tap` personale basta

## Rischi noti

1. **TCC**: il permesso registrazione schermo/audio va a Ghostty, non a `nastro`. Prompt una tantum, da documentare
2. **macOS < 14.4**: niente process tap → fuori scope (la macchina è su Tahoe)
3. **Sleep durante la call**: `caffeinate` implicito durante `record`
4. **Disco pieno**: check spazio all'avvio della registrazione, warning sotto 1GB
