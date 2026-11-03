# hybrid-fusion Specification

## Purpose
`roca query` fuses the hits of its full-text leg and its vector leg. This
capability says both legs name a plugin row the same way, so a row found by
both comes back once, with consensus.

## Requirements

### Requirement: Both legs label a plugin row the same way

A `roca query` hit for a row of a plugin database SHALL carry that database's
search name (the name `--databases` accepts, for example `corpus`), whether the
full-text leg or the vector leg found it.

#### Scenario: A row found by both legs fuses into one hit

- **WHEN** a home was made by `roca init --db-path ~/.roca/roca.db`, it ingested an
  agent session whose exchange mentions `harbor lantern`, `vector = true`
  is set under `[features]`, a `roca-vector` on `PATH` answers `query` with that
  exchange as its only hit, and the operator runs
  `roca query "harbor lantern" --databases corpus --json`
- **THEN** the command exits 0 and exactly one hit has that exchange's id, with
  `"database": "corpus"`, `legs` containing both `fts` and `vector`, and
  `"consensus": true`

### Requirement: Dual-confirmed rows survive the both-legs filter

`roca query --require-both` SHALL return a plugin row that both legs found.

#### Scenario: The both-legs filter keeps the fused row

- **WHEN** on that same home the operator runs
  `roca query "harbor lantern" --databases corpus --require-both --json`
- **THEN** the command exits 0 and its hits include that exchange with
  `"consensus": true`
