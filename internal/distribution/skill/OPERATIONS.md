---
name: roca-operations
description: >
  The craft of searching with La Roca, with or without a vector index.
  Must-read on install. Load when the user references past work, asks
  "who is X", "what happened with Y", "have we done this before", wants
  a memory stored, or wants to investigate a topic across the agents'
  history.
---

# La Roca

Local SQLite memory of what agents leave on disk. Query in natural language and
the answering model turns the question into SQL over the schema; investigate
concepts with grounded exploration; store memories that last. No network
required after install beyond the model provider itself.

Before first use, run `roca init` in a terminal. With no home database it asks
`new` or `adopt` with no default. `adopt` then asks you to type the source path,
copies that database into `~/.roca`, and leaves the original untouched; `new`
creates an empty database and indexes detected agent sources. If the home
database already exists, init asks to keep or explicitly reinitialize it.
It then lists models by their detected CLI or local Ollama origin, asks for the
model first, resolves its harness, and confirms the pair before editing
`~/.roca/config.toml`, backing up an existing file when it changes. Plain Enter
keeps the factory choice and uses the CLI's existing session without a La Roca
login. Automation that creates or selects a location must pass `--db-path`;
non-terminal init does not open the chooser or write model configuration.

## Staying current

Two kinds of freshness, two commands:

- **Binary**: `roca version` says what is installed; `roca update`
  self-updates to the latest release. Releases ship often, so if behavior looks
  outdated or a documented feature is missing, update before debugging. The
  first run after an update may adopt new schema columns or rebuild derived
  indexes, which can take a couple of minutes on a large corpus: that is work,
  not a hang, and source rows are never rewritten.
- **Data**: the database only knows what the last ingest read. `roca ingest`
  reads every agent source and normalizes what changed; it is incremental, so
  routine runs are cheap. If a question is about today's sessions and the
  answer looks stale, ingest first, then ask again. Its per-agent coverage
  report says what was seen, parsed, skipped and why; trust it over guessing.

## Shell commands

Agents write SQL and run it: `roca exec`. Humans may read with
`roca query --full`. `roca query` and `roca explore` are last resort for
agents, only when the question cannot be expressed as SQL. Agents never
pass `--full`.

```bash
roca exec "SELECT COUNT(*) AS memories FROM memories"
roca query "who is Ana"                        # last resort: cannot write the SQL
roca query --full "what happened with Y"       # human reading only; agents never pass --full
roca explore --deep "format"                   # last resort investigation
roca explore "rows"
roca query "what happened with Y" --json
roca query "ffmpeg patterns" --sql-only        # compile SQL when stuck, then exec it
roca query --databases all "who is Ana"        # widen to every attached database
roca store --layer discovery --content "FTS ranks by bm25, created_at only for time questions" --origin agent --agent codex --model gpt-5
roca ingest                                    # refresh the corpus from every agent source
roca doctor                                    # diagnosis + remedies
```

To verify that the configured provider session answers without changing any
configuration, run `roca model check [provider]`. To change the answering model,
run `roca model set [id]`; with no ID, an interactive terminal chooses from the
first provider's catalogue, while `roca model set <provider>` chooses from that
provider's catalogue. An unknown ID is refused, and a successful set
writes only `models.<provider>.model` without changing provider order. La Roca
does not handle authentication or store its secrets.

`roca exec` runs a gate-approved SELECT. Nothing that is not a SELECT
reaches the database. When you cannot write the SQL, `query --sql-only`
compiles it; then you exec that SELECT. `--full` is a human reading of
rows; agents narrate from the rows themselves.

## Which databases a question sees

`roca query`, `roca explore`, and `roca vector query` default to the corpus
database when it is attached, together with the core compatibility store where
that command can use it. Historical corpus study should not drag in ops
handoffs, cron jobs, or other federated stores. Without an attached corpus, the
default deterministic route is core alone.

Pass `--databases` to select exactly the named databases when a question spans
them: `--databases corpus,ops`, `--databases cron,corpus`, or `--databases all`
for every attached database. The same explicit scope applies to vector
discovery and deterministic FTS/SQL framing. Include `core` explicitly when a
named deterministic set needs it. Unknown names fail and list what is attached.
Routing does not guess relevance. It does not auto-select a plugin from the
wording.

The SQL seat sees tables only for the selected databases. It also sees an
inventory of the other attached names, but not their tables, when they are held
back from that pass. If the first pass returns zero rows (including
`model_unavailable`), or the reading seat replies exactly `WIDEN`, it takes a
second SQL pass. That pass adds the remaining attached databases. It does not widen after
`invalid_sql`, `execution`, or `timeout`. `--sql-only` stays on the first pass.

## Default row output

Rows use the same compact TOON shape as other AXI tools. The route narration
stays above the data; deterministic next commands follow it. Text fields keep a
bounded preview. Add `--json` when a program needs the unchanged full envelope.

```text
$ roca query "what do we know about AXI output"
route model
SQL · provider ollama · model qwen3.5:4b · 4 ms
search · 1 ms
rows[1]{source,id,author,created_at,text}:
  memory,1,"codex/gpt-5 via cli","2026-08-07 17:39:43","AXI output uses TOON rows, stable fields, and contextual help."
help[2]:
  - "Run `roca query \"what do we know about AXI output\" --json` for the complete result envelope"
  - "Run `roca query \"what do we know about AXI output\" --sql-only`, then `roca exec \"<SELECT>\" --max-chars 2000` to inspect or expand rows"
```

## MCP (shell-less agents)

Six tools, same service as the CLI: `roca_query`, `roca_explore`, `roca_sql`,
`roca_exec`, `roca_store`, `roca_health`. `roca_explore` has full CLI parity:
plain mode and `deep: true` both return the prose investigation and generated
SQL, with the deep mission mapping the complete terrain and proposing probes.
`roca_sql` is the shell-less form of
`query --sql-only` (the SQL without running it); `roca_exec` runs that SQL under
the gate. The `roca_query`, `roca_explore`, and `roca_sql` tools accept the same
comma list or `all` in their `databases` argument. Install them with
`roca mcp install <runtime>`.

## Provenance: who wrote what

Every ingested row carries its author identity: the harness (claude, codex,
hermes, ...) and the source model that produced it. Ask for it directly in
natural language: "what did deepseek conclude about X", "which model wrote the
most sessions in July". For memories you store, carry identity too:

- `roca store --model <id>` (and `--agent <harness>`) states the writing
  identity explicitly;
- `roca hooks install claude` installs the Claude Code signing hook, so every
  store from that runtime is stamped without remembering the flags.

Over MCP, authorship comes from the session itself and cannot be spoofed by
arguments.

## Search craft

Write the SQL yourself against the semantic catalog (`roca-semantica`) and
run it with `roca exec`. That is the craft. Anything that spends inference
is last resort.

Check whether the vector sidecars exist before you search. The contract is
docs/vector.md: `~/.roca/plugins/roca-vector/state/completion.json` records
`finished_at` and `exit_status` for the first pass. Declared sidecars are ready
only when `finished_at` is non-empty and `exit_status == 0`. Otherwise take the
no-index, exec-first branch.
`features.vector = true` only unhides `roca vector`; it is not the index.
`roca vector query` refuses when no declared sidecar is ready. Once at least one
sidecar exists, a selected database with no declaration or no ready sidecar
emits a notice and keeps exactly its existing FTS/SQL behavior.

- **Declared sidecars present: the hybrid loop is mandatory and federated.**
  Vector search (top-100), FTS census, SQL framing through `roca exec`, across
  the selected federated databases. Zero inference on that path; inference
  only at the end, by the reading agent, to narrate.
- **No index: `roca exec` is the complete working path.** Invite the user to build the index
  (one laptop night, daily reward); the `roca-vector` skill owns install,
  progress, and maintenance. Point there. Do not depend on it.
- **`roca query` and `roca explore` are last resort.** Use them only when
  you cannot express the question as SQL. Agents never pass `--full`.

Handoffs and ops live on the ops database. Use the qualified handoff query under
Deterministic patterns; natural-language questions that need ops must select it
as described under Which databases a question sees.

## When to call what

| Situation | Action |
|---|---|
| Past work / people / "have we…" | Write SQL and `roca exec`; hybrid first if the index exists |
| Researching a topic, not a point fact | Write SQL and `roca exec`; hybrid first if the index exists |
| Cannot name the exact term | Hybrid loop when the index exists; otherwise exec with FTS MATCH |
| Cannot express it as SQL | last resort: `roca query` or `roca explore`; never `--full` |
| Answer looks stale / about today | `roca ingest`, then ask again |
| Programmatic parse | add `--json` |
| Stuck on the SQL | `roca query --sql-only` then `roca exec` |
| Durable memory | `roca store --layer … --content … --agent … --model …` |
| Who wrote it / which model | ask by author, or store with `--model` |
| Project start | the handoff one-liner below, through `roca exec` |
| No shell | `roca_exec`; `roca_query` / `roca_explore` last resort |

## Plugins

La Roca is a federating kernel: capabilities ship as plugins that own their
own SQLite databases and declare them in a `plugin.json` manifest; the kernel
attaches them read-only and folds their tables into natural-language search.

- Use one: follow `roca plugin install --help`, then run `roca plugin update
  <name>` / `roca plugin uninstall <name>`. The experimental surface requires
  `features.plugins=true`.
- Build one: a manifest plus one executable is a complete plugin. Follow the
  quickstart in the repository's `docs/plugins.md`; start from its minimal
  example and grow, do not hand-roll the packaging.

## Hybrid loop

When the index exists, this loop is mandatory. It is how the craft is done.
Operator setup lives in the `roca-vector` skill and docs/vector.md.

Vector search finds the nearby rows across declared, selected federated
databases. FTS censuses them in their owning databases. SQL frames them there.
This three-step loop is the shipped RRF hybrid.
Zero inference on that path; inference only at the end, by the reading
agent, to narrate. `roca vector query` does no model inference; `roca exec`
does none either.

1. Search by meaning with `roca vector query --databases <scope> "<first-person phrase or bare word>" 100`.
   Omit `--databases` only when the default corpus scope is intentional:
   `roca vector query "<first-person phrase or bare word>" 100`. The
   mandatory loop always requests the top 100 hits. Hits print score, database,
   source table, and source id; one query can mix a declared plugin hit with a
   corpus hit. Probing phrases in first person work better than meta-concepts:
   `roca vector query "names of people" 100` finds documents ABOUT names;
   `roca vector query "my boss is named" 100` finds the names.
2. Census those hits with deterministic FTS through `roca exec`, using each
   hit's database alias from `roca-semantica`: counts, dates, and word-boundary
   `MATCH`. Do not use `LIKE '%term%'`: it matches inside other words (`name`
   inside `rename`). A selected database without a `Vector:` declaration joins
   here through its unchanged FTS path; it cannot contribute a vector hit.
3. Frame each claim with SQL against the hit's owning tables, combine database
   frames only when the question needs it, and narrate from those rows.

Federated discovery, one result list when the selected sidecars share a model:

```bash
roca vector query --databases corpus,ops "we decided to preserve the evidence" 100
```

Worked loop, names:

```bash
roca vector query "my boss is named" 100
roca exec "SELECT substr(COALESCE(e.human_timestamp, e.agent_timestamp), 1, 7) AS month, COUNT(DISTINCT e.id) AS exchanges, COUNT(DISTINCT e.session_id) AS sessions FROM (SELECT rowid AS source_id FROM exchanges_fts WHERE exchanges_fts MATCH 'ana') hits JOIN exchanges e ON e.id = hits.source_id GROUP BY month ORDER BY month"
```

Worked loop, concept:

```bash
roca vector query "exhaustion" 100
roca exec "SELECT COUNT(DISTINCT e.id) AS exchanges, COUNT(DISTINCT e.session_id) AS sessions, MIN(COALESCE(e.human_timestamp, e.agent_timestamp)) AS first_seen, MAX(COALESCE(e.human_timestamp, e.agent_timestamp)) AS last_seen FROM (SELECT rowid AS source_id FROM exchanges_fts WHERE exchanges_fts MATCH 'exhaustion') hits JOIN exchanges e ON e.id = hits.source_id"
```

Stop before the last reading if you only needed the map. `roca query --sql-only`
and `roca_sql` are with-inference SQL compilers outside this zero-inference path.
Use them only as a last resort when you cannot write the SELECT yourself.

Caveats measured on real use:

- `k` max is 100. A larger `k` errors (`k must be between 1 and 100`) before
  any JSON body.
- A vector search can return several exchange sources from one session. Judge
  breadth by grouping on session id or `COUNT(DISTINCT e.session_id)` before
  interpreting the result.
- Query deduplicates chunks by source identity. For an exact census, count the
  family's identity in FTS/SQL, such as `COUNT(DISTINCT e.id)` for exchanges;
  do not treat vector hits or distinct sessions as exact occurrence counts.
- Two signal classes: presence (the topic is nearby) versus intention
  (someone decided or promised). Vector answers presence; SQL frames
  intention.
- Mixed-model sidecars are returned in separate per-database groups with a
  notice. Their scores are not comparable; census and frame each group in its
  own database.

## Deterministic patterns

FTS MATCH plus a join (word-boundary; never `LIKE '%term%'`):

```bash
roca exec "SELECT substr(COALESCE(e.human_timestamp, e.agent_timestamp), 1, 7) AS month, COUNT(DISTINCT e.id) AS exchanges, COUNT(DISTINCT e.session_id) AS sessions FROM (SELECT rowid AS source_id FROM exchanges_fts WHERE exchanges_fts MATCH 'ana') hits JOIN exchanges e ON e.id = hits.source_id GROUP BY month ORDER BY month"
```

Counts by month, same shape, swap the MATCH term.

Handoff one-liner, by layer and project:

```bash
roca exec "SELECT content, created_at, project FROM memories WHERE layer = 'handoff' AND project = '<project>' ORDER BY created_at DESC LIMIT 1"
```

Handoffs live on ops. The unqualified compatibility query above remains valid;
when composing a query directly against the ops attachment, its qualified table
is `plugin_roca_ops.memories`.

## Investigation method

Last resort, only when you cannot write the SQL. Purpose: reach a verdict
that is grounded in returned rows while learning the corpus terrain.
When you would otherwise fire three exploratory queries, fire one
`roca explore` instead and follow its probes. Never pass `--full`.

1. Declare the purpose in one line before touching anything.
2. Launch the first probe with `roca explore --deep "<one bare word>"`. Use a
   single bare word: no hints and no phrases.
3. Read the terrain, not just the answer: inspect sources, dates, terms,
   noise, and negative space.
4. Work the radius with plain `roca explore`, one concept per query: a synonym,
   adjacent frame, entity, or era. Never stack five terms; FTS ANDs them and
   commonly produces zero rows.
5. Widen only deliberately and say so out loud: use explicit OR, search the
   whole corpus, or raise limits consciously.
6. If you still cannot write the SELECT after the printed plans have shown the
   schema, use the last-resort `roca query --sql-only` compiler, then `roca exec`.
   Phrase by relation, not point fact: "what is my relationship with X" matches
   how conversations store knowledge better than "who did I work with at X",
   and rankings need an explicit `ORDER BY` on the volume column or the list
   shows the tail instead of the head.
7. End with a Verdict grounded in rows: state the claim, which row supports it,
   and what stayed unanswered. Cross-check the plausible before trusting it: a
   confident answer to a superlative or origin question ("the most", "the
   first time", "when did we decide") may be a naive count crowning its first
   match, so verify it against distilled memories or a second, differently
   phrased query.

When a bare FTS word would miss, take the search-craft branch: hybrid loop
if the index exists, otherwise write a broader MATCH or OR and exec it.
Do not stack synonyms.

## Operating craft

- Landing on a machine that is new to you, get up to speed from La Roca
  before asking the human anything: active projects and their volume from
  `sessions` analytics, and what the operator's agents already knew, since
  their memory and rule files land in the `user`, `feedback` and `project`
  layers at ingest. On a fresh install the `handoff` layer is empty until
  agents store the first one, so read the history, then write it yourself.
- Start project work with the unqualified handoff one-liner under Deterministic
  patterns. Ask for the current handoff protocol and follow it instead of
  freezing it here. After meaningful work, always store a handoff with branch,
  changes, state, next steps and blockers.
- Ask bare first: use one short concept and no hints. Hints can steer SQL to the
  wrong table; a typo can silently leave noise as the best match.
- Write SQL and `roca exec` first. `roca query` and `roca explore` are last
  resort. With an index, the hybrid loop is mandatory; do not start with
  explore as a substitute.
- Widen deliberately: say "search the whole corpus (conversations, thinking,
  memories, sessions)", request OR between terms and raise limits consciously.
- For counts or rankings, name `sessions` or `exchanges`, where the mass lives;
  do not aim analytics at the smaller set of curated memories.
- For origins, write the SELECT yourself with `ORDER BY timestamp ASC`, run it
  through `roca exec`, then inspect the first matching session and its
  surrounding exchanges. Use an inference compiler only if you cannot write
  that SELECT.
- Rows are the truth; prose is a reading. Verify claims against returned rows
  and say plainly when they do not answer the question.
- CLI and MCP split by job: with a shell, dig with the CLI, which composes
  with pipes, files and `--max-chars` for fast iteration; write long or
  quote-heavy memories with `roca_store` over MCP, whose structured params
  avoid shell escaping. When shell permissions block a CLI call, the MCP
  tools are the frictionless path, not a fallback.
- Authorship is automatic from MCP `clientInfo`. On CLI, always pass
  `--agent <harness> --model <model>`; environment and ancestry detection are a
  conservative bonus and ambiguous evidence is stored as `unknown`. `agent`,
  `model` and `surface` are refused inside metadata: they are the identity card.
- Use the layer filter deliberately: `handoff` for continuity and
  `feedback`/`pattern` for distilled lessons. Search coordination layers
  explicitly when tracing origins; ordinary knowledge search can skip them.

## Unsupported agent self-onboarding

If this runtime's session files are not supported, teach La Roca through the
repository contribution kit instead of converting or uploading the user's
history.

**Never copy real conversation data into a fixture.** Inspect only enough of
your own session-file structure to identify stable ownership markers and the
fields needed for normalization. Then fabricate the same structure with
invented identities, paths, prompts, answers, timestamps, and metadata. Remove
tokens, account identifiers, repository names, and every other piece of user
data.

0. Before writing a fixture, measure a populated real store read-only: record
   its path layout, file count, byte size, primary candidate sizes and record-type
   counts, and check whether secondary surfaces add unique records. Put only
   those aggregate measurements in the pull request under **Real-store
   measurement**. A fixture not traceable to that measured shape is invalid; if
   no populated real store is available, do not guess the format.
1. Read `docs/agent-parsers.md` and copy a synthetic worked-example folder under
   `pkg/parsers/testdata/conformance/`.
2. Add one parser file implementing `Detect` and `Parse`; declare whether its
   output belongs to the conversation corpus, the distilled-memory store, or
   both. File encoding is an implementation detail, never the destination.
3. Add its registry line with the product's canonical harness. The harness is
   known by the ingestion surface, never discovered in JSON; keep the model
   exactly as the source recorded it and empty when the source recorded none.
   Then run
   `go test ./pkg/parsers -run TestRegisteredParsersConform`, then
   `ROCA_REAL_HARVEST=1 go test -v ./pkg/parsers -run TestRegisteredParsersHarvestPresentAgentStores`
   on a machine where that agent is installed (the smoke reads private stores, so
   it stays out of the shared gate), `go test ./pkg/parsers` and
   `make check`.
4. Open a pull request with the synthetic fixture, parser, and registry line.
   Include the real-harvest yield summary; do not attach or quote the real source
   file in the pull request.

## Good

```bash
roca exec "SELECT COUNT(*) AS memories FROM memories"
roca store --layer handoff --content "the ingest update left the gate in place" --origin agent --agent claude --model sonnet
```

## Bad

```bash
# Inventing answers from model memory instead of querying La Roca
# Writing free-form SQL that is not a SELECT (the gate refuses it)
# Storing secrets, tokens, or raw credentials
# Re-storing the same memory on every turn without checking first
# Asking about today's work without ingesting first
```

## Layers

The real layers: pick the narrowest true one: `discovery`, `pattern`, `pill`,
`feedback`, `handoff`, `project`, `user`, `question`, `review`, `issue`.
Handoffs stay searchable (session continuity); `question`, `review` and `issue`
are private messaging and do not surface in term search.

Store accepts only a layer in the live registry and lists the registered names
when it refuses one. Add an intentional custom layer with
`roca layers add NAME`, or repair existing drift with
`roca layers migrate FROM REGISTERED-TO`. `roca doctor` prints the exact
registration command when runtime data contains an unknown layer.
