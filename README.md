# nastro

Registratore di call minimale per macOS, in TUI. Audio di sistema + mic, tutto locale, trascrizione con whisper.cpp quando la vuoi tu.

**Stato: design.** Nessuna riga di codice ancora: qui dentro ci sono le decisioni.

## Perché

Le app per registrare le call o sono bot in cloud, o sono GUI. Voglio `rec` in un pane del terminale, un file locale, e la mia pipeline di trascrizione. Niente cloud, niente bot in call.

## Design

- [docs/naming.md](docs/naming.md) - perché "nastro", verifica collisioni
- [docs/stack.md](docs/stack.md) - Go + Bubble Tea v2, helper Swift (CoreAudio tap), whisper.cpp; alternative valutate
- [docs/features.md](docs/features.md) - MoSCoW v0.1/v0.2, rischi
- [docs/flows.md](docs/flows.md) - comandi, schermate, stati, edge case

## Il succo

```
nastro                      # TUI
nastro record               # registra (headless ok), Ctrl+C = salva
nastro records              # lista registrazioni
nastro transcribe last      # whisper.cpp, italiano di default
```

Requisiti: macOS 14.4+ (CoreAudio process tap). Il permesso di registrazione viene chiesto al terminale ospite.
