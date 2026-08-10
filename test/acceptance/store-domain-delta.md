# Store domain — capability delta vs. the approved scenarios

The 16 approved store-domain scenarios are the contract this wave ships. This
file lists what the store-domain CODE can do that NO approved scenario claims,
so the owner can prune or commission coverage deliberately. It is an inventory,
not a wish list: nothing here is broken, and nothing here is deleted.

Each row is `capability -> where it lives -> claimed by no scenario`.

## Bootstrap and schema

- Classify a database by structure and report its verdict (current /
  migratable / incompatible / foreign) as a user-facing command.
  -> `roca schema status` / `internal/store/adopt.go` `Inspect`, `internal/provider/service/service.go` `SchemaStatus`.
  -> No scenario runs `roca schema status` directly or asserts a verdict other
     than "current" (scenarios 4 and 5 reach the migratable->repair path through
     `roca init` and only check the end state).
- Report orphan tables (tables outside v1) without blocking adoption.
  -> `internal/store/adopt.go` `Report.Orphans`, narration in `internal/distribution/cli/commands.go`.
  -> No scenario seeds an orphan table and asserts it is reported and left intact.
- Structural adoption that ignores the text of the DDL (same columns, rewritten
  create statements).
  -> `internal/store/adopt.go` `Inspect`/`compare`.
  -> The consecrated suite claims this (D-4 / D-4b); no store-domain scenario does.
- `roca index` as a standalone command (rebuild the search index by hand).
  -> `internal/distribution/cli/search.go` `indexCommand`, `internal/store/search/index.go`.
  -> No scenario runs it (init builds the index implicitly).
- `roca exec` (run a caller-supplied SELECT under the read-only gate).
  -> `internal/distribution/cli/commands.go` `execCommand`, `internal/provider/service/query.go` `Exec`.
  -> No scenario.
- `roca health` (non-destructive checks over live data, e.g. `orphan_supersedes`).
  -> `internal/distribution/cli/memory.go` `healthCommand`, `internal/provider/service/health.go`.
  -> No scenario.

## Writing memories

- Deduplication: identical content in the same layer/status/project among
  non-superseding memories is recognized as already stored (`Skipped`).
  -> `internal/provider/service/store.go` `Store`.
  -> No scenario stores the same memory twice and asserts it is skipped.
- Layer aliases resolved at write time (`handover`->`handoff`, `protocol`->`pattern`).
  -> `internal/provider/service/store.go` `Store` via the layer registry.
  -> No scenario writes through an alias and asserts the physical layer.
- Store flags beyond layer/origin/project/content: `--status` (active/pending/
  resolved), `--source-agent`, `--metadata` (JSON), and the `--supersedes`
  write-surface audit (`SurfaceCLI`/`SurfaceMCP`).
  -> `internal/distribution/cli/memory.go` `storeCommand`, `internal/provider/service/store.go` `StoreRequest`/`encodeMetadata`.
  -> No scenario (origin and project only are claimed, in scenario 6).
- Read-only mode refuses a store with a reason.
  -> `internal/store`/`internal/provider/service` `ROCA_READ_ONLY` / `refuseReadOnly`.
  -> No scenario.

## Search

- Search provenance and degradation: the FTS index falls to the LIKE floor with
  a declared reason when there is no index, and the method travels in the answer.
  -> `internal/store/search/engine.go` `Search`/`Provenance`, `internal/provider/service/search.go`.
  -> No scenario exercises the LIKE floor or asserts `search_method`.
- Search restricted to one layer (`--layer`).
  -> `internal/distribution/cli/commands.go` `queryCommand`, `internal/provider/query/fts.go`.
  -> No scenario (all search scenarios are cross-layer over memories).
- Per-field truncation budget (`--max-chars`).
  -> `internal/provider/service/query.go` `truncate`.
  -> No scenario.
- Search across the other indexed sources (exchanges, thinking blocks, sessions)
  and the source-priority ordering between them.
  -> `internal/provider/query/fts.go` `renderFTS`, `internal/provider/service/query.go` `dedupRows`/`relevanceRank`.
  -> Scenario 13 ranks within memories only; no scenario seeds or searches a
     non-memory source.
- A direct search entry point with no model in the path (`service.Search`), used
  by the golden bench.
  -> `internal/provider/service/search.go` `Service.Search`.
  -> No scenario (the suite reaches FTS only through the keyword rescue).

## Backup and adoption by copy

- Adoption by copy: copy an external database into the home path non-
  interactively / interactively, original untouched.
  -> `internal/store/backup.go` `CopyDatabase`, `internal/distribution/cli/commands.go` `selectInitDatabase`.
  -> No scenario drives the interactive `adopt` flow or asserts the source is
     untouched (scenarios 4 and 5 use `--db-path` over an in-place database).
- Backup guards: a same-second backup already existing is refused; the copy is
  verified by integrity check and per-table row counts.
  -> `internal/store/backup.go` `Backup`/`verifyBackup`.
  -> Scenario 5 asserts the restore; the "already exists" refusal is not claimed.

## Resilience

- Concurrency beyond two writers and the read-during-write snapshot guarantee.
  -> `internal/store/store.go` `Write`/`Open` (`_txlock=immediate`, `busy_timeout`).
  -> Scenario 15 uses exactly two writers.

## Finding worth a decision (not a capability gap)

- Scenario 9 ("A memory can supersede another; the old one stops answering"):
  the binary excludes from search the memory that CARRIES the `supersedes`
  pointer (the replacement), and keeps the original answering. The scenario is
  green asserting that current behaviour; its frozen title reads the opposite.
  This is a product-semantics question for the owner, recorded here and in the
  task status, not a coverage gap to fill.
