# La Roca

**Local semantic memory for agent fleets. One binary.**

[![CI](https://github.com/thellmwhisperer/la-roca/actions/workflows/ci.yml/badge.svg)](https://github.com/thellmwhisperer/la-roca/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go 1.25](https://img.shields.io/badge/go-1.25-00ADD8.svg)](go.mod)

Agent runtimes leave sessions, exchanges, reasoning blocks, tool calls, and
memory files in different local formats. La Roca ingests that trail into one
SQLite database and answers natural-language questions through a CLI or MCP.
There is no hosted service, vector database, or agent framework to run.

## Install and start

```sh
curl -fsSL https://raw.githubusercontent.com/thellmwhisperer/la-roca/main/install.sh | sh
roca init
roca query "what did we decide about the ingest matrix"
```

With no home database, `roca init` asks whether to create a new database or
adopt an existing one by copy. It never chooses silently. For automation, pass
an explicit location with `roca init --db-path <path>`.

The default data directory is `~/.roca`. It contains `roca.db`, configuration,
credentials, backups, `prompt.md`, and operational JSONL under `logs/`. La Roca
does not edit agent instruction files; the operator decides whether to use the
generated prompt or install the bundled skill.

## Operational logs

The dated `executions`, `mcp-audit`, and `ingest` JSONL streams retain 30 days.
A logging failure warns once but never changes a command or tool result.

Execution records store the command, changed flags, database path, duration,
exit code, error and result metadata. Query records keep the question, route,
provider, model, SQL, timings, degradation, provider failure text and row count;
they never store result row contents. MCP audit records store the tool,
redacted arguments, verdict, degraded state, duration and result row count.
Ingest records retain the complete ingest envelope, including every file error
and every discarded source record with its path, parser, record position and
reason.

No log is stored in SQLite and no run tables exist. Before a line reaches disk,
redaction covers sensitive field names; bearer and key/value secrets; PEM
private keys; OpenAI `sk-*`, GitHub `gh[pousr]_*` and `github_pat_*`, Slack
`xox*`, JWT `eyJ*`, AWS `AKIA*`, and Google `AIza*` credential shapes. Log
directories and files are created with operator-only permissions.

## What it reads

`roca ingest` incrementally reads supported local artefacts:

| Runtime | Artefacts |
|---|---|
| Claude Code | Sessions, subagent transcripts, and per-project memory files |
| Claude Desktop and Cowork | Session stores and Claude memory files |
| Codex | Sessions, memory, rule and skill files, and the state database |
| OpenCode | Its local database |
| Pi | Session files |
| Hermes | Its state database |

Repository `AGENTS.md` and `CLAUDE.md` files are instructions and are never
ingested as memories. Live databases are opened as guests with SQLite
`query_only` enabled and a short busy timeout.

## Querying

`roca query` asks a configured provider to produce one SQLite `SELECT`, checks
the statement against the embedded schema, applies a row limit, executes it,
and returns compact TOON rows. If no provider is usable or generated SQL cannot
run, literal search is attempted and the degraded state is reported.

```sh
roca query "what changed in the release process"
roca query "what changed in the release process" --full
roca query "what changed in the release process" --sql-only
roca exec "SELECT layer, COUNT(*) AS total FROM memories GROUP BY layer LIMIT 20"
```

The default `query` result is evidence for agents. `--full` adds a prose
interpretation for a human reader in the question's language. `--sql-only`
compiles without executing; `roca exec` runs an explicit `SELECT` through the
same gate. `--json` returns the complete machine-readable envelope, and
`--max-chars` bounds text fields.

Literal search uses a local SQLite FTS5 index ranked by relevance, with a plain
`LIKE` fallback before the index exists. Unicode diacritics are folded for
matching without imposing a corpus language.

## Providers

The default order is `codex, ollama`: an available subscription provider first
and a local provider last. Configuration decides the order. `roca doctor`
reports what is available and how to fix anything that is not.

```sh
roca login codex
roca login xai
roca model set codex <model-id>
roca doctor
roca logout codex
```

See [docs/models.md](docs/models.md) for provider configuration and selection.

## CLI and MCP

The daily root menu is `init`, `query`, `store`, `ingest`, `login`, `doctor`,
`update`, and `uninstall`. Integration commands remain callable without
crowding the root help.

`roca mcp serve` runs a foreground stdio server owned by the calling agent. It
exposes five tools: `roca_exec`, `roca_health`, `roca_query`, `roca_sql`, and
`roca_store`. They call the same service as the CLI.

```sh
roca mcp install codex
roca mcp status
roca mcp uninstall codex

roca skill install codex
```

Supported integration targets are Codex, Claude, OpenCode, Hermes, and Pi.
Configuration edits preserve unrelated bytes and create a recovery backup.
See [docs/mcp.md](docs/mcp.md).

## Operations

Every CLI execution writes a redacted JSONL record under the selected data
directory's `logs/`. A logging failure is reported on stderr but does not
change the command result. Log directories and files are created with
operator-only permissions and dated streams retain thirty days.

```sh
roca update
roca uninstall
roca uninstall --purge
```

Update verifies the downloaded binary against `checksums.txt` before replacing
the current executable. Uninstall removes integrations and the binary; data is
kept unless the operator explicitly consents to purge. See
[docs/lifecycle.md](docs/lifecycle.md).

`ROCA_READ_ONLY=1` refuses writes in the shared service before database I/O, so
CLI and MCP enforce the same boundary.

## Build and test

```sh
make build
make check
make accept-index
make dist
```

`make check` runs formatting, vet, unit tests, the Godog acceptance suite, and
the duplication gate. Acceptance contracts live under `features/`; the Godog
harness is compiled only with the `acceptance` build tag.

## License

MIT. See [LICENSE](LICENSE).
