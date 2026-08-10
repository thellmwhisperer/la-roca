# CodeRabbit whole-repo pass, batch 2

Base `2468208` (the OSS sweep seed), reviewed per domain with
`coderabbit review --committed --base-commit 2468208 --agent --dir <d>`.

Domain status:

| Domain | Files | Outcome |
|---|---|---|
| `internal/provider` | 58 | 8 findings, then the review **timed out server-side** at ~55 min: coverage is partial |
| `internal/distribution` | 73 | pending |
| `cmd` | 1 | pending |
| `test` | 40 | pending |
| `features` | 30 | pending |
| `docs` | 11 | **dropped by decision**: `docs/reviews` and the inventories leave the public tree and README/docs get rewritten, so reviewing them is wasted effort |

## The timeout, and what it means for coverage

The provider run was healthy throughout: 79 `heartbeat/reviewing` events, no rate
limit, 8 findings streamed out as it worked. It then ended on
`{"type":"error","errorType":"timeout","message":"Review timed out"}` rather than
a clean completion.

Two consequences worth carrying forward:

- **Findings stream incrementally, so a timeout still yields everything found
  before it fires.** All 8 provider findings arrived before the cut and are
  triaged below. Nothing was lost.
- **What the cut-off tail would have held is unknown**, so `internal/provider` is
  NOT fully reviewed. A follow-up pass should scope below the domain
  (`internal/provider/service`, `query`, `config`, `oauth`, and the package root
  separately) so each review fits the server's budget. `internal/distribution` is
  73 files, larger than the 58 that timed out, so expect the same there.

These runs are again on the free CLI allowance: the worktree has no git remote,
so the Pro seat cannot be attributed and quota is finite across domains.

## internal/provider: 7 fixed, 1 raised

| # | File | Sev | Finding | State |
|---|---|---|---|---|
| P1 | `service/ingest_test.go` | minor | The dry-run test counted exchanges but not memories, so a dry run that persisted a memory passed. | fixed: asserts zero memories too |
| P2 | `service/store.go` | **major** | `surfaceKey` is documented reserved, "a caller's own metadata never overwrites it", but the audit was written only when a surface was supplied. A caller passing `{"surface": "forged"}` on a path with none got it stored verbatim. | fixed red-first: the reserved key is deleted before the audit is applied |
| P3 | `service/ingest.go` | minor | The `s.Index` error path returned without setting `TotalElapsedMS`, which every other exit sets. | fixed (consistency; the sibling paths carry the coverage) |
| P4 | `query/text.go` | minor | `SearchTerm`'s "shorter than three letters" filter counts BYTES. | **needs-decision** `search-term-word-length` |
| P5 | `config/file.go` | minor | Table headers were matched by raw line text, so `[models."xai"]` and `[ models.xai ]` never matched the table being edited. | fixed red-first, and the severity is understated: see below |
| P6 | `query/fts.go` | **major** | `RenderSQLLike` formatted its SQL before building clauses, so a term carrying no word produced `WHERE  AND ...` in four branches of one statement. `RenderSQLFTS` already refused such terms. | fixed red-first: refused the same way |
| P7 | `oauth/login.go` | minor | A parent context cancellation fired `waitCtx.Done()` and was reported as "the login did not finish within 5m0s", a timeout that never happened. | fixed: reports the caller's cancellation |
| P8 | `oauth/login_test.go` | minor | The credential-leak assertion joined three substrings with AND, so it only fired when all three appeared, and its two-letter fixtures (`at`, `rt`) matched ordinary prose. | fixed: distinctive sentinels, each checked on its own |

### P5 is worse than minor

The rabbit filed it minor. What it actually does: `roca model set xai <id>` over a
configuration written `[models."xai"]` (valid TOML, the same table) fails to match
the header, appends a SECOND `[models.xai]`, and leaves the operator's own file
unparseable:

```
the configuration at ~/.roca/config.toml is not valid TOML:
toml: line 5 (last key "models"): Key 'models.xai' has already been defined.
```

Every later command that loads the configuration then fails. The fix normalizes a
header to its canonical dotted key (brackets, surrounding whitespace and
component quoting removed) before comparing, so the three spellings are one
table. `tableKey` returns empty for anything that is not a plain table header, so
an array-of-tables header and ordinary key lines can never compare equal to one.

### P4 needs a decision: `search-term-word-length`

The finding is accurate: `len(word) < 3` is byte length, and the function's own
comment says "anything shorter than three letters". The proposed fix is
`utf8.RuneCountInString`.

**Applying it as proposed would break the rescue for whole language families.**
Measured against the current code:

```
"τι κανεις σημερα"  -> "τι+κανεις+σημερα"
"什么 是 这个 项目"    -> "什么+是+这个+项目"
"что это за проект" -> "что+это+за+проект"
```

Under a rune count, every term in the Chinese question is 1 or 2 characters and
would be dropped, leaving an empty term: the rescue stops answering Chinese
questions at all. Byte length is inconsistent across scripts (Greek `τι` is two
letters and survives where English `to` does not) but it accidentally keeps CJK
alive.

So this is a product choice about how a language-independent rescue treats
non-alphabetic scripts, not a clean fix. It bears directly on the
answer-in-the-question's-language capability shipped the same day. Options:

- Keep byte length, and correct the comment to say what it measures.
- Count runes with a per-script floor (1 for scripts whose words are short by
  nature, 3 for alphabetic scripts), which is the start of the language pack v1
  deliberately does not have.
- Count runes and accept that CJK questions reach the rescue with no term, taking
  the honest zero.

## Open decisions

- `index-build-lease` (batch 1, F7): untouched, as instructed.
- `search-term-word-length` (P4): raised above.
