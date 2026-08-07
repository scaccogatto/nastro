# Product

<!-- impeccable:product-schema 1 -->

## Platform

terminal (TUI) — fuori dai valori di schema web/ios/android/adaptive: nastro è un'app da terminale (Go/Bubble Tea) per macOS, non ha superficie web o nativa mobile.

## Users

Sviluppatori che vivono nel terminale e fanno call (Meet/Zoom/Teams) con clienti o team, e vogliono registrazione + trascrizione locali senza bot in call né cloud. Primo utente e design partner: l'autore (freelance, call in italiano con clienti, workflow herdr/Ghostty). Destinazione confermata: **open source pubblico** (GitHub pubblico, brew tap, doc curata per terzi).

## Product Purpose

Registrare le call (audio di sistema + microfono, opzionalmente video in futuro) da terminale, in file locali, e trascriverle on-device con whisper.cpp quando l'utente lo chiede. Successo: non perdere mai una call, non dipendere mai da un servizio esterno, trascrizione utilizzabile in italiano.

## Positioning

L'unico registratore di call TUI: niente bot che entra in call (cattura via CoreAudio process tap), niente cloud (file e trascrizione restano sulla macchina), interfaccia da terminale con equivalente headless per scripting. I vicini o sono GUI (QuickRecorder), o sono bot/cloud (Fireflies, tl;dv), o sono rulli continui (Screenpipe).

## Operating Context

- macOS 14.4+ (CoreAudio process tap); il permesso TCC di cattura va al terminale ospite, non al binario
- Gira tipicamente in un pane di herdr dentro Ghostty; i path nella TUI sono link OSC 8 cliccabili
- Filesystem come source of truth: una directory per registrazione in `~/Recordings/nastro/`, nessun database
- Trascrizione: whisper.cpp (`whisper-cli`) + modello ggml locale, default `large-v3-turbo`, lingua default `it`
- Config: `~/.config/nastro/config.toml`, default in memoria, mai scritta dal tool

## Capabilities and Constraints

- Comandi: `nastro` (TUI), `record`, `records --plain`, `transcribe <id|last>`; ogni flusso TUI ha l'equivalente CLI headless
- Architettura a due binari: `nastro` (Go) orchestran­te + `nastro-tap` (Swift) per la cattura; comunicazione via stdout (livelli `level sys=.. mic=..`) ed exit code (2 = permesso mancante)
- Garanzie: scrittura incrementale (file valido anche dopo crash), metadata periodici (crash perde ≤5s di durata), lockfile con staleness detection, caffeinate anti-sleep, record <2s scartati
- **Lingua UI: inglese, deciso e applicato** (2026-08-07). L'italiano resta solo come lingua di default della trascrizione
- Won't confermati: niente summarization AI/cloud, niente GUI (eccezione: indicatore ● NSStatusItem best-effort), niente auto-start su rilevamento meeting
- Video: fase 2 dichiarata, non implementata

## Brand Commitments

Nome `nastro` (italiano per "tape", verificato libero su GitHub/crates/npm/brew, docs/naming.md); binario helper `nastro-tap`. Nessun logo o identità visiva definiti.

## Evidence on Hand

- docs/: naming.md, stack.md, features.md (MoSCoW), flows.md (schermate, stati, edge case) — decisioni di design datate 2026-08-06/07
- Codebase funzionante con 66 test Go verdi (anche -race) ed e2e reali verificati (registrazione afinfo-valida, trascrizione italiana su Metal)
- Nessun utente terzo, testimonianza o benchmark: non fabbricarne

## Product Principles

1. **Mai perdere una call**: ogni scelta tecnica privilegia l'integrità del file registrato (scrittura incrementale, stop puliti, discard espliciti)
2. **Locale per scelta, non per limite**: niente cloud e niente bot sono posizionamento, non mancanze da colmare
3. **TUI come vista, CLI come contratto**: ogni cosa fattibile a schermo è fattibile headless e componibile in script
4. **La cattura è il job, il resto è cosmesi**: le feature accessorie (indicatore menu bar, VU meter) degradano in silenzio, mai a spese della registrazione
5. **Un utente vero prima di mille ipotetici**: le feature nascono dal workflow reale dell'autore, poi si generalizzano per l'open source
