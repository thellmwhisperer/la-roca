## ADDED Requirements

### Requirement: Hybrid search answers on a cutover home

On a home that serves from the federated plugins (`serving = "cutover"` under
`[layout]`), `roca query` SHALL run its full-text leg without a SQL error and
SHALL return the matching memories, exchanges and thinking blocks.

#### Scenario: A word in an exchange, a thinking block and a memory is found

- **WHEN** a home was made by `roca init --db-path ~/.roca/roca.db`, it ingested
  a Claude Code session whose exchange and thinking block mention `orchard`, the
  operator stored a memory with `roca store --layer discovery --content "orchard
  note"`, ran `roca migrate`, set `serving = "cutover"` under `[layout]` in
  `~/.roca/config.toml`, and runs `roca query "orchard" --json`
- **THEN** the command exits 0, prints no `no such column` text, and its hits
  include one exchange, one thinking block and one memory whose snippets mention
  `orchard`

### Requirement: Each row appears once on a cutover home

On a cutover home, `roca query` SHALL NOT return the same row twice, whether it
reaches that row through a core surface or through a plugin surface.

#### Scenario: No row is repeated

- **WHEN** on that same home the operator runs `roca query "orchard" --json`
- **THEN** no two hits share the same `table` and `id`, and the number of hits
  equals the number of distinct rows that mention `orchard`
