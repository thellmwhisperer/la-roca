---
name: roca
description: >
  Use La Roca, the local memory for agent fleets. Load when the user
  references past work, asks "who is X", "what happened with Y", "have we done
  this before", or wants a memory stored.
---

# La Roca

Local SQLite memory of what agents leave on disk. Query in natural language and
the answering model turns the question into SQL over the schema; store memories
that last. No network required after install beyond the model provider itself.

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

## Semantic-first retrieval

Use semantic retrieval for questions about concepts, similar past work, or
memories expressed with different words. Do not present a literal keyword
rescue as if it were a semantic answer.

When the optional local `roca-vector` plugin is installed and ready, use it as
the candidate retriever:

```bash
roca vector --json query "<bare concept>" 8
```

For a non-default core database, add `--db-path /path/to/roca.db` after
`vector`.

The vector result is a locator and relevance signal, not final evidence. For
each useful candidate, recover the live source text through the core query or
`roca exec`, then inspect enough surrounding context to establish what the
source actually says. Keep available source kind, stable source id, layer,
project, date, and provenance with the finding; a missing provenance value
means the source said nothing.

If the vector route is unavailable, say that semantic retrieval is unavailable
and name the missing route. Use exact SQL or literal search only when the user
explicitly asks for a literal lookup, and label that result as exact rather
than semantic. Never silently downgrade a conceptual question to keyword
matching.

The vector route requires explicit local setup: `features.plugins = true`,
`features.vector = true`, an installed package, and its local embedding model.
It must remain optional and local. See the [semantic retrieval and evidence
workflow](../../../docs/semantic-retrieval.md) for the complete setup,
degradation, and evidence contract.

## Evidence-first investigation

For a question that needs more than one remembered fact, apply the [semantic
retrieval and evidence workflow](../../../docs/semantic-retrieval.md): recover
source context, preserve the stable source id, separate observations from
interpretation, and end with evidence, contradictions, coverage limits, and
the next probe.

For project, decision, and agent questions, prefer the narrowest true layer
and project filter. Store a durable handoff or decision only after the result
has been checked against its source rows.

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

## When to call what

| Situation | Action |
|---|---|
| Past work / people / "have we…" | `roca query "<question>"` |
| Conceptual investigation | Follow Semantic-first retrieval; report an unavailable vector route before using any exact fallback |
| Exact or terrain investigation | `roca explore --deep "<one bare word>"`, then plain radius explores |
| Programmatic parse | add `--json` |
| Inspect SQL first | `roca query --sql-only` then `roca exec` |
| Durable memory | `roca store --layer … --content … --agent … --model …` |
| No shell | the MCP tools above |

## Investigation method

Purpose: reach a verdict that is grounded in returned rows while learning the corpus terrain.

1. Declare the purpose in one line before touching anything.
2. For a conceptual question, follow Semantic-first retrieval above. Use
   `roca explore --deep "<one bare word>"` only for exact or terrain
   investigation, with a single bare word and no hints or phrases.
3. Read the terrain, not just the answer: inspect sources, dates, vocabulary,
   noise, and negative space.
4. Work the radius with plain `roca explore`, one concept per query: a synonym,
   adjacent frame, entity, or era. Never stack five terms; FTS ANDs them and
   commonly produces zero rows.
5. Widen only deliberately and say so out loud: use explicit OR, search the
   whole corpus, or raise limits consciously.
6. Graduate to `roca query --sql-only` plus `roca exec` once the printed plans
   have shown the schema.
7. End with a Verdict grounded in rows: state the claim, which row supports it,
   and what stayed unanswered.

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
roca query "what feedback do we have" --json
roca store --layer handoff --content "the ingest update left the gate in place" --origin agent --agent claude --model sonnet
```

## Bad

```bash
# Inventing answers from model memory instead of querying La Roca
# Writing free-form SQL that is not a SELECT (the gate refuses it)
# Storing secrets, tokens, or raw credentials
# Re-storing the same memory on every turn without checking first
```

## Layers

The real layers — pick the narrowest true one: `discovery`, `pattern`, `pill`,
`feedback`, `handoff`, `project`, `user`, `question`, `review`, `issue`.
Handoffs stay searchable (session continuity); `question`, `review` and `issue`
are private messaging and do not surface in term search.
