# hybrid-vector-companion Specification

## Purpose
The vector leg of `roca query` runs the `roca-vector` companion. This capability
says hybrid search finds that companion where `roca vector` finds it, in the
managed install directory even when it is not on `PATH`.

## Requirements

### Requirement: Hybrid search finds the companion where roca vector finds it

`roca query` SHALL find `roca-vector` in the managed install directory
(`$ROCA_PREFIX`, else `~/.local/bin`) even when that directory is not on
`PATH`, and SHALL run its vector leg with it.

#### Scenario: The companion outside PATH still runs the vector leg

- **WHEN** a home was made by `roca init --db-path ~/.roca/roca.db`, it ingested an
  agent session whose exchange mentions `harbor lantern`, `vector = true`
  is set under `[features]`, a `roca-vector` that answers `query` with that
  exchange is in `~/.local/bin` only, `PATH` does not contain `~/.local/bin`, and
  the operator runs `roca query "harbor lantern" --json`
- **THEN** the command exits 0, its `engines` include `vector`, and its notices do
  not include `vector plugin is not installed`

### Requirement: A missing companion still degrades with a notice

When neither the managed install directory nor any `PATH` entry holds
`roca-vector`, `roca query` SHALL answer from full-text search with the
`vector plugin is not installed` notice, as today.

#### Scenario: No companion anywhere

- **WHEN** on a home made the same way, with `vector = true`, no `roca-vector`
  exists in `~/.local/bin` or on `PATH`, and the
  operator runs `roca query "harbor lantern" --json`
- **THEN** the command exits 0, its `engines` are only `fts`, and its notices
  include `vector plugin is not installed; continuing with FTS-only`
