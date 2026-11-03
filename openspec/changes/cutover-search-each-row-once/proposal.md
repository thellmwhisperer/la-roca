## Why

After `roca migrate` and `serving = "cutover"` under `[layout]`, every
`roca query` fails. `roca vector query` and `roca exec` still answer, so the
home looks half broken.

The full-text branch of the hybrid search joins each match back to its row with
`JOIN <source> AS s ON s.rowid = f.fila` (`ftsBranch`,
`internal/provider/service/hybrid.go`). Under cutover the core search surfaces
`memories`, `sessions`, `exchanges` and `thinking_blocks` are TEMP VIEWs that
`installHubCompatibility` (`internal/provider/service/hub.go`) creates over the
plugin tables. A SQLite view has no implicit `rowid`, so the statement fails at
prepare time. The `*_fts` compatibility views already expose `rowid`; only the
source side lacks it.

Once the views expose `rowid`, every exchange and thinking block comes back
twice. `collectSurfaces` searches the core surfaces and then every plugin
surface. Under cutover the core `sessions`, `exchanges` and `thinking_blocks`
views read the corpus tables themselves, and the corpus plugin declares the same
tables as its own surfaces. The same physical row is read by two legs under two
labels, so duplicate collapsing never merges them and the copies take top-k
slots. Memories do not duplicate: the core `memories` view shows only rows whose
`source_database` is `core`, and the corpus leg serves the rest.

Who asked: proposal.

Proof: on v1.94.0 (81721cf), in a disposable `HOME` with one Claude Code session
and one memory file ingested by `roca init --db-path ~/.roca/roca.db`, then
`roca migrate` (`migration: verified`) and `serving = "cutover"` under
`[layout]` in `~/.roca/config.toml`:

```
$ roca query "pepino"
error: run the validated query: SQL logic error: no such column: s.rowid (1)
exit=1
```

The same home with a build whose four views select `rowid` (`id AS rowid` for
`memories`, `exchanges` and `thinking_blocks`, the base `rowid` for `sessions`):

```
$ roca query "ponytail"
rows[6]{rank,source,legs,fts_rank,snippet}:
  1,core.thinking_blocks.1,fts,1,"The brief calls for the ponytail-review skill…"
  2,"core+plugin:roca-ops+plugin:roca-corpus.thinking_blocks.1",fts,2,"The brief calls for the ponytail-review skill…"
  3,core.exchanges.2,fts,3,"Base directory for this skill: …"
  4,"core+plugin:roca-ops+plugin:roca-corpus.exchanges.2",fts,4,"Base directory for this skill: …"
  5,core.exchanges.1,fts,5,"Read `AGENTS.md` at the clone root …"
  6,"core+plugin:roca-ops+plugin:roca-corpus.exchanges.1",fts,6,"Read `AGENTS.md` at the clone root …"
```

Three rows, six hits.

## What Changes

- The four core compatibility views expose `rowid`, with the same integer their
  `*_fts` view exposes as `rowid`: `id` for `memories`, `exchanges` and
  `thinking_blocks`; the base table `rowid` for `sessions`, which has a text key.
- Under cutover, the search plan leaves out the core `sessions`, `exchanges` and
  `thinking_blocks` surfaces, because the corpus surfaces already serve those
  rows. The core `memories` surface stays, because it serves rows that no plugin
  surface serves.

## Capabilities

### New Capabilities

- `cutover-search`: what `roca query` returns on a home that serves from the
  federated plugins (`serving = "cutover"`).

### Modified Capabilities

None.

## Impact

- Code: `installHubCompatibility` in `internal/provider/service/hub.go` (the
  four view definitions); `Search` or `collectSurfaces` in
  `internal/provider/service/hybrid.go` (skip the core surfaces the corpus
  already serves when the service runs on the hub).
- Acceptance scenarios to write, run by `make accept` against the real `roca`:
  the two scenarios of `specs/cutover-search/spec.md`, in
  `features/store/search.feature` or a new `features/store/cutover-search.feature`,
  on a home made by `roca init --db-path`, an ingest of a fixture Claude Code
  session whose exchange and thinking block mention `orchard`, one
  `roca store` memory that mentions `orchard`, `roca migrate`, and
  `serving = "cutover"`.
- Not touched: the `source_database = 'core'` filter of the core `memories` and
  `memories_fts` views; the `ops.memories` table; `roca exec`; `roca vector
  query`; the legacy-serving layout; the database label a hit carries; the
  default serving mode of a new install.
