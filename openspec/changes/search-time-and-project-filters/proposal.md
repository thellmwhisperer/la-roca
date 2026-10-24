## Why

"What did we discuss last night about X?" is the most common question an agent asks La
Roca, and neither `roca query` nor `roca vector query` can express "last night" or "in
this project". Both search the whole history; the agent then filters hits by timestamp
or project with a hand-written SELECT.

Who asked: issue #546.

Proof: on 24-oct-2026, `roca query --help` lists `--databases`, `--max-chars`,
`--no-templates`, `--oversample`, `--require-both` and `--top`, and
`roca vector query --help` lists `--databases`, `--expand-templates`, `--min-score` and
`--template`; neither has a time or project filter.

## What Changes

- `roca query` and `roca vector query` gain `--since <date|duration>`,
  `--until <date|duration>` and `--project <name>`, applied after retrieval and before the
  result is printed.
- Dates are `YYYY-MM-DD` in local time, or `yesterday`, `today`; durations are `<n>h` and
  `<n>d` back from now.
- `--project` matches `sessions.project` for exchange and thinking hits and
  `memories.project` for memory hits.
- A hit whose source row has no timestamp or no project is kept and marked, not dropped,
  so coverage gaps stay visible.
- The help lines printed after a search mention the three flags.

## Capabilities

### New Capabilities

- `search-filters`: time and project filters on both search commands.

### Modified Capabilities

None.

## Impact

- Code: `roca query` in core (flag parsing and the post-retrieval filter) and
  `roca vector query` (the core resident path in
  `internal/distribution/cli/vector_query_resident.go` and the `roca-vector` plugin's
  `query` command), plus the shared help hint.
- Acceptance scenarios to write, run by `make accept` against the real `roca` on a fixture
  corpus with sessions on known dates and projects: a date filter, a duration filter, a
  project filter, a hit without a timestamp kept and marked, and the help line naming the
  flags.
- Not touched: ranking and fusion, the vector index and its build, `roca exec`, new
  database columns.
