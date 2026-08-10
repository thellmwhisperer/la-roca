# Closing review, 2026-08-10

Review of everything shipped on 2026-08-10 (base `5509078`, 226 files, +7843/-2391):
the four Gherkin acceptance waves, the AXI/TOON output rework, the English sweep,
language-agnostic answers, rows-by-default query, file-based logging with counted
discards, the ingest UX, the comment sterilization and the `skills/` prune.

Gate state at the end of this branch: `make check` is green end to end (build, fmt,
vet, unit tests, 64 per-domain scenarios plus the legacy suites, slop gate at
22/22 active clones). The slop ceiling was **not** raised: the one clone this
branch's new tests introduced was removed by folding paired cases into tables.

## CodeRabbit: skipped, and why

**CodeRabbit produced no findings. It failed twice and was not retried further.**

1. Full working tree at `--base-commit 5509078`: refused with `too_many_files`,
   226 files against a limit of 150.
2. Split by directory as the tool itself suggested (`--dir internal`, 144 files,
   reported as fitting): refused with `rate_limit`, "You've used all free CLI
   reviews for now", reset in 40 minutes.

The rate limit is not incidental. This is a disposable worktree with no git
remote, so CodeRabbit cannot attribute the review to the organization and falls
back to the free CLI allowance even though the account holds a Pro seat:

> CodeRabbit couldn't find a Git remote for this repository, so it can't match
> the review to one of your organizations. This review will use the free CLI
> allowance, even if you're signed in.

So every finding below is from this review alone. Two structural notes for next
time: the 150-file cap means a whole-day review has to be split by directory from
the start, and a review run from a worktree with no remote will always be
rate-limited rather than billed to the org seat.

## Findings

Severity: **high** breaks a documented contract or a gate; **medium** produces
wrong or dishonest output; **low** is cosmetic or a consistency defect.

| # | Source | Sev | Finding | State |
|---|---|---|---|---|
| 1 | this review | high | `make check` was already red at `5f02197`. The vocabulary guard walks gitignored local agent state, so `.claude/settings.local.json` failed the build over a file the repository does not contain. | fixed `28b59c7` |
| 2 | this review | high | `renderUninstall` branched on the purge *request*, not the outcome, printing `purged: yes` directly under the errors saying it failed. `--json` reported `purge && report.Purged` correctly, so the two surfaces contradicted each other. | fixed `9f1a872` |
| 3 | this review | high | `lifecycle.removeDataDir`'s bounded survivor list hardcoded "La Roca did not create" for the overflow bucket, misclassifying owned survivors as foreign. This is the exact misclassification the D-7 second half exists to prevent, and it sends the operator to delete product files by hand. Also read "and 1 more files". | fixed `80ab472` |
| 4 | this review | medium | `ParseSubagent` dropped the surplus side of an unbalanced transcript with no discard recorded: `pairs := min(len(humans), len(agents))` silently lost the rest, and a transcript that paired into nothing returned zero discards, so "no exchanges" was indistinguishable from "empty file". | fixed `9a6001c` |
| 5 | this review | medium | `ParseCodexSession` never set `Deferred` at all. A rollout whose last event is a `user_message` lost that turn from the exchanges, the discards and the deferred count at once, so a live tail was invisible in all three. A `user_message` arriving over an unclosed one also lost the first silently. | fixed `394663b` |
| 6 | this review | medium | Empty thinking blocks were stored as corpus noise in both parsers: `pi.go` appended a trimmed-to-empty `thinking` block (its sibling `text` blocks already refuse empties) and `codex.go` appended `Thinking{Text: ""}` for a reasoning event with no summary. Neither skipped nor counted. | fixed `133f254`, `394663b` |
| 7 | this review | medium | `DiscardDetail.Record` is documented as the record position, and for OpenCode/Hermes the complaint's index in the complaint list was handed over as one. The report said "record 2" about a database that has no second record. | fixed `e4561c4` |
| 8 | this review | medium | A dry run whose `tableCounts` call failed reported `counts_before` as five zeros with nothing beside them, reading as "this database is empty". The sibling `LoadState` failure on the same path does warn. | fixed `e4561c4` |
| 9 | this review | low | `axi.Exec` was the one renderer formatting its own count: `fmt.Sprintf("%d rows")` gave "1 rows" and no thousands grouping, bypassing the `Quantity`/`Number` helpers shipped the same day. A pre-existing assertion pinned the bug (`"rows ·"` over a `COUNT(*)`); it was corrected with the fix. | fixed `da1ba70` |
| 10 | this review | low | `renderIngest`'s deferred line printed "1 exchanges", the same plural defect the two preceding commits fixed elsewhere. | fixed `cdd45ba` |
| 11 | this review | low | `ingestSourceLabel` derived the label from the session count, so the live rows called one source "claude" until its first session landed and "claude-code" after: one source under two names inside one report. | fixed `cdd45ba` |
| 12 | this review | low | The English sweep missed two unit fixtures: one temp filename in a file it did sweep, and a Spanish fixture added 45 minutes after it in a file it never reached. | fixed `a7f3751` |
| 13 | this review | low | The store wave filed its delta inventory at `test/acceptance/store-domain-delta.md`; the other three used `features/<domain>/delta-inventory.md`. | fixed `391b82b` |
| 14 | this review | medium | A log-write failure turns a successful command into exit 1. | **needs-decision** `logging-failure-is-fatal` |
| 15 | this review | medium | `dedupRows` reorders and filters any result set with `source` and `text` columns, including model-generated SQL with its own `ORDER BY`. | **needs-decision** `dedup-reorders-model-sql` |
| 16 | this review | low | `pi.go` mixes two numbering schemes in one discard stream. | logged (below) |
| 17 | this review | low | `loginModel` writes the config before the credential is saved. | logged (below) |
| 18 | this review | low | The uninstall prompt reads `[Y/n]` but only exactly `n` purges. | logged (nit) |
| 19 | this review | low | `logfile.Append` globs and prunes the stream on every single write. | logged (nit) |
| 20 | this review | low | The store domain has no `bite-proof.md`; the other three waves do. | logged (gap) |

### The two that need a decision

**`logging-failure-is-fatal`** (`internal/distribution/cli/cli.go:75-77`). When
`logExecution` fails, `Execute` returns `(ExitError, logErr)`. A `roca query` on a
machine whose `~/.roca/logs/` is not writable prints its answer to stdout and then
exits 1 with an error, so a script checking the exit code concludes the query
failed while holding the answer. Both readings are defensible, which is why this is
not fixed here: either the JSONL trace is part of the product's promise and failing
to write it is a real failure (current behaviour, and the README states the
guarantee unconditionally), or observability must never fail the command and the
failure belongs on stderr as a warning with the command's own exit code preserved.
The second option narrows a guarantee documented the same day, so it is the owner's
call. No test pins the current behaviour either way.

**`dedup-reorders-model-sql`** (`internal/provider/service/query.go:131-166`).
`found()` runs `dedupRows` on every answer, including the two model-SQL paths
(`llm.go:181`, `llm.go:318`). Any result set whose columns include `source` and
`text` is then deduplicated, re-sorted by `relevanceRank` (memory first, thinking
last) and stripped of rows whose `text` is blank or not a string. The consequence:
a question answered by model SQL carrying its own `ORDER BY` displays rows in a
different order from the SQL displayed beside them, and rows can vanish with no
note and a reduced `row_count`. Fixing it means choosing which surface is
authoritative, and the store delta inventory lists this ranking as a deliberate
capability, so it is a product decision rather than a defect to patch.

### Logged, not fixed

- **16** `pi.go` numbers discards two ways in one stream: `piRows` uses physical
  line numbers, `piEntries` sets `record = index + 2` over the *filtered* rows, so
  a file with one invalid line reports two different records under the same number.
  Cheap to unify, but it changes the meaning of every Pi discard position and
  deserves its own change rather than a ride on this branch.
- **17** `loginModel` calls `SetProviderModel` and `SetModelOrder` before
  `store.Save(token)` / `SaveAPIKey`, so a credential write that fails leaves the
  provider order rewritten with no credential behind it. `models.go` is
  review-only on this pass by instruction, so this is recorded for the split that
  ships with the model picker.
- **18** The prompt reads `Keep the Roca database ...? [Y/n]` and purges only on
  exactly `n`, so a full-word `no` keeps the data. It fails in the safe direction
  and the output says plainly what happened, so it is a nit.
- **19** Every `logfile.Append` does a `filepath.Glob` plus a date parse per match.
  Harmless at current volumes.
- **20** `features/store/` has no `bite-proof.md`. Writing one means mutating code
  and observing the scenarios fail, which is work to commission, not to invent.

### Reviewed and found sound

- `cli/models.go` (720 lines, review only): no functional defect. Finding 17 is the
  only real issue; `modelChoiceSource` rebuilding a map literal per call and
  `renderDoctor` doing the same for status strings are allocations, not defects.
- The purge inventory itself (`ownedPaths`): the journals, the cache **root** and
  the per-runtime skill directories are all declared, and the skill entries use
  `filepath.Dir` of a path under each runtime's own directory, so nothing outside
  a Roca-created directory can be reached. `logExecution` refuses to recreate a
  data directory a concurrent purge removed, and `--purge` pre-logs then sets
  `prelogged` so the run leaves no record behind it.
- `logfile` redaction and retention: the documented "current day plus the previous
  29" matches `AddDate(0, 0, -(RetentionDays-1))`, and the README is explicit that
  a stream is pruned only when it is next written, so a dormant stream keeping old
  files is documented rather than a defect.
- The Spanish under `features/` and `test/acceptance/` is **deliberate coverage**,
  not sweep leakage: `F08-04` mixes Spanish and English questions in one Examples
  table to prove surface parity across languages, and Spanish questions are what
  exercise "answer in the question's language" (`ddd7632`). Removing it would
  delete the evidence for a capability shipped the same day. Left alone on purpose.
- The superseded-memory filter (`a32d887`) is correctly inverted and pinned at the
  store seam.

## Consolidated prune-candidate list

One list from the four delta inventories, with a verdict per item. "Keep" means
the code is reachable by a real user and the gap is coverage debt, not dead code.
"Prune" means the evidence says nothing needs it. Nothing here was deleted.

### Prune (evidence of no consumer)

| Item | Where | Evidence |
|---|---|---|
| `service.Search`, the model-free search entry point | `internal/provider/service/search.go` | **No production caller.** Every caller is a `_test.go`; there is no `search` command; the store inventory says its consumer is the golden bench, and the golden-bench machinery was deleted the same day (`c5e8de0`). Pruning it also removes the service-level search tests, so the cost is rewiring those onto the engine directly. |
| Legacy root/default config lookup | `internal/provider/config/file.go`, `config.go` | "Legacy" compatibility in a product with no released predecessor. `ROCA_CONFIG` relocation is worth keeping; the legacy lookup path beside it has nothing to be compatible with. |

### Needs a decision (product scope, not coverage)

| Item | Where | Question |
|---|---|---|
| Successful model SQL and successful model prose | `provider/service/llm.go` (`llmStage`, `Interpret`) | **The most important gap in the whole inventory.** The primary happy path of the product has no approved scenario: every provider scenario covers degradation, selection or refusal. Commission coverage; do not prune. |
| SQL gate allowlist, schema/table/column checks, hidden tables, chained-statement rejection, JOIN validation, LIMIT clamping | `provider/query/sqlgate/` | The security boundary for model-authored SQL, entirely unclaimed. Second highest coverage priority. Keep and commission. |
| DeepSeek, Z.ai, xAI and custom OpenAI-compatible adapters | `provider/openai.go`, `catalog.go` | Four adapters, one xAI scenario. Each is a live maintenance surface. Does v1 ship four key providers or fewer? |
| `roca exec` and `roca query --sql-only` | `cli/commands.go`, `service/query.go` | No scenario claims `exec` on either surface. They are each other's natural pair, and MCP exposes `roca_sql` plus `roca_exec` for the same job. Keep both, or keep only the MCP pair? |
| MCP `roca_sql` as a separate tool | `distribution/mcpplug` | Compile-only SQL beside `roca_query --sql-only`. One job, two spellings. |
| Hermes cost/token metadata and finish reasons | `internal/ingest/hermes.go` | Ingested data nothing queries and no scenario claims. Keep the source, drop the unread columns? |
| Full embedded layer registry and classifier labels | `provider/layers/layers.go`, `data/layers.yaml` | Likely wider than v1 uses. Worth an audit of which layers real corpora produce. |
| Row deduplication and relevance ordering; search across exchanges, thinking blocks and sessions with source priority | `service/query.go` (`dedupRows`, `relevanceRank`), `query/fts.go` | Ties directly to finding 15. Deciding that decides this. |
| Private-repository release authentication | `distribution/release/release.go` | Token-authenticated release discovery in an OSS product distributing public binaries. |
| Interactive adoption by copy | `store/backup.go`, `cli/commands.go` | Claimed by no scenario, and scenarios 4 and 5 reach adoption through `--db-path` instead. Keep the flow or make `--db-path` the only door? |

### Keep, as coverage debt

Reachable operator surfaces or safety code, unclaimed only by the new per-domain
scenarios. Grouped, not enumerated one by one:

- **CLI surface**: the hidden-but-callable commands (`version`, `schema`, `index`,
  `health`, `mcp`, `skill`, `logout`, `model`, `models`), their nested surfaces,
  and the unclaimed flags (`--db-path`, `--layer`, `--max-chars`, the store
  provenance/status/metadata/supersession set, update selection, MCP overrides).
- **Bootstrap**: interactive init adoption and reinitialization, `schema status`
  verdicts, orphan-table reporting, read-only refusals, exit-code distinctions,
  spinner behaviour, contextual help rows.
- **Install and lifecycle**: `agentcfg` surgical edits across TOML/YAML/JSON/JSONC
  for five runtimes with env overrides, idempotency, concurrent-change refusal and
  recovery backups; skill path overrides, repeated-install idempotency,
  canonical-content protection; checksum verification, archive extraction,
  download limits, token privacy, fallback download, interrupted-install
  convergence, foreign-binary refusal, symlink resolution, atomic swap, version
  health checks and rollback. Security-relevant; prune nothing here.
- **Purge**: convergence after partial failure, foreign-file refusal, journal
  cleanup and JSON path privacy. Bounded survivor **classification** is now
  claimed by unit coverage added in this branch (finding 3).
- **MCP plug**: the five-tool catalogue, schemas, descriptions, handshake
  metadata, pipe-close behaviour, malformed-call recovery, read-only behaviour,
  database-path scrubbing and structured-content envelopes.
- **Ingest**: root precedence including XDG, Windows and `~`; Windows/WSL path
  equivalence and longest-root selection; Claude compaction placement, tool-result
  backfill, latency and partial-line tolerance; Cowork sidecar merging; subagent
  layout variants and parent identity; Pi active-branch selection and fingerprints;
  OpenCode graph traversal and malformed-row isolation; Codex state-database
  enrichment; foreign-database schema validation; memory update-in-place and
  immutable landed exchanges; symlink de-duplication. Deferred-exchange reporting
  and per-source narration are now partly claimed by this branch (findings 5, 10, 11).
- **Provider**: the correction attempt after a gate rejection, distinct rescue
  reasons, no silent fall-through after a provider turns faulty, Codex subscription
  chat and OAuth (PKCE, loopback, refresh, logout), Ollama chat and remedies,
  environment order precedence, timeouts, the documented config keys, model
  switching with unrelated TOML preserved, doctor's report fields, schema-aware
  prompt construction, and FTS compilation with the LIKE floor.
- **Store**: write-time deduplication and layer aliases, the store flag set,
  read-only refusal, search provenance and LIKE degradation, `--layer`,
  `--max-chars`, `roca index`, `roca health`, backup guards (same-second refusal,
  integrity verification, per-table row counts), and concurrency beyond two
  writers.
- **Already claimed elsewhere, no debt**: structural adoption that ignores DDL
  text is claimed by the legacy suite (D-4/D-4b); the store wave simply does not
  restate it.

## Commits on this branch

```
80ab472 fix(lifecycle): classify the survivors the overflow stops naming
9f1a872 fix(cli): report the purge outcome, not the purge request
28b59c7 fix(cli): keep local agent state out of the vocabulary gate
9a6001c fix(ingest): count the turns positional pairing cannot use
394663b fix(ingest): account for every turn a codex rollout does not land
133f254 fix(ingest): keep textless thinking blocks out of the corpus
e4561c4 fix(ingest): report the honest position and the counts it could not read
da1ba70 fix(axi): count rows through the quantity helper
cdd45ba fix(cli): count held exchanges in prose and keep one name per source
b9c25a9 fmt: gofmt the lifecycle and codex changes
391b82b docs(acceptance): file the store delta inventory with its domain
38c072a test: fold the paired new cases into tables to hold the slop ceiling
a7f3751 test: english sweep for the two unit fixtures it missed
```

Every behavioural fix landed red first: the reproducing test was written and seen
to fail before the production change. No new features, no new abstractions, no
raised ceilings.
