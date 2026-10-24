## ADDED Requirements

### Requirement: A missing vector state folder is created, not reported as moved

When a vector command finds the companion's state folder absent while it holds
the shared relocation lock, and no relocation moved that state elsewhere, it SHALL
create the folder and continue. It SHALL NOT fail with "vector state moved" for a
folder that never existed.

#### Scenario: The first vector query after turning vector search on answers

- **WHEN** a home was initialized by `roca init --json --db-path ~/.roca/roca.db`
  with vector search off, so `~/.roca/plugins/roca-vector` does not exist, the
  operator sets `vector = true` under `[features]` in `~/.roca/config.toml`, and
  runs `roca vector query "what did we decide" 5`
- **THEN** the command exits 0, prints a `notice:` line for `corpus` and for `ops`
  saying the database has no ready vector sidecar and continues with FTS-only, and
  does not print `vector state moved`

#### Scenario: The second vector query answers the same way

- **WHEN** the operator runs `roca vector query "what did we decide" 5` again on
  that home
- **THEN** the command exits 0 with the same notices and does not print
  `rerun the command`

#### Scenario: The advised install starts the background pass

- **WHEN** on that same kind of home, before any vector query, the operator runs
  `roca vector install`
- **THEN** the command exits 0, says that setup continues in the background, and
  `~/.roca/plugins/roca-vector/state` exists afterwards
