# CodeRabbit whole-repo pass, batch 1: store, ingest, scripts

14 findings (3 major), verified against `main` at `1542f3d`, which is the commit
the pass itself used as its base. 12 were still valid and are fixed, 1 is a false
positive from the review's own directory scope, and 1 needs a decision because its
remedy is a new subsystem rather than a fix.

`make check` is green end to end and the slop gate holds at 22/22 active clones.

The branch is rebased onto `main` after it advanced by two commits mid-round,
both of them prune items from the closing review being acted on:
`d7a65f1 refactor(search): remove model-free service entry point` (the
caller-less `service.Search`) and `9822cfe refactor(config): remove root-level
fallback` (the legacy config lookup). The first collided with the dedup ruling,
since the removed `Service.Search` was one of the call sites the ruling
retargets; `main`'s deletion won, and the seam test that arrived with it
(`TestSearchRowsWithIdenticalSourceAndTextAreDeduplicated`, which asserted
deduplication through `found`) now asks `foundRanked` for it, because that is
where the ruling puts the presentation.

## Recovering the findings

The handoff file `rabbit-findings-store-ingest.md` arrived as a skeleton: 14
headers carrying a severity and a file name, every body empty and every line
number the literal `?`. Rather than guess at 14 findings and attribute the guesses
to the rabbit, the real ones were recovered from the CLI's own review cache under
`~/.coderabbit/reviews/`, where each finding is a JSON document with `fileName`,
`startLine`, `severity`, `title` and `comment`. Two cached runs matched the
skeleton exactly, 7 findings each, same base commit, from
`--dir internal/store` and `--dir internal/ingest`. Every citation below is that
recovered text, not a reconstruction.

Worth knowing for the next batch: the bodies live in that cache, so a lost
handoff is recoverable as long as nobody clears it.

## Findings

| # | File | Sev | Finding | State |
|---|---|---|---|---|
| F1 | `store/adopt.go` | minor | `Inspect`'s comment claimed a database "identical column by column" is adopted, but `compare` walks only the EXPECTED columns and never rejects extras. | fixed: comment now states the rule it applies (all required columns match; an extra table is still an orphan) |
| F2 | `store/real_db_test.go` | minor | The failure message said "a live existing database is adopted" for a test that only calls `Inspect` and adopts nothing. | fixed: says "should be adoptable" and names both accepted verdicts |
| F3 | `store/search/engine.go` | minor | An unsupported non-empty `Method` fell through to the FTS branch while `Provenance.Method` kept reporting the unsupported string, so the answer described a route the engine had not taken. | fixed red-first: refused by name |
| F4 | `store/search/engine.go` | **major** | "Execute the LIKE fallback before returning": the `MethodLike` branch returns an empty `Result` without querying. | **skipped, false positive**: the sole production caller, `service.searchByTerm`, renders the LIKE, puts it through the gate and executes it itself; the empty `Result` is the signal to do so. The pass ran `--dir internal/store` and could not see the caller. |
| F5 | `store/search/engine_test.go` | minor | The diacritic-folding fixture stored `Muller` unaccented, so `TestLexicalIndexSearchFoldsDiacritics` passed whether or not folding worked. | fixed: fixture now stores `naïve Müller façade`, and the test still passes, which is now evidence |
| F6 | `store/search/match.go` | minor | `Tokenize` documents itself as "exactly what `unicode61 remove_diacritics 2` does" but cut on `unicode.IsDigit`, which is the decimal digits alone. A superscript (`No`) or private-use rune was a separator here while the index kept it inside the token. | fixed red-first: token characters are now letters, ALL number categories and `Co`; `Tokenize` had no tests at all and now has them |
| F7 | `store/search/index.go` | **major** | Accurate: `readState` checks only the terminal `built` state, so two concurrent builders each rebuild all four FTS tables, and a failure on a later table re-does the ones that completed. | **needs-decision** `index-build-lease` |
| F8 | `ingest/ingest_test.go` | minor | `os.Chmod(path, 0)` does not bind a privileged process, so under a root CI the read succeeds and the test fails for a reason that is not the product's. | fixed, by a different route than proposed (see below) |
| F9 | `ingest/parsers/subagent_test.go` | minor | The nonzero-record check permitted wrong source positions. | fixed: asserts records 3 and 4 for the unanswered turns and record 1 for the unprompted answer |
| F10 | `ingest/parsers/codex_test.go` | minor | Discard counts were asserted without the metadata, so a discard on the wrong record or with no reason passed. | fixed: asserts record 2 for the superseded question and record 3 for the empty reasoning, each with a reason |
| F11 | `ingest/parsers/codex_test.go` | minor | The per-block emptiness loop passed a parser that stored whitespace or synthesized text. | fixed: asserts no thinking block is stored at all |
| F12 | `ingest/parsers/pi_test.go` | minor | Same weakness on the Pi side, plus the discard position unasserted. | fixed: asserts zero thinking blocks and record 3 with a reason |
| F13 | `ingest/parsers/codex_test.go` | minor | The comment opened by naming Pi for a Codex test, and nothing asserted that a deferred turn is not ALSO discarded. | fixed: comment corrected and `Discards` asserted empty, so deferred and discarded stay exclusive |
| F14 | `ingest/parsers/metadata.go` | **major** | When `meta.SourceAgent` is the legacy `claude-cowork`, only `entrypoint` was normalized; `session.SourceAgent` kept the alias, which is not on the supported roster. | fixed red-first: the alias normalizes to `cowork` and stays the entrypoint value only |

### F8, fixed by a different route than proposed

The proposal was to call `read` with a `SidecarPath` pointing at a directory. That
cannot work through the scan: `filesUnder` admits an entry only when
`entry.Type().IsRegular()`, so a `cw.json` that is a directory (or a symlink) is
dropped from the walk, the audit target that carries it as a sidecar is never
built, and the test would stop exercising the pairing it exists for.

The real failure mode is a spurious FAILURE under a privileged process, not a
false pass. So the test now skips with that reason when `os.Geteuid() == 0` and
keeps its end-to-end coverage everywhere it can actually run.

### F7 needs a decision: `index-build-lease`

The facts hold. `buildLexicalIndex` reads only `built` and writes it only after all
four rebuilds, so two processes that call `Index` with the state absent both
rebuild everything, and a failure on the third table re-does the first two. The
`exchanges_fts` rebuild is the expensive one.

It is not a correctness defect today: an FTS `'rebuild'` is idempotent, so the
duplicated work converges on the same index, and `Index` has one entry point
(`service.Index`, reached by `roca init` and `roca index`). The cost is wasted
work and a write lock held longer than necessary.

The proposed remedy is a build lease plus per-table completion state plus
stale-lease recovery after a terminated builder. That is a new concurrency
subsystem and a new guarantee about concurrent indexing, so it is not something
this branch invents. Two halves worth separating when the decision is made:

- **Per-table progress** is cheap and arguably just makes the existing state
  honest: record each table as it completes and resume from the first unfinished
  one. It needs new `search_state` keys and nothing else.
- **Single-builder serialization** is the part that is genuinely new: a lease has
  to be taken, honoured, and recovered when its holder dies, and getting staleness
  wrong turns a wasteful-but-correct path into a wedged one.

## Second ruling, applied in this branch: `dedup-reorders-model-sql`

Resolved as implementation cleanup. The objective: a query is the question going
to the model, the model's SQL going to the database, and ITS rows coming back
faithfully. So on the model-SQL paths the answer now returns exactly what the
statement produced, with no deduplication, no reordering and no silent dropping;
the literal rescue keeps its ranking and deduplication as the presentation that
makes a term search readable. It derives from the v1 model-only decision: no
silent filters, and an honest zero.

`QueryResult.found` is now the plain recorder and `foundRanked` carries the
presentation, so faithful is what a call site gets by omission rather than by
remembering. Call sites: the model path (`llm.go` `llmStage`) and the two
zero-row declarations record verbatim; the rescue (`llm.go` `rescue`) and the
direct literal search (`query.go` `Search`) record ranked.

Pinned red-first on both sides:

- `TestTheModelPathReturnsTheRowsItsSQLProduced` drives a real model-path query
  whose SQL cross-joins the three seeded memories, so the statement produces nine
  rows of which `LIMIT` keeps eight, every id repeated and ordered by id. Before
  the change it reported 3 rows for SQL that produced 8.
- `TestFoundRecordsTheRowsExactlyAsGiven` pins duplicates, order and null text
  surviving the verbatim recorder; `TestRankedRowsPreferAnswersOverEchoesAndThinking`,
  `TestRankedRowsWithNullTextAreRemoved` and
  `TestRankedRowsWithIdenticalSourceAndTextAreDeduplicated` pin the rescue's
  ranking and deduplication at the seam that owns them; and
  `TestNoRowsIsDeclaredEmptyOnEitherRecorder` pins the honest zero on both.

With `service.Search` gone, the ranked recorder now has exactly one caller: the
literal rescue. That is the whole of the presentation path.

One detail worth recording, because the ruling's parenthetical differs from the
code: the literal rescue DOES set `res.SQL` (to the gated LIKE or FTS statement it
ran). The behaviour is therefore keyed on the path (`PathLLM` against
`PathKeyword`) rather than on whether SQL is present, which is what the ruling's
objective asks for. If the intent was instead that the rescue should stop
publishing its SQL, that is a separate change and is not made here.

## Commits

```
b0ea03c fix(query): return the rows the model's SQL produced
90ba46f fix(ingest): normalize the legacy cowork alias to its roster name
2e9c750 fix(search): refuse a method the engine cannot run
fbe5800 fix(search): tokenize on unicode61's own token characters
b9f891b test(ingest): pin the discard positions and the blocks not stored
bf2dfee docs(store): state the adoption rule compare actually applies
```

Every behavioural fix landed red first. One open decision: `index-build-lease`.
