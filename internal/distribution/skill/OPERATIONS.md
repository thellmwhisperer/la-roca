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

Data = `roca query`; human reading = `roca query --full`; investigation =
`roca explore`; raw SQL = `roca exec`.

```bash
roca query "who is Ana"                        # natural-language search
roca query --full "what happened with Y"       # add prose for human reading
roca explore --deep "format"                   # launch a one-word investigation probe
roca explore "rows"                            # follow one radius concept
roca query "what happened with Y" --json
roca query "ffmpeg patterns" --sql-only        # the SQL the model would run, without running it
roca exec "SELECT COUNT(*) AS memories FROM memories"  # run a gate-approved SELECT
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

`roca exec` runs exactly what `query --sql-only` prints, under the same
read-only gate; nothing that is not a SELECT reaches the database.

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
the gate. Install them with `roca mcp install <runtime>`.

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

Check whether the vector index exists before you search. The contract is
docs/vector.md: `~/.roca/plugins/roca-vector/state/completion.json` records
`finished_at` when the first pass finished. That file, with a non-empty
`finished_at`, is the index. Absent or unfinished means there is no index.
`features.vector = true` only unhides `roca vector`; it is not the index.
`roca vector query` refuses until the index is ready.

- **Index present: the hybrid loop is mandatory.** It is how the craft is
  done. Vector search (top-100), FTS census, SQL framing. Zero inference on
  that path; inference only at the end, by the reading agent, to narrate.
- **No index: `roca query` and `roca explore` are the complete working
  path.** They spend inference, and they deliver the result. Do not relegate
  them and do not wait. Invite the user to build the index (one laptop
  night, daily reward); the `roca-vector` skill owns install, progress, and
  maintenance. Point there. Do not depend on it.

## When to call what

| Situation | Action |
|---|---|
| Past work / people / "have we…" | Search craft: hybrid if the index exists, else `roca query "<question>"` |
| Researching a topic, not a point fact | Search craft: hybrid if the index exists, else `roca explore "<concept>"` |
| Cannot name the exact term | Hybrid loop when the index exists; otherwise explore, and invite the index |
| Answer looks stale / about today | `roca ingest`, then ask again |
| Programmatic parse | add `--json` |
| Inspect SQL first | `roca query --sql-only` then `roca exec` |
| Durable memory | `roca store --layer … --content … --agent … --model …` |
| Who wrote it / which model | ask by author, or store with `--model` |
| No shell | the MCP tools above |

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

Vector search finds the nearby rows. FTS censuses them. SQL frames them.
Zero inference on that path; inference only at the end, by the reading
agent, to narrate. `roca vector query` does no model inference; `roca exec`
does none either.

1. Search by meaning with `roca vector query "<first-person phrase or bare word>" k`.
   `k` is optional (default 10) and capped at 100. Hits print score, source
   family, and source id. Probing phrases in first person work better than
   meta-concepts: `roca vector query "names of people" 20` finds documents
   ABOUT names; `roca vector query "my boss is named" 20` finds the names.
2. Census those hits with deterministic FTS through `roca exec`: counts,
   dates, and word-boundary `MATCH`. Do not use `LIKE '%term%'`: it matches
   inside other words (`name` inside `rename`).
3. Frame the claim in SQL. Then the reading agent narrates from those rows.

Worked loop, names:

```bash
roca vector query "my boss is named" 20
roca exec "SELECT substr(COALESCE(e.human_timestamp, e.agent_timestamp), 1, 7) AS month, COUNT(DISTINCT e.id) AS exchanges, COUNT(DISTINCT e.session_id) AS sessions FROM (SELECT rowid AS source_id FROM exchanges_fts WHERE exchanges_fts MATCH 'ana') hits JOIN exchanges e ON e.id = hits.source_id GROUP BY month ORDER BY month"
```

Worked loop, concept:

```bash
roca vector query "exhaustion" 20
roca exec "SELECT COUNT(DISTINCT e.id) AS exchanges, COUNT(DISTINCT e.session_id) AS sessions, MIN(COALESCE(e.human_timestamp, e.agent_timestamp)) AS first_seen, MAX(COALESCE(e.human_timestamp, e.agent_timestamp)) AS last_seen FROM (SELECT rowid AS source_id FROM exchanges_fts WHERE exchanges_fts MATCH 'exhaustion') hits JOIN exchanges e ON e.id = hits.source_id"
```

Stop before the last reading if you only needed the map. `roca query --sql-only`
is a convenient natural-language, with-inference SQL compiler outside this zero-inference path;
use it only when that inference is intentional.

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

## Investigation method

This is the complete working path when there is no index. Purpose: reach a
verdict that is grounded in returned rows while learning the corpus terrain.
When you would otherwise fire three exploratory queries, fire one
`roca explore` instead and follow its probes.

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
6. Graduate to `roca query --sql-only` plus `roca exec` once the printed plans
   have shown the schema. Phrase by relation, not point fact: "what is my
   relationship with X" matches how conversations store knowledge better than
   "who did I work with at X", and rankings need an explicit `ORDER BY` on the
   volume column or the list shows the tail instead of the head.
7. End with a Verdict grounded in rows: state the claim, which row supports it,
   and what stayed unanswered. Cross-check the plausible before trusting it: a
   confident answer to a superlative or origin question ("the most", "the
   first time", "when did we decide") may be a naive count crowning its first
   match, so verify it against distilled memories or a second, differently
   phrased query.

When a bare FTS word would miss, take the search-craft branch: hybrid loop
if the index exists, otherwise keep exploring one concept at a time. Do
not stack synonyms.

## Operating craft

- Landing on a machine that is new to you, get up to speed from La Roca
  before asking the human anything: active projects and their volume from
  `sessions` analytics, and what the operator's agents already knew, since
  their memory and rule files land in the `user`, `feedback` and `project`
  layers at ingest. On a fresh install the `handoff` layer is empty until
  agents store the first one, so read the history, then write it yourself.
- Start project work with `roca_query("latest handoff for <project>")`. Ask for
  the current handoff protocol and follow it instead of freezing it here. After
  meaningful work, always store a handoff with branch, changes, state, next
  steps and blockers.
- Ask bare first: use one short concept and no hints. Hints can steer SQL to the
  wrong table; a typo can silently leave noise as the best match.
- With no index, reach for `roca explore` first; the investigation method
  above is the same discipline applied by hand with `query` when you need
  finer control than the probe gives you. With an index, the hybrid loop is
  mandatory; do not start with explore as a substitute.
- Widen deliberately: say "search the whole corpus (conversations, thinking,
  memories, sessions)", request OR between terms and raise limits consciously.
- For counts or rankings, name `sessions` or `exchanges`, where the mass lives;
  do not aim analytics at the smaller set of curated memories.
- For origins, compile with `roca_sql`, run with `roca_exec` using
  `ORDER BY timestamp ASC`, then inspect the first matching session and its
  surrounding exchanges.
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
   `internal/ingest/parsers/testdata/conformance/`.
2. Add one parser file implementing `Detect` and `Parse`; declare whether its
   output belongs to the conversation corpus, the distilled-memory store, or
   both. File encoding is an implementation detail, never the destination.
3. Add its registry line with the product's canonical harness. The harness is
   known by the ingestion surface, never discovered in JSON; keep the model
   exactly as the source recorded it and empty when the source recorded none.
   Then run
   `go test ./internal/ingest/parsers -run TestRegisteredParsersConform`, then
   `ROCA_REAL_HARVEST=1 go test -v ./internal/ingest/parsers -run TestRegisteredParsersHarvestPresentAgentStores`
   on a machine where that agent is installed (the smoke reads private stores, so
   it stays out of the shared gate), `go test ./internal/ingest/parsers` and
   `make check`.
4. Open a pull request with the synthetic fixture, parser, and registry line.
   Include the real-harvest yield summary; do not attach or quote the real source
   file in the pull request.

## Good

```bash
roca query "who is Ana"
roca explore "what we know about retention"
roca query "what feedback do we have" --json
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
