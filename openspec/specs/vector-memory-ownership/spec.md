# vector-memory-ownership Specification

## Purpose
After `roca migrate` and `serving = "cutover"`, memories live in the ops
`memory_records` table, tagged with the database they came from. This
capability says which vector sidecar embeds each of them, so every memory is
embedded once, by its owner, and a sidecar with nothing of its own to embed
reports itself finished.

## Requirements

### Requirement: Each memory is embedded once, by its owner

In a home served in cutover, every active memory SHALL be embedded by exactly
one vector sidecar: a memory whose migration source is the corpus by the
corpus sidecar, and a memory whose source is `core` or `plugin:roca-ops`, or
that was written after the cutover, by the ops sidecar.

#### Scenario: A core memory and a corpus memory after the migration

- **WHEN** `roca` and `roca-vector` were installed by `install.sh` into
  `~/.local/bin`, a home was made by `roca init` with `vector = true`, it
  ingested an agent session that carries a memory mentioning `harbor lantern`,
  the operator ran `roca store --layer discovery --content "orchard compass
  note"` before the migration, then `roca migrate` is verified, `serving =
  "cutover"` is set under `[layout]`, and `roca vector install` finished
- **THEN** `roca vector status --json` shows the ops sidecar with embedded
  chunks for the `orchard compass note` memory and a state other than
  `outdated`, `roca vector query "orchard compass note" --databases ops --json`
  returns that memory from ops, and `roca vector query "harbor lantern"
  --databases corpus,ops --json` returns the corpus memory once, from corpus
  only

### Requirement: A sidecar with nothing to embed is finished

A sidecar whose declared tables hold no rows to embed SHALL report a state
other than `outdated` after `roca vector install`, and `roca doctor` SHALL NOT
offer `roca vector install` as its remedy.

#### Scenario: An ops sidecar with no memories of its own

- **WHEN** on a home made the same way but without the `roca store` step, so
  every memory came from the corpus, `roca vector install` finished
- **THEN** `roca vector status --json` shows the ops sidecar with 0 embedded
  chunks and a state other than `outdated`, and `roca doctor` prints no
  `roca vector install`
