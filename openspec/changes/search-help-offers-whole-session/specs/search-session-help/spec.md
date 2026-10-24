## ADDED Requirements

### Requirement: Search help offers the whole session of a hit

After `roca vector query` or `roca query` returns at least one exchange or thinking-block
hit, the help lines SHALL include one complete `roca exec` statement, with
`--max-chars 2000`, that returns every exchange of that hit's session ordered by
exchange number, using the hit's own database alias. When no hit is an exchange or a
thinking block, no such line SHALL be printed.

#### Scenario: An exchange hit offers its session

- **WHEN** a fixture corpus holds a session of three exchanges, one of which mentions
  `orchard`, and the operator runs `roca query "orchard"`
- **THEN** the help lines include a `roca exec` statement that reads
  `plugin_roca_corpus.exchanges` by that hit's `session_id`, ordered by `exchange_number`,
  with `--max-chars 2000`

#### Scenario: The printed statement returns the session in order

- **WHEN** the operator runs that printed `roca exec` statement verbatim
- **THEN** it exits 0 and returns the three exchanges of the session in exchange-number
  order

#### Scenario: A thinking hit offers its session

- **WHEN** the only hit of a search is a thinking block of a fixture session
- **THEN** the help line's inner lookup reads `thinking_blocks` to find the session and
  the statement returns that session's exchanges in order

#### Scenario: Memory hits offer no session line

- **WHEN** every hit of a search is a memory
- **THEN** no whole-session help line is printed
