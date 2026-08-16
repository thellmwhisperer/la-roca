<p align="center"><img src="docs/assets/banner.jpg" alt="La Roca: glowing agent monoliths feeding golden roots into one shared bedrock" width="100%"></p>

# La Roca

**Your agents' history is a database.**
**Interrogate it. Interact with it. Learn from it. Have fun with it.**

One core file, zero dependencies. Local SQLite. CLI + MCP.

[![CI](https://github.com/thellmwhisperer/la-roca/actions/workflows/ci.yml/badge.svg)](https://github.com/thellmwhisperer/la-roca/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

https://github.com/user-attachments/assets/f27d377b-e4ad-4c59-beb6-86dd02af4f84

Your coding agents write thousands of sessions, reasoning traces, tool calls,
and memory notes to disk, then forget all of it. La Roca reads what Claude
Code, Codex, Qwen Code, GLM, Cursor, OpenCode, Pi, Hermes, Grok Build, and Claude
Desktop leave behind, normalizes it into one SQLite database on your machine, and answers questions
about it: from your terminal, or from the agents themselves over MCP.

Every core query answer shows its proof: the SQL that produced it and the rows
that back it.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/thellmwhisperer/la-roca/main/install.sh | sh
```

This installs one static binary at `~/.local/bin/roca`, with no dependencies
and no other changes. La Roca supports macOS on Apple Silicon and Linux; on
Windows, use WSL.

If `claude` or `codex` is already installed and signed in, no login of any kind
is needed: run `roca init` and go. Update later with `roca update`.

## Sixty seconds

```text
$ curl -fsSL https://raw.githubusercontent.com/thellmwhisperer/la-roca/main/install.sh | sh
$ roca init
$ roca query --full "what did we decide about the retention window"
SQL · codex · gpt-5.6-luna · 3.1 s / search · 2 ms / answer · codex · gpt-5.6-luna · 9.8 s

You decided it on 2 August: operational logs keep 30 days, in dated streams.
Your own words that night: "30 days and out. I do not want eternal logs."
```

If an already signed-in agent CLI is on `PATH`, that is the factory default:
La Roca detects it and natural-language queries work immediately. No provider
table and no model configuration step are required; `roca model check` only
confirms that the session answers. Without a detected CLI, La Roca tries the
local Ollama floor and finally the keyword rescue, naming every missing or
unavailable answering route.

`roca init` asks before creating or adopting a database, then shows the models
this machine can actually serve. The chooser is model-first: pick a model,
let La Roca resolve its detected CLI or Ollama harness (or choose among several),
and confirm the pair. Plain Enter keeps the same factory choice La Roca would
have made without configuration. The confirmed choice is written surgically to
`~/.roca/config.toml`, with a named recovery backup when an existing file is
changed; it does not log in again or copy an agent CLI's session. See the
[full init flow](docs/lifecycle.md#initialize) for the terminal and automation
contracts.

Every human-readable init closes with one `answering:` line naming the provider,
model, exact configuration path, and how to change it. A non-interactive init
with an explicit `--db-path` asks no model questions, writes no model
configuration, keeps the factory selection, and prints the same one-line answer
notice. The summary also tells you how deep your memory goes: the oldest moment
it ingested is the floor of your rock. `roca doctor` reports the same floor and
model health.

## Let your agent drive

The best experience is letting your agent drive: ask it a question in plain
language and it interrogates La Roca for you, reads the rows, follows up,
and digs where the evidence points. You stay in the conversation; the
database work happens underneath.

An agent with a shell uses the CLI directly. An agent without one gets the
same operations through La Roca's MCP layer, and the experience is
practically the same, sometimes better.

## Private by construction

One binary, one SQLite file in `~/.roca`, zero network in the ingest path.
Providers are called only to answer the questions you ask, and the SQL phase
never sees your rows. With `--full` or `explore`, the prose phase receives at
most ten result rows with each field truncated to 240 characters; the database,
the full result set, and the search index never leave the machine. Explore reads
the whole result set locally to compute its terrain, and only the aggregates
travel: row counts per source, month clusters, co-occurring terms, and negative
space. The ten-row cap governs raw row content in every model prompt, so an
interpreter sees that capped sample plus those precomputed aggregates, never
the full result set as text. Configure only Ollama when no query content may
leave it at all.

Every CLI command and MCP tool call writes a size-capped, redacted record to
the bundled ops database and to JSONL under `logs/`; query records never store
result row contents, and `roca doctor` summarizes recent query failures. The
stable format, retention, and full redaction list live in
[docs/operations.md](docs/operations.md). `ROCA_READ_ONLY=1` refuses writes in
the shared service before database I/O, so CLI and MCP enforce the same
boundary.

## What you can ask

Things that are one question away once your history is a database.
The prose examples below use an optional Codex-to-Ollama split; without an
explicit `models.interpret_order`, the provider that writes the SQL also reads
the rows.

### Which model do you actually have chemistry with?

The questions you never thought your logs could settle:

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

### The bug you know you have already fixed once

```text
$ roca query "have I fixed a stale lock error before"
SQL · provider codex · model gpt-5.6 · 2.9 s / search · 3 ms
rows[2]{source,created_at,text}:
  exchange,"2026-06-14 23:41:02","fixed: stale .lock left by a killed run; rm .ingest.lock and rerun with --resume"
  memory,"2026-06-15 00:02:19","Pattern: a killed ingest leaves .ingest.lock behind; delete it before blaming the parser"
```

### The perfect one-liner an agent wrote for you weeks ago

```text
$ roca query "the ffmpeg one-liner that extracted frames for verification"
rows[1]{source,created_at,text}:
  exchange,"2026-07-29 18:05:33","ffmpeg -ss 2 -i out.mp4 -frames:v 1 -q:v 3 frame.jpg   # verify before delivering"
```

### Yesterday's decision, with the conversation that made it

```text
$ roca query "what did we decide about the retention window"
rows[2]{source,created_at,text}:
  memory,"2026-08-02 21:14:09","Decision: operational logs keep 30 days, in dated streams"
  exchange,"2026-08-02 21:02:44","30 days and out. I do not want eternal logs."
```

### One answer, two readers

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

### Exact SQL, when you want it

Because it is a real database, not a search box. A keyword or vector index
cannot replace an exact SQL query for this:

```sh
roca exec "SELECT source_agent, COUNT(*) AS sessions
           FROM sessions
           WHERE started_at LIKE '2026-07%'
           GROUP BY source_agent
           ORDER BY sessions DESC"
```

`roca query` compiles your question into one checked `SELECT` and shows it.
`--sql-only` compiles without executing, `--full` adds a prose reading of the
rows, `roca exec` runs your own `SELECT` through the same read-only gate, and
`--json` returns the complete machine envelope. Questions must contain text and
have a generous 1000-character cap on both CLI and MCP query surfaces.

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
under [Model providers](docs/models.md#the-repairs-between-the-model-and-the-gate).
If the repaired candidate still fails either the gate or at execution, La Roca
gives the model exactly one correction attempt with that SQL and SQLite's exact
verdict before using the literal rescue. `retry_type` distinguishes
`gate_rejection` from `execution_error`; the JSON envelope retains both attempts
and attributes the retry latency separately.

## Three ways to use it

### 1. Shared context between agents

A session starts by asking for the latest handoff and ends by storing one.
The Claude session you open today knows what Codex did last night, without
you re-explaining the project.

```sh
roca query "latest handoff for this project"
roca store --layer handoff --content "token refresh done, retry pending" --agent codex --model gpt-5
```

### 2. Chat with your data

Ask your own history real questions:

- Which sessions went well, and which one wasted an evening?
- What do you keep re-explaining to every new session?
- Which model is fastest at fixing tests? Which one writes the best plans?
- Which model do you actually have fun working with, and which one can you
  simply not work with?
- Which harness works best for which kind of work?

The answers are already in your logs, with the rows to prove them. Use them
to prompt better and to pick the right agent for the next job.

### 3. Distill what repeats

Patterns in your history become skills that travel back to every agent.

A regular skill is a snapshot of a tool. A skill distilled from La Roca
comes with its whole story: the how, the why, and the failed attempts behind
the final answer, one question away.

`roca skill install` ships the operating craft into each runtime today. The
installed skill and generated prompt keep shipped SYSTEM content separate from
an operator-owned USER zone, and `roca update` tracks their release in
`~/.roca/artifacts.json`. Automatic refresh is available behind the default-off
`features.artifact_refresh` key.

The `pill` layer is built for what comes next: condensed artifacts distilled
from your own history and injected through hooks, charging an agent with
exactly the information the task needs instead of a whole skill.

## How it works

- **One normalized schema.** Sessions, exchanges, thinking blocks, tool calls,
  and curated memories in typed layers (`handoff`, `pattern`, `discovery`,
  `feedback`, `pill`, among others), the same shape for every runtime it
  reads.
- **A query is two inferences.** The first sees the schema, never your rows,
  and writes one `SELECT`. The second sees only the result rows and composes
  the answer. Each phase's provider is configured independently, including
  fully local through Ollama: make the query smart so the reader can be
  cheap, local, and secure.
- **Bring the plan you already pay for.** La Roca detects supported agent CLIs
  already on `PATH` and uses their existing signed-in sessions without reading,
  copying, or storing secrets. No La Roca login is required. For machines
  without a usable local CLI, the local Ollama floor and keyword rescue remain.
- **Exact core retrieval; optional semantic plugin.** Core recovery is SQL plus
  a local FTS5 index with diacritic folding; a plain `LIKE` fallback works
  before the index exists, and this route stays exact and auditable. Your model
  can still supply natural-language query planning at question time; semantic
  candidate retrieval is an optional executable package you build and install
  yourself, with its embedding index remaining outside core. See the
  [executable plugin contract](docs/plugins.md#executable-only-packages) and
  the [roca-vector package guide](plugins/vector/README.md) for the setup and
  domain-extension boundary.
- **Semantic-first agent craft.** The installed skill can use the optional
  local vector package to retrieve conceptual candidates when both
  `features.plugins` and `features.vector` are enabled and its local model is
  ready, then resolve their source context through core before forming a
  verdict. A vector score is not evidence, and literal rescue is never silently
  presented as semantic retrieval. See
  [Semantic retrieval](docs/semantic-retrieval.md).
- **Honest core degradation.** No usable provider, or SQL that cannot run,
  falls back to literal search and says so in the result. The semantic-first
  skill reports an unavailable vector route instead of presenting that exact
  fallback as semantic retrieval.

## What it reads

`roca ingest` incrementally reads supported local artefacts:

| Runtime | Artefacts |
|---|---|
| Claude Code | Sessions, subagent transcripts, and per-project memory files |
| Claude Desktop and Cowork | Session stores and Claude memory files |
| Claude web/Desktop export you point it at | Conversations and Claude memories from the official Anthropic data export |
| ChatGPT export you point it at | Conversations from the official OpenAI data export |
| Codex | Sessions, memory, rule and skill files, and what matters from its state database |
| Qwen Code | Project chat sessions, including tool calls and source-recorded models |
| GLM | User skill documents and their supporting Markdown files |
| Cursor | Composer sessions, prompts, thinking, and tool calls from its local state databases |
| OpenCode | Sessions and exchanges, distilled from its local database |
| Pi | Complete session tree, including nested child runs |
| Hermes | Sessions, distilled from its state database |
| Grok Build | Sessions, from the session update stream and its metadata sidecar |

Repository `AGENTS.md` and `CLAUDE.md` files are instructions and are never
ingested as memories. Live databases are read with SQLite `query_only` enabled
and a short busy timeout. Cursor databases are first serialized into an
immutable snapshot so active WAL content is included without changing Cursor's
stores.

Downloaded Anthropic exports are one-shot imports. Run `roca ingest <path>` with
the extracted directory; see
[Ingest sources](docs/ingest.md#import-an-anthropic-data-export).

Downloaded OpenAI exports use the same one-shot command. La Roca reads legacy
`conversations.json` exports and newer `conversations-*.json` shards, imports
only the delta across newer exports, and refuses paths it cannot read. See
[Ingest sources](docs/ingest.md#import-an-openai-data-export).

## Agents plug in

La Roca is built agent-first, following the AXI convention (agent ergonomic
interface) shared by a family of agent-facing tools: route narration above
the data, compact TOON rows, bounded text previews, and deterministic next
commands in every answer. An agent never has to guess what it just got or
what to run next.

`roca mcp serve` runs a foreground stdio server owned by the calling agent,
exposing six tools that call the same service as the CLI: `roca_query`,
`roca_explore`, `roca_exec`, `roca_sql`, `roca_store`, and `roca_health`.
`roca_explore` accepts `deep: false|true` and returns the same prose, terrain
mission, generated SQL, and next probes as the matching CLI mode; it is never a
rows-only MCP shortcut.

Behind the default-off experimental `features.plugins` flag, third parties extend
queries with [isolated SQLite plugin databases declared in a manifest](docs/plugins.md),
and may add Git-style `roca-<name>` neighbor executables for commands.

Installed plugins may also declare idempotent rides for the default-off
[`roca cron` train](docs/plugins.md#scheduled-rides). Set `features.cron = true` to
expose it; absent or false, the command does not exist. The train reads ride
manifests whether or not `features.plugins` is set, invokes and observes those
commands in order, defers on the core lock or a closed dependency gate, and
records each journey in its own plugin database. It is designed for an ordinary
system-cron entry, not a resident daemon:

```sh
roca cron list
roca cron run --dry-run
roca cron run nightly
```

```sh
roca mcp install codex     # declare the server in a runtime's configuration
roca mcp status            # which agents have La Roca configured
roca skill install claude  # ship the usage craft into a runtime's skills
roca hooks install claude  # sign Claude Code shell writes before they run
```

Supported integration targets are Codex, Claude, OpenCode, Hermes, and Pi; the
signing hook ships for Claude only, and
[Memory authorship](docs/operations.md#memory-authorship) explains what it
stamps. Configuration edits preserve unrelated bytes and create a recovery
backup.


## Going deeper

The [docs index](docs/README.md) orders the longer reads:

- [Architecture](docs/architecture.md): the kernel, its plugins, and the four
  internal domains.
- [Model providers](docs/models.md): automatic CLI detection, provider order,
  local floor, and CLI-owned authentication.
- [The MCP plug](docs/mcp.md): tools, contract, integration targets.
- [Install, update, and uninstall](docs/lifecycle.md): the binary's life.
- [Operations](docs/operations.md): logs, redaction, retention.
- [Releases](docs/releases.md): how versions are cut.

## Build and test

```sh
make build
make check
make accept-index
make upgrade-gauntlet
make split-oracle
make dist
```

`make check` runs formatting, vet, unit tests, the Godog acceptance suite, and
the duplication gate. Acceptance contracts live directly under
`features/{store,ingest,provider,distribution}/`; every feature there is
discovered automatically, and `make accept-index` rejects any other layout. The
Godog harness is compiled only with the `acceptance` build tag.

`make upgrade-gauntlet` is the second gate every pull request has to pass: it
upgrades the committed homes of older releases through the binary you just
built. [Releases](docs/releases.md#schema-migration-definition-of-done) explains
when a change owes the gauntlet a new frozen home.

`make split-oracle` replays the DATA SPLIT compatibility oracle on its own, the
executable definition of zero behavior change for CLI and MCP users that
`make check` already runs with the rest of the acceptance suite. It drives the
binary you just built against a fully synthetic fixture, normalizes away run
noise (timestamps, durations, correlation ids, home paths, and the build's own
version and source sha), and compares the recording against the Ed25519-signed
goldens in `testdata/data-split-oracle/`.
The oracle never reads a real `~/.roca` database and never writes user data: it
records into a temporary home and keeps the recording under the project's
`.tmp/` only when it differs from the seal. A difference is reported, never
absorbed, because an intended behavior change is resealed by the owner instead
of edited into the golden.

Resealing is the owner's decision. It means replacing `golden.json` with the
reviewed recording, recomputing the fixture and golden SHA-256 digests in
`manifest.json`, and signing that manifest payload again with the Ed25519
private key the owner holds outside this repository. Only the public half of the
key is committed, pinned both in `manifest.json` and in the oracle test's
`oraclePublicKeyBase64`, so a bundle edited without that private key fails
verification rather than passing quietly.

The seal is tamper evidence, not a capability barrier. A branch that replaces the
pinned public key along with the goldens verifies against its own fresh key, so
what protects the contract is review: any diff touching `oraclePublicKeyBase64`
or `testdata/data-split-oracle/` is a reseal, and it needs the owner's eyes
before it merges.

## License

MIT. See [LICENSE](LICENSE).
