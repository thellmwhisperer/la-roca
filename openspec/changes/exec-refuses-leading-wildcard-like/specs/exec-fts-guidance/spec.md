## ADDED Requirements

### Requirement: A leading-wildcard LIKE on a column with an FTS twin is refused with the MATCH statement

`roca exec` SHALL refuse, before running it, a statement with a `LIKE` whose pattern
starts with `%` on a text column of a table that has an FTS twin, unless `--timeout-ms`
is set explicitly above the default bound. The refusal SHALL exit non-zero, name the
table and column, and print a complete `roca exec` statement that runs the same term as
a `MATCH` on the FTS twin and joins back to the table's rows.

#### Scenario: The scan is refused and the FTS statement is printed

- **WHEN** the operator runs
  `roca exec "SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE '%orchard%'"`
  on a corpus holding exchanges that mention `orchard`
- **THEN** the command exits non-zero without running the scan, names
  `plugin_roca_corpus.exchanges.human_text`, and prints a `roca exec` statement that uses
  `exchanges_fts MATCH 'orchard'`

#### Scenario: The printed statement answers

- **WHEN** the operator runs the `roca exec` statement printed by that refusal
- **THEN** it exits 0 and returns the exchanges that mention `orchard`

#### Scenario: An explicit longer timeout lets the scan run

- **WHEN** the operator runs the same `LIKE '%orchard%'` statement with
  `--timeout-ms 60000`
- **THEN** the statement runs and exits 0 with the matching rows

### Requirement: Other LIKE statements keep today's behaviour

`roca exec` SHALL run, as today, a `LIKE` on a table without an FTS twin and a `LIKE`
whose pattern does not start with `%`.

#### Scenario: A table without an FTS twin is not refused

- **WHEN** the operator runs a `LIKE '%x%'` statement on a text column of a table that has
  no FTS twin
- **THEN** the statement runs as today and is not refused

#### Scenario: A prefix pattern is not refused

- **WHEN** the operator runs
  `roca exec "SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE 'orchard%'"`
- **THEN** the statement runs as today and is not refused
