# corpus-duplicates Specification

## Purpose
Older ingests left some corpus sessions twice, with ids that differ only in the
last character. This capability says how `roca doctor` reports them to the
operator without changing any corpus row.

## Requirements

### Requirement: Doctor reports historical duplicate sessions

`roca doctor` SHALL count the corpus sessions whose 36-character `session_id` shares
its first 35 characters, its `started_at` and its title with another session, and
SHALL print one warning with the number of extra rows, the number of groups, and a
SELECT the operator can paste into `roca exec` to list them. It SHALL print no such
warning when there are none, and SHALL NOT change any corpus row.

#### Scenario: Two copies of one session raise the warning

- **WHEN** the corpus holds two sessions whose ids differ only in the last character,
  with the same `started_at` and the same title, and the operator runs `roca doctor`
- **THEN** the output carries one duplicate-sessions warning that says 1 extra row in
  1 group and includes the SELECT that lists them

#### Scenario: A shared stem with a different start is not a duplicate

- **WHEN** the corpus holds only the sessions of the `anthropic-export` fixture, two of
  which share 35 id characters but have different `started_at`, and the operator runs
  `roca doctor`
- **THEN** the output carries no duplicate-sessions warning

#### Scenario: Doctor changes nothing

- **WHEN** the operator runs `roca doctor` on a corpus with duplicate sessions
- **THEN** the session and exchange counts in `plugin_roca_corpus` are the same before
  and after
