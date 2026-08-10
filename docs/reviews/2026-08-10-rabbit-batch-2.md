# CodeRabbit whole-repo pass, batch 2

Base `2468208` (the OSS sweep seed), reviewed per domain with
`coderabbit review --committed --base-commit 2468208 --agent --dir <d>`.

Domain status:

| Domain | Files | Outcome |
|---|---|---|
| `internal/provider` | 58 | 8 findings, then the review **timed out server-side** at ~55 min: coverage is partial |
| `internal/distribution` | 73 | 33 findings, completed cleanly |
| `cmd` | 1 | completed, no findings |
| `test` | 40 | 3 findings, completed cleanly |
| `features` | 30 | **not reviewed**: the free allowance ran out on the last domain (`rate_limit`, 43 minute wait) |
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
  separately) so each review fits the server's budget.

`internal/distribution` was the counter-example: 73 files, larger than the 58 that
timed out, and it completed cleanly with 33 findings. So the timeout is not a
simple function of file count and provider's was closer to bad luck than to a
hard ceiling. `features` never ran at all: the free allowance was exhausted by
then, so two of the six domains are still owed a pass.

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

## internal/distribution: 33 findings, 31 fixed, 2 deferred

The three that matter most were not the ones rated highest.

**D22, rated critical, and it earns it.** `ownedPaths` is built with `os.Getenv`,
and two tests hand that inventory straight to `lifecycle.Apply`, which calls
`os.RemoveAll` on every entry. `CLAUDE_CONFIG_DIR` is a supported override, so on
a machine that sets it, running `make check` DELETED the operator's real skill
directory. The isolation now lives at the resolution seam (`resolvedIn`), so a
test cannot opt out by forgetting, and one owner is shared with the
skill-example fixture.

**D15, and the report said it succeeded.** A Hermes `mcp_servers` written as a
flow mapping that already holds another server took the block-map path, which
appends block YAML under a value written inline. The declaration landed at the
document's TOP level:

```yaml
mcp_servers: {other: {command: theirs, args: [x]}}
roca:
  command: roca
  args: [mcp, serve]
```

Hermes never reads that key, the operator's file gains a stray one, and the
command reported success. Refused now with a named remedy, the same shape the
TOML editor already uses for an inline table.

**D30 is a D-7 violation.** The skill withdrawal used `os.RemoveAll` on the skill
directory, so anything the operator had put beside the canonical `SKILL.md` went
with it. The parent-directory cleanup three lines below already had the right
shape (`os.Remove`, which only succeeds when empty); the fix removes the file and
lets the directory follow only when nothing else is left.

Two findings closed holes in this branch's own earlier work:

- **D26**: the purge aborted when it could not write its pre-purge trace,
  contradicting the `logging-failure-is-fatal` ruling applied in the same branch.
  The trace is now named in the report and the purge goes on, with `prelogged`
  set either way so the post-run record cannot recreate the directory the purge
  is about to remove.
- **D31**: batch 1 made the readable verdict branch on the OUTCOME, but the
  withdrawal's error paths only appended to `report.Errors` and left `Purged`
  standing, so a purge that could not withdraw an integration still printed
  "purged: yes" under its own errors. Every error path now takes the verdict with
  it.

The rest, by theme:

| # | Sev | Finding | State |
|---|---|---|---|
| D1 | minor | The MCP narration test compared a path against JSON without accounting for escaping. | fixed |
| D2 | minor | `spinner.finish` says "safe to defer" and panicked on the second `close(s.stop)`. | fixed red-first |
| D3 | minor | The passthrough meta-test inspects `ReturnStmt` loosely. | **deferred**: hardening a meta-test's AST matching, no product behavior |
| D4 | **major** | `--config` applied to every runtime edited one file once per runtime, each pass with a different agent's rules. | fixed red-first: refused for more than one runtime |
| D5 | minor | The TOON size assertion claimed an order of magnitude and checked 8x. | fixed: the real ratio beats 10x |
| D6 | minor | `--metadata null` decoded without error and carried nothing. | fixed |
| D7 | minor | The package doc claimed six tools over five registrations. | fixed |
| D8 | minor | `published.Tag == current` compares versions verbatim. | **needs-decision** `update-version-comparison` |
| D9 | minor | The clipping assertion measures the whole output. | **skipped, false positive**: for one column and one row `rowOutput` returns the bare value, no TOON header and no indentation, so it already measures the field |
| D10 | minor | The inline-table refusal matched the servers key as a PREFIX, refusing a document over `mcp_servers_legacy`. | fixed red-first |
| D11 | minor | `forgiven` computed a slice from `strings.Index` without checking for -1. | fixed |
| D12 | **major** | `err == io.EOF` misses a wrapped shutdown, so the server's normal end of life exited as a failure. | fixed |
| D13 | **major** | `ingestRows.finish` panicked on a second call. | fixed red-first |
| D14 | minor | The YAML flow-span scan applied backslash escaping to single-quoted scalars and looked one byte BEFORE the span, so it ran past the mapping and swallowed the operator's trailing comment. | fixed red-first |
| D16 | minor | `doc["status"] == ""` never fires for a MISSING key, so it passed over an envelope with no status. | fixed |
| D17 | minor | `write` promises the previous permissions are kept; `os.WriteFile` applies its mode only when it creates the file, and `CreateTemp` already made it 0600, so an operator's 0644 config came back 0600. | fixed red-first |
| D18 | minor | `os.Stat`'s error dropped before `info.Size()`: a nil dereference when the database goes between the check and the stat. | fixed |
| D19 | minor | A payload over the ceiling was truncated to exactly the ceiling and returned as success, failing its checksum with a misleading reason. | fixed |
| D20 | minor | `sawToken` written on the server goroutine and read on the test's: a real race. | fixed, verified under `-race` |
| D21 | **major** | The JSON surface filtered error entries that ARE the database path, but an error is prose with the path inside it, so a failure published the location the surface exists to withhold. | fixed red-first: scrubbed, suffixes before the bare path |
| D23 | minor | My own batch-1 overflow test did not fail when no overflow line existed. | fixed |
| D24 | minor | The MCP privacy assertion was skipped by a bare `return`, and its fixture (`roca_sql`) answers "there is no model to ask" with no error: the check had NEVER run. | fixed: pointed at `roca_exec`, which really errors |
| D25 | minor | The bare-login subtest read the developer's real home. | fixed |
| D27 | minor | `modelSet` accepts an empty model ID. | **deferred to the models.go split** |
| D28 | minor | `knownProviderNames` returns map-order names. | **deferred to the models.go split** |
| D29 | **major** | A log file that could not be removed stopped the rotation AND failed the append that called it. | fixed: rotation is housekeeping, never the append's verdict |
| D32 | minor | My own batch-1 chmod test needs a root and Windows skip. | fixed |
| D33 | **major** | Redaction missed `access_key`, `private_key`, `signing_key`, `session_key`, and the AWS and Google key shapes. | fixed, with table tests including the `keyword` non-match |

`models.go` stays review-only by standing instruction (its split ships with the
model picker), so D27 and D28 are recorded for that pass rather than edited here.

## test: 3 findings, 3 fixed, and one of them corrected in flight

All three were unchecked type assertions in the acceptance harness: a delta field
that panicked inside the step instead of naming itself, a budget step that
appended `"<nil>"` for a row it could not read, and `searchRows` treating a
malformed answer as an empty one.

The third is worth recording, because the finding as written is wrong for this
codebase. It asked that a MISSING `rows` field be an error. Applying that turned
the store suite red:

```
Scenario: No match is an honest zero, not an error
  the answer declares no rows field: {"row_count": 0, "match": "empty", ...}
```

`QueryResult.Rows` carries `json:"rows,omitempty"`, so an absent `rows` IS the
honest zero. Absent-is-empty is the contract, and only a `rows` field that exists
and is not a list is malformed. Half the finding applied; the other half would
have broken the guarantee it looked like it was protecting.

## Superseded while this branch was open

`main` advanced 29 commits during this batch (a large ingest wave, a surface
contract rework, a sqlgate pass, and the public cleanup), so the branch was
rebased four times. Three items were overtaken and their fixes dropped in favour
of main's, which reached the same place:

- **The dedup ruling itself.** Main implemented the identical split
  independently, naming the presentation recorder `foundSearch` where this branch
  called it `foundRanked`. Same semantics: `found` records what the SQL returned,
  and only the literal search applies presentation. Main's naming won; what
  survives from this branch is the end-to-end test
  (`TestTheModelPathReturnsTheRowsItsSQLProduced`), which drives a real model-path
  query and is not tied to either name.
- **D11** (`forgiven` guarded against a term absent from its compound): main
  rewrote the vocabulary guard to match SHA-256 digests of encoded terms, and the
  `forgiven` helper no longer exists.
- **D5** (the TOON ratio assertion): main replaced that test with a per-row
  character budget plus a marked-truncation count, a stronger contract than the
  size ratio it tightened.

One earlier conclusion of mine was also overruled, and the report should say so
rather than leave a claim the tree contradicts: batch 1 recorded that the Spanish
fixtures under `features/` and `test/acceptance/` were deliberate coverage for
answering in the question's language and should not be swept. `main`'s AGENTS.md
now reads "Public source, documentation, features, and fixtures are English-only;
use unmistakably synthetic test data", and the fixtures were translated. The
later decision stands; the batch 1 note is obsolete.

Note on this file's home: `main` deleted `docs/reviews/` and every delta
inventory as internal working material. These two batch reports are the only
things left under that path, so they are transient by construction: keep them for
the merge and delete them with the rest, or name a home that survives.

## What is still owed

- `internal/provider` past the timeout, ideally scoped per package.
- `features`, which never ran.

## Open decisions

- `index-build-lease` (batch 1, F7): untouched, as instructed.
- `search-term-word-length` (P4): raised above.
- `update-version-comparison` (D8): `published.Tag == current` is a verbatim
  comparison, and nothing in the tree normalizes versions. `git describe` yields
  `v0.1.0-5-gabc` or `v0.1.0-dirty` where the release tag is `v0.1.0`, so those
  never compare equal and `roca update` re-downloads and self-replaces. Whether
  that is wrong depends on intent: re-installing over a dev or dirty build may be
  exactly right. Inventing normalization rules on the self-replacement path is a
  product call, not a fix.

