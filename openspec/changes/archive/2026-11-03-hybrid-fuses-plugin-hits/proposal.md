## Why

`roca query` runs a full-text leg and a vector leg and fuses them with
reciprocal rank fusion. A row found by both legs should come back once, with
`legs` `[fts, vector]` and `consensus: true`, and `--require-both` keeps only
those rows. For any row a plugin database serves, the two legs never agree, so
`roca query` shows the same row twice and `--require-both` drops every plugin
row.

The two legs label the same database differently. The search plan names a
plugin leg by its database name (`scopeName` in
`internal/provider/service/scope.go`), for example `corpus`, the same name
`--databases` takes and the vector companion reports. After the full-text
statement runs, `EnsureDatabaseColumn` (`internal/provider/service/plugins.go`)
rewrites that provenance to the source label of the attached databases:
`plugin:roca-corpus` when only the corpus is searched, and
`core+plugin:roca-ops+plugin:roca-corpus` when all are. Duplicate collapsing and
fusion key on database, table and id (`internal/search/hybrid.go`), so
`corpus.thinking_blocks.1` and `plugin:roca-corpus.thinking_blocks.1` never meet.

The search label this change keeps is `corpus`: it is what `--databases`
accepts, what the search plan already assigns, what the help lines print, and
what every vector hit carries. `roca exec` keeps its own `plugin:roca-corpus`
provenance.

Who asked: proposal.

Proof: on v1.94.0 (81721cf), in a disposable `HOME` with one agent session
ingested by `roca init --db-path ~/.roca/roca.db`, `vector = true` and a
finished `roca vector install` (`roca vector status` shows `corpus` `complete`):

```
$ roca query "ponytail review skill"
search hybrid · engines fts,vector
rows[4]{rank,source,legs,vector_score,vector_rank,fts_rank,snippet}:
  1,"core+plugin:roca-ops+plugin:roca-corpus.thinking_blocks.1",fts,null,null,1,"The brief calls for the ponytail-review skill, so I should invoke that."
  2,corpus.thinking_blocks.1,vector,0.533623,1,null,"The brief calls for the ponytail-review skill, so I should invoke that."
  ...
$ roca query "ponytail review skill" --require-both
no matches in memory for that search
```

On a larger home, `roca query "check out the ponytail skill first, as
instructed" --databases corpus --json` returns full-text hits with
`"database": "plugin:roca-corpus"` and vector hits with `"database": "corpus"`,
every one with `"consensus": false`.

## What Changes

- The full-text leg of `roca query` keeps the database label the search plan
  gave its surface, so a plugin row carries the same label from both legs:
  `corpus` for the corpus plugin, `ops` for the ops plugin.
- A row found by both legs comes back once, with `legs` `[fts, vector]` and
  `consensus: true`, and `--require-both` returns it.

## Capabilities

### New Capabilities

- `hybrid-fusion`: how `roca query` labels and fuses the hits its two legs find
  for the same plugin row.

### Modified Capabilities

None.

## Impact

- Code: the full-text branch of `internal/provider/service/hybrid.go`
  (`ftsBranch`, `searchFTS`) and its use of `EnsureDatabaseColumn` in
  `internal/provider/service/plugins.go`, so a search hit keeps its surface
  label; the unit test that expects `plugin:roca-corpus` on a search hit
  (`internal/provider/service/resident_test.go`) changes to `corpus`.
- Acceptance scenarios to write, run by `make accept` against the real `roca`:
  the two scenarios of `specs/hybrid-fusion/spec.md`, with a stub `roca-vector`
  on `PATH` that answers `query` with one fixed hit for the fixture exchange, so
  no test loads the embedding model.
- Not touched: `roca exec` provenance labels; the vector companion and its
  output; the RRF formula and `rrf_k`; the core surfaces of a legacy home; the
  cutover layout.
