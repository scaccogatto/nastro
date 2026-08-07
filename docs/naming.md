# Naming

## Scelto: `nastro`

Italiano per "tape". Corto, tipabile, evocativo, e dentro la cultura dei terminal-recorder (charmbracelet/vhs registra "tape files": nastro è il vhs italiano).

## Verifica collisioni (2026-08-06)

| Registry | Esito |
|---|---|
| GitHub | Nessun progetto "nastro"; solo repo cechi non correlati ("nastroje" = strumenti in ceco) |
| crates.io | Libero |
| npm | 404 (libero) |
| Homebrew | Nessuna formula/cask |

## Candidati scartati

| Nome | Motivo |
|---|---|
| bobina | Runner-up valido, meno immediato |
| fono | Collide con fonoster (8K⭐, dominio voice: troppo vicino) |
| traccia | traccia-ai esiste, npm occupato |
| presa | npm occupato, vicino a presage |
| incidi | Libero ma poco leggibile fuori dall'italiano |
| vhs / tape / deck / rec | Occupati o troppo generici |

## Binari

- `nastro` - CLI/TUI principale (Go)
- `nastro-tap` - helper di cattura (Swift). "tap" = CoreAudio process tap, nome descrittivo
