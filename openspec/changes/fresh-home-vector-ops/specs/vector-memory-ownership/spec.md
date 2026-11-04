## ADDED Requirements

### Requirement: A home that does not serve ops declares no ops sidecar

In a home served in `legacy-serving`, with `features.roca_ops` false or absent
and no DATA SPLIT writer fence, the vector registry SHALL declare no
`roca-ops` database. The first `roca vector install` pass on that home SHALL
read only declared sources the query route can read, and SHALL finish without
`roca migrate`.

#### Scenario: The first vector pass on a new home without roca_ops

- **WHEN** `roca` and `roca-vector` were installed by `install.sh` into
  `~/.local/bin`, `~/.roca/config.toml` held only `[features]` `vector = true`
  before `roca init`, init ingested an agent session that carries a memory
  mentioning `harbor lantern`, the operator ran `roca store --layer discovery
  --content "orchard compass note"`, and `roca vector install` finished, with
  no `roca migrate`
- **THEN** `roca vector install` exited 0, the last `vector worker:` line of
  `~/.roca/plugins/roca-vector/state/worker.log` says `exit 0`, that log holds
  no `is not a table this query can read`, `roca vector status --json` lists
  no `roca-ops` database and no database whose state is `empty` or
  `outdated`, and `roca doctor` prints no `roca vector install`

#### Scenario: A vector command on a home that does not serve ops

- **WHEN** a home was made by `roca init` with no configuration before it,
  `vector = true` and `roca_ops = false` are then written under `[features]`
  in `~/.roca/config.toml`, and the operator runs `roca vector status`
- **THEN** the command exits 0 and `~/.roca/plugins/vector-registry.json`
  declares no `roca-ops` database

### Requirement: A memory stored on that home is found by full-text search

On a home that does not serve ops, a memory `roca store` wrote lives in core.
`roca query` SHALL return it from core, and `roca vector query` without
`--databases` SHALL exit 0 and say that core has no vector declaration and
continues with FTS-only.

#### Scenario: Finding the stored memory before the migration

- **WHEN** on the home of "The first vector pass on a new home without
  roca_ops", the operator runs `roca query "orchard compass" --json` and then
  `roca vector query "orchard compass" 5`
- **THEN** the first command exits 0 with a row whose source starts with
  `core.memories` and whose text holds `orchard compass note`, and the second
  exits 0, prints `database core has no vector declaration; continuing with
  FTS-only`, and prints no `unknown database`
