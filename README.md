<p align="center"><img src="docs/assets/banner.jpg" alt="La Roca: glowing agent monoliths feeding golden roots into one shared bedrock" width="100%"></p>

# La Roca

**Everything your agents ever did. Anything you ever prompted.**
**One question away.**

The AI memory system that is a real database. Any agent, any model, one
continuous line of work. The archaeology of why things are the way they
are, one query away.

One tool, zero dependencies, 100% local. SQLite + exact SQL + optional
local semantic search. CLI + MCP.

[![CI](https://github.com/thellmwhisperer/la-roca/actions/workflows/ci.yml/badge.svg)](https://github.com/thellmwhisperer/la-roca/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

https://github.com/user-attachments/assets/f27d377b-e4ad-4c59-beb6-86dd02af4f84

Your agents forget everything they write to disk (thinking blocks,
exchanges, tool calls...). La Roca makes it easy for your agents to query
that data, and teaches them how as they go:

- every answer says what you got and which command comes next
- every error names its fix
- the shipped skills carry the craft

An agent learns La Roca by using La Roca. CLI and MCP.

## Let your agent drive

Do not read a manual. Tell your agent:

> Install https://github.com/thellmwhisperer/la-roca and learn to use it.

The repository teaches your agent the rest: [`AGENTS.md`](AGENTS.md) tells
it what this is, how to install it, and how to search well. Three commands
later it is answering questions about everything your agents ever did:

```text
$ curl -fsSL https://raw.githubusercontent.com/thellmwhisperer/la-roca/main/install.sh | sh
$ roca init
$ roca query "what did we decide about the retention window"
search hybrid · engines fts,vector · 24 ms
terms[2]: retention, window
rows[1]{rank,source,legs,consensus,snippet}:
  1,corpus.memories.202,vector+fts,true,"30 days and out. I do not want eternal logs."
```

Init installs the skills into every runtime it detects. If your runtime was not
detected, use `roca skill install codex` (or `claude`, `opencode`, `pi`, or
`hermes`) as a fallback.

If an already signed-in agent CLI is on `PATH` (`claude`, `codex`, ...),
no login of any kind is needed: La Roca borrows the plan you already pay
for. Supported on macOS, Linux, and native Windows.
[Install details and the full init flow.](docs/lifecycle.md)

## What you can do

### Settle questions your logs already answered

> **You:** which model do I actually have chemistry with?
>
> **Your agent:** Claude is the passionate one: three times the praise and
> three times the cursing of anyone else, and you always come back. Codex
> is the contractor: half the anger, a third of the joy, and the only one
> you trust overnight. Want the rows behind that?

The bug you know you fixed once, the perfect one-liner an agent wrote for
you in July, yesterday's decision with the conversation that made it:
[one question away](docs/queries.md), always with the rows as proof.

### Investigate your corpus

Everything above works from minute one: exact SQL and full-text search need
nothing extra. The full experience begins when you index your corpus: one
model download, one background build, and your agents also search by
meaning. Search finds what you already know how to name; investigation
finds the rest:

> **You:** find that authentication rewrite we abandoned
>
> **Your agent:** your sessions never call it that. They call it "the
> passport mess": 41 mentions across 3 projects, first on 11 February,
> dropped in April after the token bug. Want the month-by-month arc?

Underneath, the
[hybrid loop](internal/distribution/skill/OPERATIONS.md#hybrid-loop): vectors
find nearby rows, full text counts them, SQL frames them. No model invents a
single row.

### Give your whole fleet one memory

A session starts by asking for the latest handoff and ends by storing one.
The Claude session you open today knows what Codex did last night, without
you re-explaining the project. Every agent reads and writes the same
database, from the shell or through [MCP](docs/mcp.md).

### Distill what repeats

Patterns in your history become skills that travel back to every agent. A
regular skill is a snapshot of a tool; a skill distilled from La Roca comes
with its whole story: the how, the why, and the failed attempts behind the
final answer. [Skills and distillation.](docs/lifecycle.md#skills)

### Drop to exact SQL whenever you want

Because it is a real database, not a search box:

```sh
roca exec "SELECT source_agent, COUNT(*) FROM sessions
           WHERE started_at LIKE '2026-07%' GROUP BY 1 ORDER BY 2 DESC"
```

`roca query` searches with deterministic full-text and optional vector fusion.
`roca playground` compiles a question into one checked `SELECT`, while
`roca explore` turns that model-backed machinery into a guided investigation.
[Queries, explore, and the read-only gate.](docs/queries.md)

### Compare rocks across machines

Register an SSH target, run the same gate-approved `SELECT` there, or scatter
one `SELECT` across local and remote rocks into a temporary in-memory SQLite:

```sh
roca remote add studio --ssh dev@studio.example
roca remote exec studio "SELECT COUNT(*) AS sessions FROM sessions"
roca remote cross "SELECT source_agent, COUNT(*) AS sessions FROM sessions GROUP BY 1" --on studio
```

SSH configuration owns authentication. La Roca opens no port and adds no sync
or daemon; remote data calls are plain `ssh <target> roca ... --json` and remain
read-only. [Remote query details and exit codes.](docs/queries.md#read-only-queries-across-machines)

## Private by construction

Everything runs on your machine: static binaries, SQLite under `~/.roca`,
zero network in the ingest path. Models see your schema and at most ten
result rows, never the database. Go fully local with Ollama and nothing
leaves at all. [The full privacy contract.](docs/operations.md)

## What it reads

Your coding agents, plus your ChatGPT and Claude data exports: incremental,
idempotent, read-only against live stores.
[Every source in detail.](docs/ingest.md)

## How it works

- **One normalized schema.** Sessions, exchanges, thinking, tool calls, and
  curated memory layers, the same shape for every runtime.
- **Query is deterministic hybrid retrieval.** Rare-term FTS and optional local
  vectors fuse into labeled evidence without an answering-model pass.
- **The playground keeps model-written SQL.** With `--full`, the first model
  sees the schema and writes one `SELECT`; the second sees only result rows.
  Either can be local.
- **Extensible.** [Plugins](docs/plugins.md) federate your own SQLite
  databases into the same query surface: a checksummed package, one consent
  screen, and your team's sources answer next to the corpus.

[Architecture](docs/architecture.md) has the longer story.

## Going deeper

[The docs index](docs/README.md): [models](docs/models.md) ·
[MCP](docs/mcp.md) · [vector](docs/vector.md) · [ingest](docs/ingest.md) ·
[plugins](docs/plugins.md) · [operations](docs/operations.md) ·
[lifecycle](docs/lifecycle.md) · [releases](docs/releases.md) ·
[build and test](CONTRIBUTING.md)

## License

MIT
