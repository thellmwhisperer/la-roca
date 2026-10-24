## ADDED Requirements

### Requirement: Help lists the commands agents are told to use

`roca --help` and `roca help` SHALL list `exec` and `vector`, each with its one-line
description, in the command list.

#### Scenario: Help shows exec and vector

- **WHEN** the operator runs `roca --help`
- **THEN** the command list has an `exec` line and a `vector` line, each with a
  description

### Requirement: The session hook states how exec treats unqualified tables

The text `roca hooks run session` injects SHALL say that an unqualified table name is
refused with the qualified candidates, and SHALL NOT say that it returns 0 rows without a
warning.

#### Scenario: The stale sentence is gone

- **WHEN** the operator runs `roca hooks run session --runtime codex`
- **THEN** the output does not contain "returns 0 rows without a warning" and does say
  that an unqualified table is refused with the qualified candidates

### Requirement: The session hook names the attached aliases and their tables

The text `roca hooks run session` injects SHALL carry one line listing each attached
database alias with its table names, built from the same catalog the `roca-semantica`
skill is generated from, without column lists.

#### Scenario: The alias line matches the attached databases

- **WHEN** a home has the bundled plugins installed and the operator runs
  `roca hooks run session --runtime codex`
- **THEN** the output carries one alias line that names `plugin_roca_corpus` with
  `sessions` and `exchanges` among its tables and `plugin_roca_ops` with `memories`, and
  names no alias that is not attached

#### Scenario: No database, no alias line

- **WHEN** the hook cannot open a database and the operator runs
  `roca hooks run session --runtime codex`
- **THEN** the output still carries the fixed craft text and no alias line
