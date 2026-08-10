# La Roca

**Local memory for agent fleets. One binary.**

[![CI](https://github.com/thellmwhisperer/la-roca/actions/workflows/ci.yml/badge.svg)](https://github.com/thellmwhisperer/la-roca/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go 1.25](https://img.shields.io/badge/go-1.25-00ADD8.svg)](go.mod)
[![Works offline](https://img.shields.io/badge/network-not%20required-success.svg)](docs/models.md)

Your agents leave an enormous trail on disk: sessions, exchanges, reasoning
blocks, tool calls and memory files. That trail is the only
institutional memory that exists of what your fleet did and decided, and today
it is unreadable, because every runtime keeps its own JSONL files and its own
private SQLite database in its own format.

La Roca turns that trail into a database you can ask questions in plain
language, from a shell or from an agent.

```
agent artefacts  ->  normalization into SQLite  ->  agentic NL-to-SQL query  ->  CLI + MCP surface
```

Four links, and nothing else. No hosted service, no vector database to run, no
agent framework to adopt.

## The magic minute

From a binary nobody has run to an answer out of your own corpus. There is a
test that walks exactly this and fails if it takes longer than sixty seconds
(`test/acceptance/magic_minute_test.go`), on a machine with no network and no
model installed.

```sh
# 1. install (the repository is private for now, so the route is authenticated)
TOKEN="<your token>"; REPO="thellmwhisperer/la-roca"
curl -fsSL -H "Authorization: Bearer ${TOKEN}" \
     -H "Accept: application/vnd.github.raw" \
     "https://api.github.com/repos/${REPO}/contents/install.sh" \
  | GITHUB_TOKEN="${TOKEN}" sh

# 2. bootstrap: if a database is found, inspect it and answer adopt or new;
#    then read every source, index, check models, generate the bench and prompt
roca init

# 3. ask
roca query "what did we decide about the ingest matrix"
```

`roca init` is the whole bootstrap and none of it can fail the command: a source
that cannot be read and a model that is not installed are reported states with
their remedy, because what init promised is a database that is ready. Every
phase announces its start and result, including detected and absent agents,
database size and row counts, index duration, ingest deltas, calibration cases
and the provider that will answer. The last output is the path to `prompt.md`
in the configured data directory (by default `~/.roca/prompt.md`) and its short
contents; paste that block into agent instructions when wanted. La Roca never
edits instruction files itself.

Database selection is never implicit. With no home database, init explains
`new` and `adopt` and asks with no default. Adoption then asks the user to type
the source path, prints its path and size, copies it into `~/.roca`, and leaves
the original untouched. New creates a fresh database and then indexes detected
agent sources. If `~/.roca/roca.db` already exists, init identifies it as its
home database and asks to keep or explicitly reinitialize it. Without a
terminal, init refuses instead of waiting or choosing. Automation can select a
location with `roca init --db-path <path>`; a missing file there is created.

## Studyable traces

Every CLI execution leaves one credential-safe JSONL record under `logs/` in
the selected data directory (by default `~/.roca/logs/`). Files are dated:
`executions-YYYY-MM-DD.jsonl`, `mcp-audit-YYYY-MM-DD.jsonl`, and
`ingest-YYYY-MM-DD.jsonl`. Each stream retains the current day and the previous
29 days; older dated files are removed when that stream is next written.

Execution records carry the command, changed flags, database path, duration,
exit code, error and the command's existing `--json` result envelope when one
exists. Query envelopes also carry route, provider, model, SQL-inference,
execution and optional interpretation timings, degradation, provider failure
text and row count. MCP audit records carry the tool, arguments, verdict,
duration and result row count. Ingest records retain the complete ingest
envelope, including every file error and every discarded source record with its
path, parser, record position and reason.

No log is stored in SQLite and no run tables exist. Sensitive flag names,
credential-shaped text, authorization headers and private keys are redacted
before a line reaches disk. Log directories and files are created with operator
only permissions.

## What it reads

One `roca ingest` sweeps every source family of the matrix, incrementally, so a
second run costs almost nothing:

| Runtime | What is read |
|---|---|
| Claude Code | session and subagent transcripts and per-project memory files |
| Claude Desktop and Cowork | their session stores; Claude's per-project memory files remain the durable context shared across these workflows |
| Codex | sessions, memory/rule/skill files, and the state database |
| OpenCode | its live database |
| Pi | its session files |
| Hermes | its state database |

`roca init` and `roca doctor` report the agents detected from the routes and
stores that actually exist. Only those present are read. `workspace_roots` has
one job: resolving the project identity of sessions. The global `~/.claude/CLAUDE.md`
and the repository `AGENTS.md`/`CLAUDE.md` files are instructions and are not
ingested as content.

OpenCode and Hermes are live databases owned by other agents. La Roca follows a
guest rule: never write to them (`query_only`) and never make their owner wait
(a 250 ms busy timeout). Technical note: the connection uses a normal OS-level
open because SQLite `mode=ro` cannot reliably read a live WAL database; the
engine-level `query_only` guard is what rejects writes
(`internal/ingest/foreign.go`).

## Your agents and the database

The memory is queried through roca, either by the CLI or by the MCP surface.
**The SQLite database is never opened directly by the operator's agents** and it
does not have to be: every answer and every write of value the agents need has a
surface they can reach.

Why:

- **No accidental corruption.** A database that is opened by two SQLite
  processes at the same time, one of which writes, is a database that can be
  corrupted. The read-only guard (`ROCA_READ_ONLY=1`) turns every agent
  connection into one that the engine itself rejects every write on, so a
  session that should only be reading cannot corrupt anything by accident.
- **Auditing.** Every write that goes through roca carries the surface it came
  from, which is the field a later `roca query` can filter on. A write fired
  directly at the database leaves no audit trail.
- **The schema is a moving target.** The tables, the columns and the indexes of
  the database are the product's private state, and they change between
  versions. An agent that queries `roca.db` directly ties itself to a schema
  version, and breaks silently when the next `roca update` brings a different
  one.

`ROCA_READ_ONLY=1` makes the database available to any process that should only
read it: the flag is checked in the service before any database I/O, so both
surfaces (CLI and MCP) refuse with the same words. An agent that cannot be
trusted with writes can be pointed at a read-only copy.

**Separation by OS user.** The honest answer is that enforcement at the file
system on the same account does not exist without encryption at rest, which
belongs in v2 because it carries a real trade-off (CGO against a static binary).
What a machine that hosts several agents can do today is run the agents under a
different OS user, and point roca at a database only that user can write. The
product does not own the user account, so it does not claim to have solved it.

## How it answers

Most questions never reach a model. A trained classifier resolves the fast route
in milliseconds, and only what it declines is worded as a question to a model,
which costs seconds. What comes back, from the model or from a template, passes
the same two-halved SQL gate before it runs: the engine says which tables and
columns exist, the parsed statement says the verb, the functions and the LIMIT.
Nothing but a bounded `SELECT` ever reaches your data.

Default row results use TOON, with a stable four-field search shape and the
next useful commands carried in the output. The route line remains narration;
`--json` keeps the complete machine-readable envelope unchanged.

```console
$ roca query "what do we know about AXI output"
route compiler · search_all_sources_by_term · 4 ms
rows[1]{source,id,created_at,text}:
  memory,1,"2026-08-07 17:39:43","AXI output uses TOON rows, stable fields, and contextual help."
help[2]:
  - "Run `roca query \"what do we know about AXI output\" --json` for the complete result envelope"
  - "Run `roca query \"what do we know about AXI output\" --sql-only`, then `roca exec \"<SELECT>\" --max-chars 2000` to inspect or expand rows"
```

Search is lexical: a SQLite FTS5 full-text index ranked by bm25, with an honest
fall to a plain `LIKE` scan when the database is not indexed yet. The index,
the query expression and the question's own folding all lowercase and strip
diacritics the same way, so what the index stores and what the query asks for
are the same tokens. That is why the index needs no network, no API key and no
external service: it is a derived table the binary builds and maintains itself.

**Offline with a local floor.** The default provider order is `codex, ollama`:
the subscription first when you have one, and local Ollama last, because the
last element is always something that can exist on any supported platform. A
machine with no model at all keeps answering everything the deterministic route
knows. `roca doctor` tells you which provider is going to answer and the exact
remedy for each one that is not available. Details in [docs/models.md](docs/models.md).

**And it does not invent.** A lexical index returns only literal matches, so a
question from a foreign domain returns nothing and not a guess. An honest zero
rows is never dressed up as an answer.

## The CLI is the product, the MCP is the plug

Every capability is a command, but the root menu keeps only the daily surface:
`init`, `query`, `store`, `teach`, `ingest`, `login`, `doctor`, `update` and
`uninstall`. Advanced and integration commands remain callable without making
`roca --help` a catalogue.

For agents with no shell, `roca mcp serve` is an MCP server over stdio, in the
foreground, that your agent owns and starts on demand. It exposes six tools
(`roca_exec`, `roca_query`, `roca_sql`, `roca_store`, `roca_teach`,
`roca_health`), and every
one of them is a passthrough into the same kernel the CLI calls, so there is one
behaviour and not two.

```sh
roca mcp install codex     # also: claude, opencode, hermes, pi
roca mcp status
roca mcp uninstall codex   # gives your file back byte for byte
```

Your configuration file is edited by byte range and never by parse and
re-serialize, so your comments, your key order and your formatting survive. That
is measured the only way that is not an opinion: installing and then withdrawing
has to give back the exact previous bytes, on all five runtimes.

**Session hooks.** `roca hook install claude` declares the lifecycle hooks that
hand a fresh session what it should already know, under a measured character
budget, and record the handoff of the one that ends. A hook reaches the kernel
by running a command, never by touching the database, and it never exits
non zero, because a hook that fails is a hook that breaks somebody's session.
See [docs/mcp-and-hooks.md](docs/mcp-and-hooks.md).

## Is the search any good

You do not have to take anybody's word for it. `roca calibrate` builds a golden
bench out of your own corpus (`roca init` builds the first one for you), and
`roca bench golden` scores the search against it. A generated case is born green
or it is not published: what cannot find its own memory measures nothing.

## Install, update, uninstall

Installing is copying one file. The installer verifies the sha256 against
`checksums.txt` before writing anything, converges over a run you killed, and
never overwrites a file it did not put there.

```sh
roca update              # verifies, then swaps by rename, keeping the way back
roca uninstall           # removes the binary, and with your consent the data
```

`roca uninstall` deletes what La Roca owns whenever it exists, names what it did
not create instead of deleting it, and can be run twice. The contract is in
[docs/lifecycle.md](docs/lifecycle.md).

The release artefacts are produced by the channel and never by hand
(`.github/workflows/release.yml`). A `vX.Y.Z` tag builds the four targets from a
single runner, publishes them, and uploads `checksums.txt` last:

| Platform | Artefact |
|---|---|
| Apple Silicon | `roca-<version>-darwin-arm64` |
| Linux x86_64 | `roca-<version>-linux-x64` |
| Linux arm64 | `roca-<version>-linux-arm64` |
| Windows x86_64 | `roca-<version>-windows-x64.exe` |

Windows is not installed by `install.sh`: download the `.exe` from the releases
page and put it on your PATH.

## Building it yourself

```sh
make build     # a static bin/roca for this machine (CGO_ENABLED=0)
make check     # format, vet, tests and the duplication gate
make accept    # the Gherkin suite against the real binary, in a sandbox HOME
make dist      # the four release targets from this one machine
```

The acceptance suite in [`features/`](features) was written before the code and
is the executable contract: 102 scenarios frozen before a line
was built, across install, operator flow, defect regressions, the query cascade,
the golden bench, teach, model adapters, the MCP plug, concurrency and the
surface, plus 9 more for the session hooks, which entered with the capability.
The scenarios implemented by this build are declared in
`test/acceptance/acceptance_test.go`, and the handful that stay unclaimed say
there, in writing, why.

## Documents

- [docs/models.md](docs/models.md), providers, presets and the cascade.
- [docs/mcp-and-hooks.md](docs/mcp-and-hooks.md), the plug and the session hooks.
- [docs/lifecycle.md](docs/lifecycle.md), install, calibrate, update, uninstall.

## License

MIT. See [LICENSE](LICENSE).
