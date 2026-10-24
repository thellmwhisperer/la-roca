## Why

The corpus holds historical duplicate sessions: rows whose `session_id` differs only
in the last character, with the same `started_at` and the same title. Nothing
reports them, so an operator who searches gets the same conversation two or three
times and does not know why.

The current binary does not create new ones. Re-ingesting fixtures by four paths
(same file, a copy at another path, a second project folder, a changed fingerprint)
added no session and no sibling id. Every parser takes the id from its source
(`pkg/parsers/*`, `internal/ingest/*`) and the writer upserts by exact `session_id`
(`internal/ingest/write.go:188`). So the part of issue #545 that asked ingest to skip
a sibling has no proof and is not in this change.

Counting by the first 35 characters alone gives false positives: the
`anthropic-export` fixture holds two different conversations whose ids share 35
characters, with different start dates. A duplicate needs the same stem, the same
`started_at` and the same title.

Who asked: issue #545. Scope: no deletion of corpus rows, because the copies differ in
text; only a `roca doctor` warning with the count. Deleting is a separate ticket.

Proof: on the real corpus, `roca exec` over `plugin_roca_corpus.sessions` grouped by
`substr(session_id,1,35)`, `started_at` and title returns many groups with
more than one row, in under a second. In a sampled group the copies hold the same
exchanges with the same timestamps, and their text lengths differ.

## What Changes

- `roca doctor` adds one warning when the corpus holds duplicate sessions: how many
  extra rows, how many groups, and the SELECT that lists them, to paste into
  `roca exec`.
- No warning when there are none. No row is changed or removed.

## Capabilities

### New Capabilities

- `corpus-duplicates`: how `roca doctor` detects and reports historical duplicate
  sessions in the corpus.

### Modified Capabilities

None.

## Impact

- Code: `internal/provider/service/doctor.go` (`Doctor`, next to `bedrock`, appending
  to `report.Warnings`).
- Acceptance scenarios to write, in `features/distribution/cli.feature` or a new
  `features/ingest/duplicates.feature`, run by `make accept`: an ingested fixture with
  two sibling sessions (same stem, `started_at` and title) makes `roca doctor` warn
  with count 1; the `anthropic-export` fixture (shared stem, different start) makes it
  print no such warning.
- Not touched: ingest, the parsers and the writer; no cleanup command; no deletion or
  rewrite of any corpus row; `roca compact`; search ranking.
