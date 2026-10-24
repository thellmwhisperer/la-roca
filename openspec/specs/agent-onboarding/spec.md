# agent-onboarding Specification

## Purpose
An agent that has not loaded a La Roca skill learns the product from `roca --help`
and from the text the session hook injects. This capability says what those two
surfaces teach: the commands agents are told to use, how `roca exec` treats an
unqualified table, and which attached aliases and tables exist.

## Requirements

### Requirement: Help lists the commands agents are told to use

`roca --help` and `roca help` SHALL list `exec` and `vector`, each with its one-line
description, in the command list. A `roca vector` call that the vector companion
does not take SHALL fail with a message that says how to turn vector search on:
set `vector = true` under `[features]` in `config.toml`, then run
`roca vector install`.

#### Scenario: Help shows exec and vector

- **WHEN** the operator runs `roca --help`
- **THEN** the command list has an `exec` line and a `vector` line, each with a
  description

#### Scenario: A vector call the companion does not take names how to turn it on

- **WHEN** vector search is off and the operator runs a `roca vector` call that is
  not a lifecycle command
- **THEN** the command fails with a message naming `vector = true` under
  `[features]` and `roca vector install`

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
database alias with its table names, built from the databases `roca exec` attaches
(so a plugin that exec does not attach is never named), without column lists.

#### Scenario: The alias line matches the attached databases

- **WHEN** a home has the bundled plugins installed and the operator runs
  `roca hooks run session --runtime codex`
- **THEN** the output carries one alias line that names `plugin_roca_corpus` with
  `sessions` and `exchanges` among its tables and `plugin_roca_ops` with `memories`, and
  names no alias that is not attached to `roca exec`

#### Scenario: A disabled optional plugin is not named

- **WHEN** a home has the optional `first-receipts` plugin installed and
  `plugins = false` under `[features]`, and the operator runs
  `roca hooks run session --runtime codex`
- **THEN** the alias line names no alias of `first-receipts` and still names
  `plugin_roca_corpus` and `plugin_roca_ops`

#### Scenario: No database, no alias line

- **WHEN** the hook cannot open a database and the operator runs
  `roca hooks run session --runtime codex`
- **THEN** the output still carries the fixed craft text and no alias line
