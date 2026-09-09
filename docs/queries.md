# Queries, explore, and the read-only gate

For human answering, see [optional playground installation](plugins.md#optional-human-answering).

First-time path: [install and initialize search](lifecycle.md#install).

`roca query` is hybrid search with no answering-model inference: it selects
rare full-text terms, embeds the question plus static question templates when
a vector index exists, fuses the two lists with RRF, and labels which legs
found each hit. Without a vector index the same command runs full-text alone.
`--top N` (default 10) controls the fused result count, `--require-both` keeps
only dual-confirmed hits, and `--databases` narrows the default (every attached
plugin database, including ops). Vector coverage follows the
[sidecar selection rules](vector.md#first-query). `--json` returns the complete
machine envelope; see [Memory identifiers for clients](#memory-identifiers-for-clients)
for its identifier encoding.
Deterministic search checks only that questions contain text and stay within
the 1000-character cap on both CLI and MCP query surfaces. Phrases such as
`system prompt` are searchable even with `features.strict_input = true`;
prompt defenses belong to the [model-invoking playground](models.md#what-happens-in-the-playground).

`roca playground` is the human room: it compiles a question into one checked
`SELECT`, `--sql-only` compiles without executing, and `--full` adds a prose
reading of the rows. `roca exec` runs your own `SELECT` through the same
read-only gate. By default it uses the configured
[`query.timeout_ms`](models.md#the-configuration). `--timeout-ms N` overrides that
statement budget for one invocation; `0` disables the bound. Vector indexing's
statement-budget exceptions are owned by [Local vector
search](vector.md#index-declared-databases); query-time source lookups keep the
interactive budget.

For investigations, `roca explore "<term>"` uses the same checked query and
second-inference seat but gives the interpreter an investigation mission. Every
explore prints grounded prose and the generated SQL. Plain mode adds short trail
hints; `roca explore --deep "<one bare word>"` also maps deterministic terrain
from that run's rows (source counts, month clusters, co-occurring terms, and
negative space) and proposes two or three single-concept probes. The mode is
always explicit. `models.explore_order` can route deep interpretation to a
stronger model, falling back to `models.interpret_order` and then the main
order.

Model-written SQL is repaired before that gate and then judged by its unchanged
rules; the SQL you write yourself for `roca exec` never is. `model_sql` keeps
the untouched model output and `repaired` names each repair applied, listed
under [Model providers](models.md#the-repairs-between-the-model-and-the-gate).
If the repaired candidate still fails either the gate or at execution, La Roca
gives the model exactly one correction attempt with that SQL and SQLite's exact
verdict before using the literal rescue. `retry_type` distinguishes
`gate_rejection` from `execution_error`; the JSON envelope retains both attempts
and attributes the retry latency separately.

`roca playground` recovers with SQL plus a local FTS5 index with diacritic
folding; a plain `LIKE` fallback works before the index exists. Its configured
model supplies semantic interpretation at question time, while the checked SQL
retrieval stays exact and auditable. No usable provider, or SQL that cannot run,
falls back to literal search and says so in the result. `roca query` instead
uses the deterministic hybrid path described above.

## Text budgets

`--max-chars N` sets the character budget per returned text field for `roca exec`,
`roca playground`, and `roca explore`, and per snippet for `roca query`. The
default is 500; zero or a negative value also selects that default. The same
budget applies to CLI TOON output, CLI `--json`, and MCP's `max_chars` argument.
TOON does not impose a smaller preview limit; selecting JSON does not expand
the text.

Characters are counted as Unicode code points, including truncation ellipses;
TOON quoting and escaping are outside that budget. Numbers, booleans, and
memory identifiers are not clipped, and a query's snippet budget does not
shorten its source citation. For example, this returns up to 900 characters of
the selected content:

```sh
roca exec "SELECT content FROM plugin_roca_ops.memories WHERE layer='handoff' LIMIT 1" --max-chars 900
```

Use `--max-chars 100` for a shorter excerpt or a larger value to expand it.

## Table names in authored SQL

`roca exec` refuses an unqualified table reference when an attached database
exposes that name, even if only one attached database has it. The error lists
the available qualified candidates; for example, `FROM memories` can report
`unqualified table "memories"; candidates: plugin_roca_ops.memories, plugin_roca_corpus.memories`.
Choose the intended database and use its qualified name. This prevents a bare
name from silently reading an empty core table. Candidates come from the
attached schemas, so the list depends on the installed databases.

Qualified references keep their existing behavior, including explicit `main`
references to core. A core-only installation without attached schemas keeps
its existing name resolution. CTE names and expressions such as `SELECT 1`
do not require a database qualifier. This check applies to authored SQL through
`roca exec`, MCP `roca_exec`, and remote exec/cross calls; model-backed queries
and their keyword rescue retain their existing core-name compatibility.
The regression contract lives in
[`unqualified_test.go`](../internal/provider/query/sqlgate/unqualified_test.go).

SQL returned by `roca playground --sql-only` or MCP `roca_sql` can therefore
still contain bare table names. Before submitting it to `exec`, inspect those
references and qualify them for the intended database; compilation alone does
not establish that the statement passes the authored-SQL check.

For a common authored query, see the README's
[exact SQL example](../README.md#drop-to-exact-sql-whenever-you-want).

## Memory identifiers for clients

Memory identifiers in SQL JSON results, memory-operation envelopes, and MCP
tool metadata are decimal strings, including small core IDs. SQL TOON output
quotes IDs outside JavaScript's safe integer range; safe numeric IDs can appear
unquoted. Keep returned ID strings intact in JavaScript: converting them to
`Number` can round an ops ID and point a later write at the wrong row.

SQL result conversion recognizes identity column names and stringifies integers
outside JavaScript's safe range even under other aliases. Safe non-identity
integers remain numbers, and SQL NULL remains null. Metadata objects are
normalized recursively when stored; JSON objects and arrays in a result's
metadata column receive the same conversion if they survive the text budget
as valid JSON, while remaining JSON text. The conversion
rules are owned by [`internal/jsonid`](../internal/jsonid/jsonid.go).

Pass the returned ID directly to CLI `roca store --supersedes "$id"` or as a
decimal string in the MCP `roca_store` `supersedes` field. MCP still accepts
integer JSON input for compatibility, but cannot recover digits a client
already rounded. The CLI accepts decimal argument text as before. This changes
client encoding, not SQLite identifier storage or SQL comparisons.

## Read-only queries across machines

`roca remote` connects already-installed Roca instances through ordinary SSH.
SSH configuration owns host aliases, keys, agents, and authentication; La Roca
stores only the name-to-target registry in `~/.roca/remotes.json`. There is no
listener, daemon, sync protocol, or additional port.

```sh
roca remote add studio --ssh dev@studio.example
roca remote list
roca remote exec studio "SELECT layer, COUNT(*) AS n FROM plugin_roca_ops.memories GROUP BY layer"
roca remote vector query studio "the deployment decision" 20
```

Each data call first checks that the local and remote Roca versions match, then
runs plain `ssh <target> roca ... --json`. `remote exec` reaches the remote
read-only SQL gate, so a non-`SELECT` is refused exactly as it is locally.
`remote vector query` likewise preserves the remote index's own result or its
honest not-installed/not-ready error. Default output is bounded TOON with
contextual `help[]`; `--json` returns the full result envelope.

Transport failures are scriptable: exit 10 means SSH could not reach the
target, 11 means `roca` is absent from the remote `PATH`, and 12 means the Roca
versions or envelopes are incompatible. A query refused by the remote gate or
vector index keeps the ordinary command-failure exit 1 and message.

Cross-machine comparison scatters one inner `SELECT` to the local installation
and every named remote, then concatenates the returned result sets in Go:
local rows first, followed by remotes in `--on` order, preserving row order
within each origin. It adds an `origin` column, fills missing columns with
nulls, and preserves value types, exact identifiers, and the existing text
budget. Gathering opens no SQLite connection. The envelope retains its
generated `UNION ALL` SQL description, but that description is not executed.

See the README's [cross-machine example](../README.md#compare-rocks-across-machines).
Pass comma-separated names to `--on`, such as `--on studio,laptop`, to compare
several remotes in one call.

Cross disables reconciliation and call-history writes for the run and opens
the local stores read-only, so it writes to neither the local nor remote rocks.
The data path is SQL and JSON only; it performs no inference.

## One search, labeled evidence

The compact default output identifies every source as `database.table.id` and
shows whether FTS, vector, or both legs found it. Vector hits carry cosine and
vector rank, FTS hits carry FTS rank, and `consensus` makes agreement visible.
The JSON envelope additionally keeps the RRF score and split source fields.

```text
$ roca query "have I fixed a stale lock error before"
search hybrid · engines fts,vector · 18 ms
databases: core, corpus, ops
terms[3]: stale, lock, error
rows[2]{rank,source,legs,consensus,vector_score,vector_rank,fts_rank,snippet}:
  1,corpus.exchanges.912,vector+fts,true,0.61,2,1,"fixed: stale .lock left by a killed run; remove it and rerun"
  2,corpus.memories.207,vector+fts,true,0.57,4,2,"Pattern: a killed ingest can leave its lock file behind"
```

The FTS leg measures each token against the selected live indexes, removes
zero-frequency and broadly common terms, and keeps a small rare-term set for
BM25 ranking. The vector leg embeds the raw question plus fixed Spanish and
English question wrappers, oversamples neighbors, applies a similarity floor,
and deduplicates chunks by stable source. RRF then combines ranks without
normalizing either leg's native scores. If vector search is unavailable,
missing, or still downloading, the same envelope reports the notice and contains
the federated FTS results alone. The query never waits on the embedding model
download.

## The playground's two readers

`roca playground` preserves the model-written SQL room. Its default output is
efficient for an agent or a human inspecting the generated statement:

```text
$ roca playground "have I fixed a stale lock error before"
route model
SQL · provider codex · model gpt-5.6 · 2.9 s
search · 3 ms
rows[2]{source,created_at,text}:
  exchange,"2026-06-14 23:41:02","fixed: stale .lock left by a killed run; remove it and rerun"
  memory,"2026-06-15 00:02:19","Pattern: a killed ingest can leave its lock file behind"
```

Add `--full` when a human wants a second model pass to explain those rows:

```text
$ roca playground --full "have I fixed a stale lock error before"
SQL · codex · gpt-5.6 · 2.9 s / search · 3 ms / answer · ollama · gemma4:12b · 11.4 s

Yes, twice, and both rows point to the same stale-lock failure and recovery.
```

Without an explicit `models.interpret_order`, the provider that writes the SQL
also reads the rows. The longer model, repair, and routing contracts live in
[Model providers](models.md); [the MCP plug](mcp.md) documents the shell-less
`roca_query`, `roca_sql`, and `roca_exec` equivalents.

## Session context

The session-context reads (`roca pill`, `roca pill show`, and `roca handoff
latest`) default `--project` to the basename of the working directory;
`handoff latest --all-projects` instead reads across projects. They
always read the existing `roca-ops` database; if
`features.roca_ops` is disabled or that database is missing, they refuse rather
than reading core or creating an empty ops database.

`roca pill [--project <project>]` loads active project and global pills, keeps
the newest timestamped row for each `metadata.pill_slug`, and lists unslugged
row IDs without loading them. `roca pill show <slug>` returns the selected pill.
Default AXI/TOON output includes complete content, `--json` returns the script
envelope, and there is no budget flag.

`roca handoff latest [--project <project>] [--limit N]` loads active handoffs
that no other memory supersedes. With no limit (or `--limit 0`) it keeps the
historical behavior: every current handoff is printed with complete content.
A positive limit keeps only the first N handoffs in newest-first order, without
clipping their content; negative limits are rejected. It chooses project
handoffs after that filtering and falls back to unsuperseded global handoffs
only when no project handoff remains. A later row alone does not supersede an
earlier handoff: a short worker receipt must name its predecessor to replace it.

`roca handoff latest --all-projects [--since 30d] [--limit N]` prints one row per
project with a current handoff, newest first, as
`lab[n]{project,last_handoff,head}`. Global handoffs are excluded, and combining
`--all-projects` with `--project` is rejected. `last_handoff` is the selected
handoff's creation timestamp; `head` is at most 3,000 Unicode characters,
including a trailing `...` when clipped. This is a per-head character cap,
not a total output byte budget. `--limit` caps the number of project rows.
`--since` filters only this cross-project view and accepts a nonnegative whole
day count followed by `d`, such as `30d`, measured back from the current UTC
time in 24-hour days. With that filter, rows with missing or invalid timestamps
are excluded. Omitting it imposes no age cutoff. `--json` returns the script
envelope, retaining the selected rows and their head caps. The CLI
implementation is owned by
[`internal/distribution/cli/session.go`](../internal/distribution/cli/session.go).

```sh
roca pill --project '<project>'
roca handoff latest --project '<project>'
roca handoff latest --project '<project>' --limit 1
roca handoff latest --all-projects --since 30d
```

A handoff is written only on explicit operator instruction. The
[handoff write policy](operations.md#handoff-writes) owns the allowed writers,
required shape, and replacement contract.

### Deleting a pill

`roca pill delete <slug>` permanently removes every `layer='pill'` row whose
`metadata.pill_slug` matches the slug in the existing `roca-ops` store. Matching
uses the same trimmed slug identity as the pill list. It removes all versions,
including inactive rows, across every project and global scope; `--project`
is rejected. Other layers are untouched. It creates no memory rows or
tombstones and exposes no generic memory delete, retire, or status-flip verb.
It requires `features.roca_ops` and an existing ops database, and is refused
in read-only mode.

Success prints `deleted: N`, the number of removed rows, even with `--json`.
An unknown slug exits non-zero and lists the known pill slugs in `help[]`
when any exist. Verify destructive behavior only against copied or laboratory
state, never the live operator store.
