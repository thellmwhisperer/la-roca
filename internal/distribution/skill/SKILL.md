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
`~/.roca/config.toml` with a recovery backup. Plain Enter keeps the factory
choice and uses the CLI's existing session without a La Roca login. Automation
that creates or selects a location must pass `--db-path`; non-terminal init
does not open the chooser or write model configuration.

## Shell commands

Data = `roca query`; human reading = `roca query --full`; raw SQL = `roca exec`.

```bash
roca query "who is Ana"                        # natural-language search
roca query --full "what happened with Y"       # add prose for human reading
roca query "what happened with Y" --json
roca query "ffmpeg patterns" --sql-only        # the SQL the model would run, without running it
roca exec "SELECT COUNT(*) AS memories FROM memories"  # run a gate-approved SELECT
roca store --layer discovery --content "FTS ranks by bm25, created_at only for time questions" --origin agent
roca doctor                                    # diagnosis + remedies
```

To change the answering model later without logging in, run
`roca model set <id>`. A provider login can also accept `--model <id>` when a
credential flow is actually needed. Both persist `models.<provider>.model` in
`~/.roca/config.toml`.

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
rows[1]{source,id,created_at,text}:
  memory,1,"2026-08-07 17:39:43","AXI output uses TOON rows, stable fields, and contextual help."
help[2]:
  - "Run `roca query \"what do we know about AXI output\" --json` for the complete result envelope"
  - "Run `roca query \"what do we know about AXI output\" --sql-only`, then `roca exec \"<SELECT>\" --max-chars 2000` to inspect or expand rows"
```

## MCP (shell-less agents)

Five tools, same service as the CLI: `roca_query`, `roca_sql`, `roca_exec`,
`roca_store`, `roca_health`. `roca_sql` is the shell-less form of
`query --sql-only` (the SQL without running it); `roca_exec` runs that SQL under
the gate. Install them with `roca mcp install <runtime>`.

## When to call what

| Situation | Action |
|---|---|
| Past work / people / "have we…" | `roca query "<question>"` |
| Programmatic parse | add `--json` |
| Inspect SQL first | `roca query --sql-only` then `roca exec` |
| Durable memory | `roca store --layer … --content …` |
| No shell | the MCP tools above |

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
- Use the layer filter deliberately: `handoff` for continuity and
  `feedback`/`pattern` for distilled lessons. Search coordination layers
  explicitly when tracing origins; ordinary knowledge search can skip them.

## Good

```bash
roca query "who is Ana"
roca query "what feedback do we have" --json
roca store --layer handoff --content "the ingest update left the gate in place" --origin agent
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
