## ADDED Requirements

### Requirement: Searches filter by time

`roca query` and `roca vector query` SHALL accept `--since` and `--until`, each a local
date (`YYYY-MM-DD`, `yesterday`, `today`) or a duration back from now (`<n>h`, `<n>d`),
and SHALL return only hits whose source session or memory falls inside the window. A hit
whose source row has no timestamp SHALL be kept and marked as undated.

#### Scenario: A date window keeps only sessions inside it

- **WHEN** a fixture corpus holds sessions on 2026-10-01 and 2026-10-20 that both mention
  `orchard`, and the operator runs `roca query "orchard" --since 2026-10-15`
- **THEN** only hits from the 2026-10-20 session are returned

#### Scenario: A duration window counts back from now

- **WHEN** a fixture corpus holds a session from two hours ago and one from three days
  ago, both mentioning `orchard`, and the operator runs `roca query "orchard" --since 24h`
- **THEN** only hits from the session of two hours ago are returned

#### Scenario: An undated hit is kept and marked

- **WHEN** a hit's source row has no timestamp and the operator filters with `--since`
- **THEN** the hit is returned and marked as undated

### Requirement: Searches filter by project

`roca query` and `roca vector query` SHALL accept `--project <name>` and SHALL return only
hits whose session (for exchange and thinking hits) or memory (for memory hits) belongs
to that project. A hit whose source row has no project SHALL be kept and marked.

#### Scenario: A project filter keeps only that project

- **WHEN** a fixture corpus holds sessions in projects `alpha` and `beta` that both
  mention `orchard`, and the operator runs `roca query "orchard" --project alpha`
- **THEN** only hits from the `alpha` session are returned

### Requirement: Search help names the filters

The help lines printed after a search SHALL mention `--since`, `--until` and `--project`.

#### Scenario: Help after a search names the filters

- **WHEN** the operator runs `roca query "orchard"`
- **THEN** a help line names `--since`, `--until` and `--project`
