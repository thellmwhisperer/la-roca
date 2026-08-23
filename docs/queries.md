# Queries, explore, and the read-only gate

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

`roca query` is hybrid search with no answering-model inference: it selects
rare full-text terms, embeds the question plus static question templates when
a vector index exists, fuses the two lists with RRF, and labels which legs
found each hit. Without a vector index the same command runs full-text alone.
`--top N` (default 10) and `--require-both` keep only dual-confirmed hits.
`--json` returns the complete machine envelope. Questions must contain text and
have a generous 1000-character cap on both CLI and MCP query surfaces.

`roca playground` is the human room: it compiles a question into one checked
`SELECT`, `--sql-only` compiles without executing, and `--full` adds a prose
reading of the rows. `roca exec` runs your own `SELECT` through the same
read-only gate.

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

## One answer, two readers

Every query serves both audiences. Your agent gets the rows; you get the
prose with `--full`:

<details open>
<summary><strong>What your agent sees (default): TOON format, for token efficiency and a better agent experience</strong></summary>

```text
$ roca query "have I fixed a stale lock error before"
SQL · provider codex · model gpt-5.6 · 2.9 s / search · 3 ms
rows[2]{source,created_at,text}:
  exchange,"2026-06-14 23:41:02","fixed: stale .lock left by a killed run; rm .ingest.lock and rerun with --resume"
  memory,"2026-06-15 00:02:19","Pattern: a killed ingest leaves .ingest.lock behind; delete it before blaming the parser"
```

</details>

<details>
<summary><strong>What you see with <code>--full</code>: concise human prose</strong></summary>

```text
$ roca query --full "have I fixed a stale lock error before"
SQL · codex · gpt-5.6 · 2.9 s / search · 3 ms / answer · ollama · gemma4:12b · 11.4 s

Yes, twice, and both times it was the same trap. On 14 June at 23:41 you
fixed it live: a killed run had left .ingest.lock behind, and the cure was
rm .ingest.lock followed by a rerun with --resume. The next morning you
stored the lesson as a pattern: a killed ingest always leaves its lock file
behind, so delete it before blaming the parser.
```

The second inference reads only the rows and writes the answer. It can be a
local model on your machine: make the query smart so the reader can be
cheap, local, and secure.

</details>

The prose examples below use an optional Codex-to-Ollama split; without an
explicit `models.interpret_order`, the provider that writes the SQL also reads
the rows.

```text
$ roca query --full "which model do I have real chemistry with, and which one just gets the job done"
SQL · codex · gpt-5.6 · 3.4 s / search · 4 ms / answer · ollama · gemma4:12b · 12.1 s

Claude is the passionate one: three times the praise and three times the
cursing of anyone else, and you always come back. Codex is the contractor:
half the anger, a third of the joy, and the only one you trust overnight
("going to sleep, I expect both PRs green by morning"). And the one you
cannot work with lately is qwen-0.8b: four abandoned sessions in a row
without a single kind word.
```

```text
$ roca query "have I fixed a stale lock error before"
SQL · provider codex · model gpt-5.6 · 2.9 s / search · 3 ms
rows[2]{source,created_at,text}:
  exchange,"2026-06-14 23:41:02","fixed: stale .lock left by a killed run; rm .ingest.lock and rerun with --resume"
  memory,"2026-06-15 00:02:19","Pattern: a killed ingest leaves .ingest.lock behind; delete it before blaming the parser"
```

```text
$ roca query "the ffmpeg one-liner that extracted frames for verification"
rows[1]{source,created_at,text}:
  exchange,"2026-07-29 18:05:33","ffmpeg -ss 2 -i out.mp4 -frames:v 1 -q:v 3 frame.jpg   # verify before delivering"
```

```text
$ roca query "what did we decide about the retention window"
rows[2]{source,created_at,text}:
  memory,"2026-08-02 21:14:09","Decision: operational logs keep 30 days, in dated streams"
  exchange,"2026-08-02 21:02:44","30 days and out. I do not want eternal logs."
```

The answers are already in your logs, with the rows to prove them. Questions
that history can settle include:

- Which sessions went well, and which one wasted an evening?
- What do you keep re-explaining to every new session?
- Which model is fastest at fixing tests? Which one writes the best plans?
- Which model do you actually have fun working with, and which one can you
  simply not work with?
- Which harness works best for which kind of work?

A session starts by asking for the latest handoff and ends by storing one:

```sh
roca query "latest handoff for this project"
roca store --layer handoff --content "token refresh done, retry pending" --agent codex --model gpt-5
```

The longer model, repair, and routing contracts live in
[Model providers](models.md). MCP tools share this gate:
[The MCP plug](mcp.md).
