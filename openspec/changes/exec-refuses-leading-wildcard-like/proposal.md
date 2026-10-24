## Why

An agent that wants every exchange mentioning a term writes
`WHERE human_text LIKE '%term%'`. A leading wildcard on a text column is a full scan; on
a real corpus it exceeds the 5 s bound of `roca exec` and the agent gets a timeout
instead of an answer. The fast path exists (the table's FTS twin with `MATCH`, in
milliseconds) but nothing in the binary points to it when the scan fails, and the rule
lives in a skill that session start does not force anyone to load.

Who asked: issue #549.

Proof: on the real corpus on 24-oct-2026,
`roca exec "SELECT count(*) FROM plugin_roca_corpus.exchanges WHERE human_text LIKE '%sweep%'"`
exits 1 with "the validated SQL exceeded the time limit after 5s", while
`roca exec "SELECT count(*) FROM (SELECT rowid FROM plugin_roca_corpus.exchanges_fts WHERE exchanges_fts MATCH 'sweep')"`
answers in 25 ms.

## What Changes

- `roca exec` recognises a `LIKE` whose pattern starts with `%` against a text column of a
  table that has an FTS twin, refuses the statement before running it, and prints the
  equivalent `roca exec` statement on the FTS twin, ready to paste.
- Tables without an FTS twin, patterns without a leading `%`, and runs with an explicit
  `--timeout-ms` above the default keep today's behaviour.

## Capabilities

### New Capabilities

- `exec-fts-guidance`: how `roca exec` steers a leading-wildcard `LIKE` to the FTS twin.

### Modified Capabilities

None.

## Impact

- Code: the `roca exec` gate in `internal/provider/query/sqlgate` (or wherever the gate
  inspects the parsed statement), using the existing table-to-FTS-twin mapping of the
  schema (`exchanges_fts`, `thinking_fts`, `sessions_fts`, `memories_fts`,
  `memory_records_fts`).
- Acceptance scenarios to write, run by `make accept` against the real `roca` on a fixture
  corpus: refusal with the printed statement; the printed statement runs and returns the
  matching rows; no twin, no leading `%` and explicit `--timeout-ms` run as today.
- Not touched: the 5 s default bound, the unqualified-table refusal, `roca query`, the FTS
  schema and its triggers.
