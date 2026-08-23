# Queries, explore, and the read-only gate

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

`roca query` is hybrid search with no answering-model inference: it selects
rare full-text terms, embeds the question plus static question templates when
a vector index exists, fuses the two lists with RRF, and labels which legs
found each hit. Without a vector index the same command runs full-text alone.
`--top N` (default 10) controls the fused result count, `--require-both` keeps
only dual-confirmed hits, and `--databases` narrows the default (every attached
plugin database, including ops). `--json` returns the complete machine envelope.
Questions must contain text and have a generous 1000-character cap on both CLI
and MCP query surfaces.

`roca playground` is the human room: it compiles a question into one checked
`SELECT`, `--sql-only` compiles without executing, and `--full` adds a prose
reading of the rows. `roca exec` runs your own `SELECT` through the same
read-only gate. By default it uses the configured
[`query.timeout_ms`](models.md#the-configuration). `--timeout-ms N` overrides that
statement budget for one invocation; `0` disables the bound. Vector ingest uses
the unbounded form only for its paged source sweeps and source counts, while
query-time source lookups keep the interactive budget.

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

Because it is a real database, not a search box:

```sh
roca exec "SELECT source_agent, COUNT(*) AS sessions
           FROM sessions
           WHERE started_at LIKE '2026-07%'
           GROUP BY source_agent
           ORDER BY sessions DESC"
```

`roca playground` recovers with SQL plus a local FTS5 index with diacritic
folding; a plain `LIKE` fallback works before the index exists. Its configured
model supplies semantic interpretation at question time, while the checked SQL
retrieval stays exact and auditable. No usable provider, or SQL that cannot run,
falls back to literal search and says so in the result. `roca query` instead
uses the deterministic hybrid path described above.

## Read-only queries across machines

`roca remote` connects already-installed Roca instances through ordinary SSH.
SSH configuration owns host aliases, keys, agents, and authentication; La Roca
stores only the name-to-target registry in `~/.roca/remotes.json`. There is no
listener, daemon, sync protocol, or additional port.

```sh
roca remote add studio --ssh dev@studio.example
roca remote list
roca remote exec studio "SELECT layer, COUNT(*) AS n FROM memories GROUP BY layer"
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
and every named remote, loads those JSON result sets into a temporary SQLite
database as `r_local`, `r_<name>` and so on, adds an `origin` column, and runs a
generated `UNION ALL` outer `SELECT` there:

```sh
roca remote cross "SELECT source_agent, COUNT(*) AS sessions FROM sessions GROUP BY source_agent" \
  --on studio,laptop
```

The temporary database is exactly `:memory:`. Cross disables reconciliation
and call-history writes for the run and opens the local stores read-only, so it
writes to neither the local nor remote rocks. The data path is SQL and JSON
only; it performs no inference.

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

For facts whose ordering matters, use explicit SQL instead of asking ranking to
imply it. A session can fetch its latest project handoff exactly and then store
the next one:

```sh
roca exec "SELECT content, created_at FROM plugin_roca_ops.memories WHERE layer = 'handoff' AND project = '<project>' ORDER BY created_at DESC LIMIT 1"
roca store --layer handoff --project '<project>' --content "token refresh done, retry pending" --agent codex --model gpt-5
```
