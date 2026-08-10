# PRD: La Roca v1

**Product:** La Roca (new repo, clean history, Go, single static binary)
**Document:** Product Requirements Document, version 1.0 (draft for the product owner's blessing)
**Date:** 2026-08-05
**Author role:** Product Manager
**Reference code audited:** frozen Python reference, branch `release/saneamiento-20260805`, HEAD `577a40c` (52 commits on top of `b0387e7`).
**Status:** proposed. After review, this document is consecrated in the product repo as `docs/prd-v1.md`.

Sibling documents this PRD does not duplicate and delegates to: the La Roca v1 tech spec (Go architecture, schema, adapters, fastText inference, distribution), the La Roca v1 Gherkin acceptance suite (executable contract), and this repository's operating manual (process, roles, state machine).

---

## 0. Executive summary

La Roca is local memory for agent fleets: it ingests the artefacts agents leave on disk, normalizes them into a SQLite database, and answers natural-language questions about that corpus over CLI and over MCP. It works with no network.

The Python reference has already demonstrated the whole essence against real data: a 685 MB production database with 3,943 sessions and 306,281 conversation rows accumulated between 2026-01-21 and 2026-08-04, two measured query routes (16 ms through the deterministic compiler, 22,533 ms through the local model), and a complete installation battery green on a real Mac mini, two cycles out of two.

What the laboratory does not give is the product's form. The current format (a 103 MB Python virtual environment, a release-archive installer, a resident server, 98 declared environment variables) is not what the product should ship. La Roca v1 is the same product in a single static binary per platform: installing is copying a file, uninstalling is deleting it and deleting the data with confirmation.

The v1 scope is closed by decision: **memory, query and teach**. `inbox` and `proposals` move to v2. Media (video and vision) is an optional companion outside the core binary. The production evidence backs that cut: the real database has 333 memories, 3,943 sessions and 72,670 exchanges, and exactly **0 proposals, 0 runs and 0 inbox messages**.

The eight defects of the cleanup campaign (D-1 to D-8) are the minimum quality bar: none of them may be reborn in the new product, and each one enters the Gherkin suite as a named regression scenario.

---

## 1. Vision and positioning

### 1.1 What La Roca is

An agent fleet produces an enormous trail on disk: sessions, exchanges, reasoning blocks, tool calls and memory files. That trail is the only institutional memory that exists of what the fleet did and decided, and today it is unreadable: thousands of JSONL files and SQLite databases private to each runtime, in different formats and with no common index.

La Roca turns that trail into a database queryable in natural language, and exposes that query over the two surfaces an operator and an agent know how to use: a CLI and an MCP server.

The chain has four links, and it is the essence contract frozen on 2026-08-04:

```
agent artefacts  ->  normalization into SQLite  ->  agentic NL-to-SQL query  ->  CLI + MCP surface
```

`py/pyproject.toml:4` described it as *"Universal semantic memory for AI agents"*. v1 ships that same intent as local memory queried in natural language; its retrieval is lexical (SQLite FTS5), with no vector store and no embedding service.

### 1.2 The three properties that define the product

**A single binary.** One static file per platform, with no interpreter, no virtual environment and no service manager. The explicit references are single-binary products like Docker. Installing is copying; uninstalling is deleting. The current state (a 103 MB virtual environment plus a 282-line shell installer that resolves GitHub releases, verifies sha256 and manages a versioned prefix with a symlink, `install.sh:1-22`) does not meet that vision, and it is the declared reason a new repo is born instead of continuing to polish the current one.

**Local and capable with no network.** The database lives on the operator's machine. The query works with no network and no credential because there is always a local floor: Ollama with `qwen3.5:4b`. The frontier improves relevance when there is a credential, but it is never a requirement for the product to answer.

**The CLI is the product.** The decision, confirmed twice on 2026-08-05: the whole kernel is exposed through commands. The MCP is a thin protocol plug over the same kernel, for agents that have no shell. Exact words: *"In the final split: the CLI is the product; the MCP is the plug."*

### 1.3 Who it is not for

La Roca is not a hosted service, it is not a vector database, and it is not an agent framework. It does not compete with a specific agent runtime's internal memory: it feeds on all of them at once, which is precisely what none of them does. The real database has five different agent families living in the same sessions table (`opencode` in six modes, `claude-code`, `pi`, `hermes` in four models, plus `codex` and `config` as memory origins).

### 1.4 Product principles

Five laws that govern any v1 decision, inherited from the cleanup campaign and from the v1 contract:

1. **Code gets pruned, functionality does not.** A capability does not disappear without an explicit order. The fastText classifier is wanted functionality and deleting it is vetoed.
2. **A green pipeline is not "it works".** Nothing is declared ready without testing the real deliverable on a real machine. A green unit suite is worth zero as acceptance.
3. **The headline equals the contract.** If one point of the acceptance criteria fails, the verdict is DOES NOT MEET, and what is green goes underneath.
4. **Debt is a finding, never a convention.** A discovered duplication is removed with an owner, not documented as a house rule.
5. **The operator's persisted data never kills the runtime.** A value the operator wrote outlives the release that understood it; it degrades with a warning, not with an exception. It is the law born of D-5.

---

## 2. Users and jobs to be done

### 2.1 The two users

**U1. The fleet operator.** A human being with a shell, who owns the machine and the agent fleet. The operator is the canonical case and the reference flow is documented command by command, extracted from a real shell history on the Mac mini over three working days. The operator works from a terminal, installs through a one-liner, and judges the product by whether it starts clean and leaves clean.

**U2. The programmatic consuming agent.** Software. It splits in two according to whether it has a shell:

- **U2a, agent with a shell** (most of them: Claude Code, Codex, OpenCode, Pi, Hermes and any hook or script). Consumes over the CLI. It is the primary consumer, and that is why the CLI is the complete surface.
- **U2b, agent with no shell.** Consumes over MCP. It is the reason the plug exists: automatic tool discovery in third-party agents, shell-less surfaces, and permission ergonomics.

The laboratory today supports five agent runtimes in its MCP config installer: `codex`, `claude`, `opencode`, `hermes`, `pi` (`py/roca/mcp/config_installer.py:85-91`).

### 2.2 The real jobs

Each job is declared with the evidence that proves it is real, not with a hypothesis. The evidence is the query templates the laboratory trained (34 labels in `py/roca/queryplan_examples.txt`), the production database's distribution, and the reference flow.

**J1. Pick the thread back up (catch-up).** "Where did this get to?" It is the operator's start-of-day job and the agent's start-of-session job.
Evidence: `latest_handover_by_project` is the template with the most training examples in the corpus (32 out of 406 lines), followed by `latest_sessions_by_project` (more than 30). A product does not train thirty variants of a question nobody asks.

**J2. Look up a past decision.** "What did we decide about X, and why?" It is the job that justifies having a database and not a notes file.
Evidence: `search_all_sources_by_term` (31 lines) and `search_memories_by_term_and_layer` (50) add up to 81 lines of the corpus, the largest search block, and `latest_memories_by_layer` is the single most trained template, with 69. In the real database, the `feedback` (165 memories) and `project` (107) layers are 82% of the 333 memories, and they are exactly the decision and correction layers.
In the Mini battery, the query *"what port does the roca write worker use?"* was resolved down this route in **16 ms** and returned five rows, one with the correct value inside.

**J3. Give a new session its context.** An agent that starts up automatically receives what it should already know: the rostered pills and the recent handovers, under a character budget measured so as not to flood the context window.
Evidence: `py/roca/hooks/context.py:1-43` implements exactly that contract, with a limit of three handovers and a measured injection budget (`py/roca/hooks/budget.py`). It is the only job served today without anyone typing a command.

**J4. Write into the common memory.** An agent or a human stores a memory, a correction, a pattern or a handover, and the rest of the fleet sees it.
Evidence: 333 memories in production, 311 written by `claude-code` and 20 by `codex`, that is, real writing by agents and not only by humans.

**J5. Correct the router without redeploying (teach).** When a question is routed badly, the operator teaches the correct example and the classifier absorbs it without recompiling or reinstalling anything.
Evidence: the command exists (`py/roca/cli.py:388`), the MCP tool (`roca_teach`), the table (`queryplan_teach_examples` in `py/roca/schema.sql:192`) and the in-place retraining. It was confirmed in writing twice as wanted functionality.
**Honest warning:** in the production database, `queryplan_teach_examples` has **0 rows**. The capability exists and has never been exercised. This does not take it out of scope (it is a closed decision), but it turns "the first real teach in production" into a v1 success metric and not an assumption.

**J6. Audit the fleet.** "How many sessions did each agent open?", "how long does it take to answer?", "which tools does it use most?", "how much does it think?".
Evidence: **twenty of the 34 templates** are fleet aggregates and counts (four `avg_*`, twelve `count_*`, three `top_*` and `distinct_memory_projects`), among them `count_sessions_by_source_agent`, `avg_response_latency`, `count_self_initiative`, `top_tool_uses_by_name` and `avg_thinking_depth`. In production there are 142,696 tool calls and 90,915 reasoning blocks to ask about.

**J7. Install and uninstall leaving no trace.** The operator wants to try the product on their machine and be able to remove it whole.
Evidence: the reference flow includes `roca uninstall` with the answer `n` to the question about keeping data, and the two defects that hurt most (D-6 and D-7) are exactly of this family. The battery verified five purges, all five with zero residue.

### 2.3 Jobs v1 recognizes but does not serve

- **Coordination between agents** (inbox, reviewable proposals). Decision: v2. The evidence that backs it: 0 proposals and 0 messages in the real database.
- **Media understanding** (video, vision). Decision: an optional companion, outside the core binary.
- **Memory shared between machines.** There is no hosted service and no synchronization. La Roca is local by design.

---

## 3. v1 scope

### 3.1 Memory: ingest and store

**Requirement M1. Source matrix.** v1 ingests the agent artefacts confirmed by
the product decision. The 2026-08-05 scope addendum removes repository
`AGENTS.md` and `CLAUDE.md` files as content while keeping Claude memory artefacts and every
session adapter. `workspace_roots` remains only for session project identity.

| Family | What is read | Verified in production |
|---|---|---|
| Claude Code | JSONL sessions under `~/.claude/projects` and per-project memory files | 1,358 sessions, 311 memories |
| Claude Desktop | sessions under `Application Support/Claude/claude-code-sessions` | yes, same table |
| Cowork | sessions under `Application Support/Claude/local-agent-mode-sessions` | yes, same table |
| Codex | sessions plus memory, rule and skill files under the Codex root | 20 memories |
| Subagents | subagent files resolved from their roots | yes |
| OpenCode | OpenCode's own SQLite databases | 2,142 sessions in six modes |
| Pi | Pi session files | 351 sessions |
| Hermes | Hermes session SQLite database | 92 sessions in four models |

Source: `py/roca/ingest/cron.py:646-715` (`run_ingest` and its sweep) and the real per-`source_agent` count of the production database.

**Requirement M2. Idempotency with a single contract.** Re-running the ingest over the same disk neither duplicates rows nor rewrites what is already normalized.
A note on debt v1 does not inherit: today there are two idempotency mechanisms. The live ingest route keeps per-file fingerprints in `ingest_file_state` (`py/roca/ingest/ingest_live.py:66-96`) and the full reconciliation route does not touch it, relying on idempotent writes instead. The table is **empty in the production database** despite there being 3,943 sessions. v1 defines **one** idempotency contract for both routes.

**Requirement M3. Normalized schema and adoption of existing databases.** The laboratory declares thirteen tables (`py/roca/schema.sql`). v1 ports the seven that carry real load or serve a v1 job: `sessions`, `memories`, `exchanges`, `thinking_blocks`, `tool_uses`, `layers`, and `queryplan_teach_examples` for the teach job. Of the remaining six, `messages`, `proposals` and `proposal_annotations` serve `inbox` and `proposals` and go to v2; `runs` and `run_logs` are the execution log, a v2 candidate; and `ingest_file_state` is redesigned under requirement M2. Four of those six (`messages`, `proposals`, `runs` and `ingest_file_state`) have 0 rows in the production database. A database created by an earlier version is **adopted**, not rejected: the schema comparison is structural (columns and types), never textual, and orphan tables from withdrawn features are reported and archived only with explicit consent. It is the D-4 contract, verified on the Mini with a real aged database.

**Requirement M4. Semantic layers.** The layer vocabulary is versioned data, not code. The laboratory declares twelve (`user`, `feedback`, `project`, `pattern`, `pill`, `discovery`, `handoff`, `handover`, `question`, `review`, `issue`, `protocol`) in `py/roca/layers.yaml`. v1 ports the layers that serve v1's jobs; the coordination layers (`question`, `review`, `issue`) are declared but stay inert until v2.

**Requirement M5. Concurrent writing with no daemon.** Several independent processes write the same database at once without any of them seeing `database is locked` and without losing transactions. The technical contract is SQLite in WAL, a generous busy timeout, and `BEGIN IMMEDIATE` with bounded retry on contended acquisition.
Laboratory evidence: `py/tests/test_native_write_concurrency.py` launches eight real processes over a barrier. Before the fix, the measurement was of lost transactions and missing rows; the test now forbids them.

### 3.2 Query

**Requirement C1. The cascade.** A question passes through, in this order:

1. **Domain gate.** A question with no answerable shape is **named** as out of domain instead of guessed at. The corpus trains that refusal explicitly (`out_of_scope`, 10 examples).
2. **Declared search.** When the question explicitly asks for a search by term, it compiles directly with confidence 1.0.
3. **fastText classifier.** 34 templates, confidence threshold 0.85 (`py/roca/core/query.py:60`). When it classifies above the threshold and the template compiles to valid SQL, that is the answer.
4. **Term rescue.** Free text the classifier could not place is searched for instead of being handed to the model.
5. **Model (LLM).** Only what survives everything above. It generates SQL, which is validated before it runs.

Source: `py/roca/core/query.py:1091-1154`.

**Requirement C2. Every route returns provenance.** Each answer declares which route it left by (`path: compiler` or `path: llm_fallback`), with what confidence, which template or which SQL, and how long it took. Measured today in this worktree: `path: compiler`, `confidence: 0.9983`, `latency_ms: 66.2` for *"count all memories"*.

**Requirement C3. The SQL is always validated, wherever it comes from.** Only `SELECT`, over an allowlist of tables and columns, with a limit. No model-generated SQL runs without passing that gate. It is the piece all five audits agreed on calling the laboratory's jewel.

**Requirement C4. Model adapters: frontier with a local floor.** The decision, 2026-08-05.

- With a credential and network, the frontier adapter is the **default** (for example `gpt-5.6-luna` or DeepSeek).
- With no network or no credential, the cascade falls **automatically** to the local floor: Ollama with `qwen3.5:4b`.
- The local one is the guaranteed floor, **not the product's identity**.
- With no frontier and no local, the product fails with a message that names the file, the missing key and the exact command that fixes it. It fails clearly, never opaquely.

The laboratory already has the seat for this abstraction: a portable provider contract with configurable order (`py/roca/kernel/llm_providers.py:83-131`), whose default order today is `ollama, http, mlx` on macOS and `ollama, http` elsewhere. v1 inverts the default so the frontier goes first when there is a credential.

**Requirement C5. Golden query bench.** Each adapter's relevance is measured with a golden query bench: between 15 and 25 real questions, each with its relevance criterion declared, run against each adapter. It is the instrument that turns "the local model answers worse" from an opinion into a number, and it is also the gate that decides whether a new adapter gets in.
**Privacy constraint, mandatory:** the bench and the training corpus may not contain private vocabulary. Today the laboratory's corpus breaks this and it is a direct blocker for a public repo: of 406 lines, **85 contain client and private project names** (`galactic` 36 times, `plus500` 31, `wallpapi` 19), and the file is distributed as package data (`py/pyproject.toml`, `package-data` section). The v1 corpus is rewritten with synthetic vocabulary.

**Requirement C6. Honest deterministic mode.** There is a mode that answers only with the compiler and never calls a model. That mode, when it cannot answer, must say the compiler does not know how to answer that question, and not report a configuration error.
Defect reproduced today in the laboratory (see Annex B, finding L-1): with the deterministic-mode flag on, an unclassified question returns `error: LLM not configured`, which is the "you have no model configured" message. It confuses the operator's intent with an installation defect.

### 3.3 Teach

**Requirement T1.** The operator or an agent teaches a pair (example question, existing template). The example is persisted, the corpus is refreshed and the classifier is retrained with no redeploy and no reinstallation.

**Requirement T2.** The effect is observable: the same question that used to fall to the model now resolves through the compiler, and the answer declares it in its provenance. A teach that changes nothing's routing is not a teach, it is a row.

**Requirement T3.** Retraining is cheap and bounded. Measured today: training the classifier from scratch costs **611 ms** on first use (against 66 ms with the model already cached) and produces a quantized artefact of **66,776 bytes** from a 20,803-byte corpus, with 34 labels and 261 vocabulary words. A 66 KB model fits comfortably in a static binary.

### 3.4 v1 CLI surface

The CLI exposes the whole kernel. `roca --help` shows only the daily nine:
`init`, `query`, `store`, `teach`, `ingest`, `login`, `doctor`, `update` and
`uninstall`. The remaining commands below stay callable but hidden. v1 commands,
grouped by function:

**Kernel (the product):**

| Command | Job |
|---|---|
| `roca query <question>` | J1, J2, J6. With `--layer`, `--sql-only`, `--no-llm`, `--max-chars`, `--json` |
| `roca exec <SELECT>` | Run the SQL `--sql-only` printed, under the same read-only gate |
| `roca store` | J4. With `--layer`, `--content`, `--project`, `--origin`, `--source-agent`, `--status`, `--metadata`, `--supersedes` |
| `roca teach` | J5. With `--question`, `--template` |
| `roca layers` | List the layers and their contents |
| `roca health` | Non-destructive checks over live data |

**Lifecycle and data:**

| Command | Job |
|---|---|
| `roca init` | Product bootstrap: config, database, model gate, first ingest. Idempotent |
| `roca ingest` | J1 to J6. Full reconciliation from disk. With `--dry-run`, `--workspace-root`, `--json` |
| `roca mcp serve` | Serve MCP over stdio, on demand, in the foreground (see 3.5) |
| `roca doctor` | Configuration and availability diagnosis |
| `roca schema status` / `roca schema archive-orphans --yes` | M3. Structural comparison and consented archiving |
| `roca backup create` / `list` / `verify` | Dated, verified restore points |
| `roca update` | Update to the latest release. In a single binary that is replacing the file, verified by checksum, with rollback when the new one does not answer. It is in the reference flow (R1) |
| `roca uninstall` | J7. Interactive by default; `--keep-data` or `--purge` for scripts |

**Agent integration:**

| Command | Job |
|---|---|
| `roca mcp install <runtime>` / `status` | Declare La Roca in the config of `codex`, `claude`, `opencode`, `hermes`, `pi`, preserving the rest of the file byte for byte |
| `roca hook install <runtime>` / `uninstall` / `status` | J3. Declare the session-lifecycle hooks (see open decision A-1) |
| `roca hook context` / `handoff` / `record` / `run` | J3. The context transport between sessions |

**Cross-cutting CLI contracts, mandatory in v1:**

- Every kernel command reads **one** database (`--db-path`), answers `--json` when asked, and audits its call with origin `cli` just as the plug audits with origin `mcp`.
- No kernel command loads the heavy stack (MCP server, config installer, model stack, ingest) at startup. The laboratory pins it with a test (`TestTransportOverhead`, `py/tests/test_hooks_cli.py:347-372`) because the CLI is on the critical startup path of every agent session.
- A command that fails names the file, the key and the command that fixes it.

### 3.5 v1 MCP surface

**Requirement P1. Between 3 and 6 tools, all passthrough.** The laboratory is already at the right number after the pruning: exactly **6** (`py/roca/plugins/core/plugin.py:63-68`), and each handler is a single call into the service, with no logic of its own (`py/roca/plugins/core/plugin.py:12-58`).

| Tool | Who it is for |
|---|---|
| `roca_exec` | Run the SQL from `roca_sql` under the read-only gate without needing a shell |
| `roca_query` | The product's job: answering from memory |
| `roca_store` | The other half: writing a memory |
| `roca_teach` | Correcting the compiler in place |
| `roca_health` | Live data health for an agent that cannot run `roca doctor` |
| `roca_sql` | Compile without running, and a probe for compiler and model availability |

Decision A of 2026-08-05 adds `roca_exec`, so v1 has **six** tools and leaves `roca_list_runs` out until there is a real background execution to record. The `runs` table has 0 rows in the production database. Adding a tool is a product decision, and so is removing one nobody calls.

**Requirement P2. No logic of its own.** Behaviour that lives in an MCP handler is behaviour no shell, hook or script can reach. One object serves both surfaces.

**Requirement P3. Demonstrable parity.** The same question over CLI and over MCP returns the same result. It is a scenario in the acceptance suite.

**Requirement P4. stdio transport on demand, with no daemon of its own.** The MCP process is born when the agent launches it and dies when the agent closes it. There is no supervisor, no launchd or systemd unit, no readiness graph, no separate writer process. The stateless MCP spec of 2026-07-28 is the north star.
An honest transition note: the post-pruning laboratory keeps **one** optional resident process (the MCP server over HTTP on the canonical listening point) with a declared reason: so that several agents share one loaded model instead of each paying for it (`py/roca/service.py:1-23`). In v1 that reason disappears: the frontier is a remote API and the local floor is the Ollama daemon, which is already resident and already shared. La Roca does not need one of its own.

### 3.6 Installation and uninstallation

**Requirement I1. Installing is copying a binary.** One static file per platform, with its checksum, produced by the release channel (GitHub Actions). The rule: the artefacts are produced by the channel, not by hand-made builds uploaded as a release.

**Requirement I2. The installer converges on what it finds.** An installation interrupted halfway, reinstalled on top, converges to a complete and usable state. Verified on the Mini with `kill -9` after three seconds, which skips the script's cleanup trap: a half release was left behind, the active link kept pointing at the complete one, `roca --version` kept answering, and the reinstallation converged with exit 0.

**Requirement I3. Uninstalling is deleting the binary and, with confirmation, the data.** The question to the operator is explicit and the answer `n` purges. The purge enumerates every path it deletes, is re-runnable, and leaves verifiable zero residue. An artefact La Roca did not create is reported and left intact; that refusal is not relaxed, what is removed is the race that triggered it falsely (D-7 contract).

**Requirement I4. Uninstalling reverts the integrations.** If the operator declared La Roca in their agents' config, `uninstall` removes it and leaves those configs as they were.

**Requirement I5. A single resolution of where the database lives.** Debt v1 does not inherit: today ten production modules independently resolve the path of `roca.db` (`diagnostics`, `backup`, `cli`, `lifecycle`, `core/service`, `core/notify`, `ingest/cron`, `hooks/hook_stop`, `kernel/db`, `kernel/deploy`), with at least three different origins: the environment variable, the canonical path under the home, and a per-installation path under the data directory. v1 has **one** function that answers that question.

**Requirement I6. Small configuration.** Debt v1 does not inherit: the laboratory's environment contract declares **98 variables**, of which 36 are public (`.slop/env-contract.yml`). v1 is configured through a TOML file and a handful of documented environment variables. A setting written at the root of the config document counts the same as one under the defaults section, and the latter wins on collision (D-1 contract).

### 3.7 Explicit v1 non-scope

Each exclusion with the decision it comes from. None is reopened in this document.

| Out of v1 | Destination | Decision |
|---|---|---|
| `inbox` (exchanges between agents) | v2 | v1 scope, 2026-08-05 |
| `proposals` (queue of reviewable proposals) | v2 | v1 scope, 2026-08-05 |
| Media: video and vision | Optional companion, outside the core binary | v1 scope, 2026-08-05 |
| A daemon of its own, supervisor, launchd or systemd, separate writer process | Does not exist | Daemonless transport, 2026-08-05; already executed in the laboratory (15 production modules and 5 service files deleted on the campaign branch) |
| Hosted service, synchronization between machines | Does not exist | Local product by design |
| Third-party plugin platform loaded in process | Does not exist in v1 | Consensus of the five audits; the media companion integrates as a separate binary |
| MLX in process | Out | It is a platform-specific local-model route; Ollama is the declared floor |
| `runs` and its log | v2 candidate | 0 rows in production; see the recommendation in 3.5 |

---

## 4. Canonical user walkthroughs

### R1. The reference installation, command by command (walkthrough)

This is the walkthrough that defines the product. It comes from a real shell history on the Mac mini over three working days, and any change that breaks it is a product regression, not a detail.

```sh
# 1. Installation from the public repository, piped to sh
curl -fsSL https://raw.githubusercontent.com/thellmwhisperer/la-roca/main/install.sh \
  | sh

# 2. Immediate verification
roca --version
roca                # bare, to see the help
roca doctor

# 3. Startup
roca init
roca doctor         # second pass, post-init
roca mcp serve      # foreground stdio server, launched on demand by the agent
```

Integrations also run:

```sh
roca mcp install codex
roca mcp install claude
roca mcp install opencode
```

Other operations exercised:

```sh
roca mcp serve
roca ingest
roca update
roca uninstall      # including the answer n to the purge question
```

**Five requirements this walkthrough imposes and that v1 must meet:**

1. **The public one-liner is the primary installation route.** Private forks and mirrors remain supported through `GITHUB_TOKEN` and `--repo`, but their authenticated API route is not the public product's canonical first-run story.
2. **`roca` with no arguments shows the help and is not an error.** It is the first thing the operator types.
3. **`roca doctor` is run twice: before and after `roca init`.** Its output has to be useful in both states, and it may not name components the product no longer has (see finding L-2 in Annex B).
4. **`roca update` and MCP serving are commands the operator already uses.** The v1 spellings are `roca update` and `roca mcp serve`; serving belongs under the hidden MCP integration group rather than the root menu.
5. **Updating is a walkthrough, not a detail.** Updating from an earlier version to the campaign one is part of acceptance, and in a single binary that means replacing the file with checksum verification and rollback when the new one does not answer.

### R2. The operator's catch-up (J1)

```sh
roca query "cual es el ultimo traspaso del proyecto X"
roca query "ultimas sesiones del proyecto X"
```

Contract: resolved by the compiler, provenance declared, and below the fast-route latency bar. With no network and no credential it works the same, because both questions are among the trained templates.

### R3. Shell-less agent over MCP (U2b, J2)

1. The agent discovers the tools over stdio when its MCP client starts.
2. It calls `roca_query` with a short question.
3. It receives rows with real content, with `isError: false`, with their provenance and their latency.
4. The same question through `roca query` in a shell returns the same result (parity, P3).

Verified on the Mini against a real client: the question *"what files did I edit recently?"* left by `path: llm_fallback`, with SQL generated by the local `qwen3.5:4b`, `latency_ms: 22533`, `confidence: 0.98`, eight rows with real ingested content.

### R4. Teach (J5)

1. The operator sees a badly routed answer, or a question that fell to the model when it could have been resolved fast.
2. `roca teach --question "<the question>" --template "<existing template>"`.
3. The same question, repeated, now leaves by `path: compiler`.
4. The effect survives a process restart and requires no reinstallation.

### R5. Clean uninstall (J7)

```sh
roca uninstall
# Keep the Roca database and data at <path>? [Y/n]: n
```

Contract: `stopped: yes`, `purged: yes`, an explicit list of deleted paths, and a subsequent check that finds not one file, not one process, not one retained port, and not one La Roca entry in any agent's config.

### R6. With no network and no credential

1. The operator starts up offline.
2. `roca query` works: the cascade uses the compiler for what it knows, and falls to the local floor for the rest.
3. No operation hangs waiting for a network that is not there: the frontier's failure is fast and the fall to local is automatic and visible in the answer's provenance.

---

## 5. Non-functional requirements

Every bar declares its origin. **Measured** means there is a real number behind it; **target** means it is a v1 requirement with no prior measurement, and that acceptance will measure it.

### 5.1 Latency

| Requirement | v1 bar | Origin |
|---|---|---|
| Startup of a command that touches neither the database nor the model (`roca --version`) | p95 <= 30 ms; never worse than the Python laboratory | **Measured**: 70 ms warm in this worktree (0.21 s cold, 0.07 s the next two) |
| Query resolved by the compiler, end to end including process startup | p95 <= 100 ms | **Measured**: 16 ms internal latency on the Mini over MCP; 66.2 ms internal and 100 ms wall clock over CLI in this worktree |
| Query that falls to the local model (`qwen3.5:4b`) | no regression against 22.5 s; target p50 <= 15 s | **Measured**: 22,533 ms on the Mini |
| Query that leaves by the frontier | **Target**: p50 <= 3 s | No prior measurement. Acceptance measures it per adapter |
| Classifier retraining after a teach | <= 1 s | **Measured**: 611 ms from scratch, loading included |
| Incremental ingest of one working day | <= 30 s | **Measured**: on the Mini, a delta of 4 sessions, 104 exchanges, 285 reasoning blocks and 205 tool calls |

### 5.2 Footprint

| Requirement | v1 bar | Origin |
|---|---|---|
| Binary size per platform | **Target**: <= 50 MB, goal 25 MB | Replaces a **measured 103 MB** virtual environment plus a system interpreter |
| Resident memory of a query process | <= 64 MB peak | **Measured**: 30.85 MB peak with the classifier loaded |
| Classifier artefact | <= 1 MB | **Measured**: 66,776 bytes (quantized) from a 20,803-byte corpus |
| Runtime dependencies on the user's machine | **Zero** for the compiler route. Ollama only when the local floor is used | Single-binary decision. Today: 5 Python dependencies plus the interpreter |
| Resident processes of its own | **Zero** | Daemonless transport decision |

### 5.3 Scale

| Requirement | v1 bar | Origin |
|---|---|---|
| Database size supported without degrading the latencies in 5.1 | >= 1 GB | **Measured**: the reference production database weighs 685,137,920 bytes |
| Row volume | >= 500,000 conversation rows | **Measured**: 306,281 rows today (3,943 sessions, 72,670 exchanges, 90,915 reasoning blocks, 142,696 tool calls), accumulated in a little over six months |
| Simultaneous writers with no loss and no visible error | >= 8 independent processes | **Measured**: the laboratory's concurrency test launches 8 real processes over a barrier |
| Corpus time horizon | >= 12 months without re-pruning | **Measured**: 2026-01-21 to 2026-08-04, six and a half months for 685 MB |

### 5.4 Installability and reversibility

| Requirement | v1 bar | Origin |
|---|---|---|
| Installation | Copy a binary. Zero steps requiring an interpreter, a package manager or a service | Single-binary decision |
| Interrupted installation | Reinstalling on top converges, with exit 0, and the active binary keeps answering throughout | **Measured** on the Mini with `kill -9` after 3 s |
| Uninstallation | Verified zero residue, enumerated path by path, re-runnable | **Measured**: 5 purges out of 5 with zero residue |
| Full cycle on a virgin machine | 2 cycles out of 2 green, with no manual intervention | **Measured** on the Mini |
| Integration rollback | The agents' configs are left as they were | Requirement from the reference flow |

### 5.5 Robustness and observability

| Requirement | v1 bar | Origin |
|---|---|---|
| A value persisted by the operator that this version does not understand never brings startup down | A warning that names key, file and remedy; the rest keeps loading | D-5 contract |
| A caught exception never loses its trace | The one-line diagnosis reaches the state; the full trace reaches the component's log | D-3 contract |
| No child process is launched into a null descriptor | Every child writes to a persistent log with rotation | Laboratory law |
| Nothing is signalled that the installation cannot prove is its own | Identity re-established from the live command line before every signal | D-6 contract |
| Stopping with the port held by a third party | FAILURE is reported, not success, and the third party is not touched | D-6 contract |

### 5.6 Code portability and privacy

| Requirement | v1 bar | Origin |
|---|---|---|
| Zero absolute home, mount or private-service paths in production code | Deterministic guard in CI | Laboratory OSS law |
| Zero private vocabulary (client, project, login, corpus) in code, package data or query bench | Human review plus a synthetic corpus | **Open finding**: 85 of the current corpus's 406 lines break it |
| Zero agent artefacts in the repository | Deterministic guard in CI | Laboratory law |
| Declared and verified release matrix | darwin-arm64 first; then linux-arm64 and linux-x64 | The laboratory's current matrix, with darwin-x64 deliberately unpublished |

---

## 6. v1 acceptance criteria

### 6.1 The executable contract

v1's acceptance **is** the La Roca v1 Gherkin suite, written before the code, black box over the `roca` binary and its MCP over stdio, with no knowledge of internals. This PRD does not reproduce it: it references it as the contract and sets its mandatory scope.

The suite covers, at a minimum:

- The complete installation cycle from the Mini battery: virgin machine, `init`, serving on demand, stopping with no residue, uninstallation and purge to zero, a half installation that converges, an aged database adopted.
- The exact reference flow (R1), command by command, as a feature of its own.
- The eight defects D-1 to D-8 as named regression scenarios.
- The cascade's two routes, teach, and the golden query bench with its per-query relevance criterion.
- The model adapters: with a credential it uses the frontier; with no network it falls to local; with nothing it fails with a clear message.
- MCP: tool discovery, querying over stdio, and result parity between CLI and MCP.
- Concurrency: N agents writing at once with no corruption and no visible error.

Execution rule: acceptance runs with real Ollama, not simulated. A battery that is incomplete or degraded because the model was unavailable **is not green**.

### 6.2 The eight defects that may never be reborn

Each one has a behaviour contract, and each contract has its scenario in the suite. All eight died in the laboratory with evidence on a real machine. Seeing them again in the new product is a birth defect, not a bug.

| ID | Original defect | Contract v1 inherits |
|---|---|---|
| **D-1** | `init` did not read `workspace_roots` from the config, so encoded session paths lost their project identity | A key written at the root of the config document resolves the same as one under the defaults section, and the latter wins on collision. Sessions under the selected roots receive project identity; repository files under those roots are not ingested. |
| **D-2** | `start` failed on a virgin machine: dead components and connection refused | Clean startup on a virgin machine, with no problems reported, two cycles out of two |
| **D-3** | "unhandled errors in a TaskGroup (1 sub-exception)" swallowed the real exception | The real exception travels attached to the diagnosis. Verified: `ConnectError: All connection attempts failed` surfaces |
| **D-4** | Textual schema comparison: a production database identical column by column rejected over orphan tables and formatting noise | **Structural** comparison. An aged database is **adopted**. Orphans are reported and do not block. Archiving is explicit and takes a backup before renaming, never deletes |
| **D-5** | An obsolete plugin name in the user's config killed the whole runtime | The operator's persisted data never kills the runtime: a warning that names key, file and remedy, and the known ones keep loading. **The provenance travels with the value**, or the degradation is only local: that was the second failure, and the one that had to be fixed separately |
| **D-6** | `stop` left an orphan writer process holding the port | Always a clean stop: zero processes, port free. Nothing is signalled that the installation cannot prove is its own. With the port held by a third party, FAILURE is reported |
| **D-7** | `uninstall --purge` failed with "own artefact appeared after the snapshot" | An idempotent, re-runnable purge that captures its own state after creating its artefacts, not before. The refusal to delete what is not its own is kept; what is removed is the race |
| **D-8** | Full cycle on a fresh machine: install ok, start fails, uninstall fails. Zero clean steps out of three | The whole cycle on a virgin machine is green from start to finish, twice in a row |

### 6.3 v1 release gates

v1 is not declared ready until **all** of them pass:

1. The complete Gherkin suite green over the real binary, with real Ollama.
2. The complete battery on the Mac mini (real hardware, virgin machine), two cycles out of two, with an honest verdict step by step.
3. The golden query bench run against each adapter, with the relevance number published per adapter.
4. A concurrency test with at least eight real processes, with no lost transactions and no visible `database is locked`.
5. Zero residue after purge, verified path by path.
6. The OSS portability and private-vocabulary guards green.
7. Release artefacts produced by the GitHub Actions channel, with checksums, for the declared matrix.

The headline law: if a single gate fails, the verdict is **DOES NOT MEET**, and what is green goes underneath.

---

## 7. Success metrics and risks

### 7.1 v1 success metrics

**Product metrics (the ones that say whether it is useful):**

| Metric | v1 goal | Baseline today |
|---|---|---|
| Questions resolved without reaching the model | >= 60% of real queries | Not instrumented. The 34 templates cover the six jobs |
| Golden query bench relevance, local floor | >= 80% of golden queries with an acceptable answer | Not measured. It is v1's new instrument |
| Golden query bench relevance, frontier | >= 95% | Not measured |
| First real `teach` executed by the operator in production | >= 1 | **0** rows in `queryplan_teach_examples` in the real database. This is the metric that says whether teach is functionality or decoration |
| Queries per operator working day | >= 5 | Not instrumented |

**Quality metrics (the ones that say whether it is a good piece of code):**

| Metric | v1 goal | Baseline |
|---|---|---|
| Production code size | 7,000 to 11,000 lines | Consensus of the five audits. The laboratory is today at **29,785** production lines in 98 files, after coming down from 34,206 with the pruning |
| Full green cycles on a virgin machine | 2 out of 2 | **Achieved** in the laboratory |
| Defects D-1 to D-8 reborn | 0 | **0** in the laboratory |
| Public environment variables | <= 12 | **36** public out of 98 declared in the laboratory |
| MCP surface | 6 tools | **6** in the laboratory after the pruning, down from 19 |

### 7.2 Risks

**R1. Local model relevance. High probability, high impact.**
The local floor is `qwen3.5:4b`, a small model, and translating natural language into SQL over a seven-table normalized schema is not an easy task for it. If the floor answers badly, the product looks broken exactly when the user needs it most: with no network.
*Mitigation:* the golden query bench. 15 to 25 real queries with a declared relevance criterion, run against each adapter, publishing the number per adapter. The cascade mitigates by design: the more the deterministic compiler resolves, the less the quality depends on the small model, and `teach` is the lever the operator has to move that boundary without waiting for a release. Second mitigation: the answer always declares which route it left by, so a poor result is attributable rather than mysterious.

**R2. fastText inference in Go. Medium probability, high impact.**
The classifier is wanted functionality and deleting it is vetoed, but fastText is a C++ library with a Python binding. Porting it to Go has three routes (our own linear inference, cgo, or an alternative format) and none of them is free. If this gets stuck, the whole fast route gets stuck.
*Mitigation:* the artefact is small and the problem is bounded: 66,776 bytes, 34 labels, 261 vocabulary words. Inference of a supervised fastText classifier is an average of n-gram vectors plus a softmax: it is portable. The tech spec must close the route with a reasoned recommendation. If the chosen route breaks in-place retraining, `teach` stops being instantaneous and that has to be said before promising it, not after.

**R3. Installability regression when changing channel. Medium probability, high impact.**
The current installer is hardened against failures that cost blood: checksum verification before touching anything, active-version switching by atomic rename, convergence over an interrupted installation. A new single-binary installer may look trivial and lose those guarantees.
*Mitigation:* the Gherkin suite translates each of those guarantees into a scenario, and the Mini battery is repeated whole over the new product. The `kill -9` mid-installation scenario is mandatory.

**R4. Drift between documentation and code. High probability, medium impact.**
It is already happening in the laboratory: `roca doctor` talks about a native supervisor the pruning deleted, and the lost-transaction measurement from the concurrency test appears with two different numbers in the same tree (see Annex B, findings L-2 and L-3).
*Mitigation:* a number or a component name that appears twice is debt, and the rule forbids documenting it as a convention. The v1 rule: every operational claim has a single source, and the documentation points at it instead of copying it.

**R5. The private corpus blocks opening the repo. High probability if nobody acts, high impact.**
85 of the 406 lines of the training corpus distributed as package data contain client and private project names. The OSS portability guard does not detect it by design, because detecting it would require embedding the private vocabulary in the detector.
*Mitigation:* the v1 corpus is written synthetic from the very first commit, and so is the golden query bench. It is birth work, not later cleanup: once published, the history already contains it.

**R6. Scope widening through the back door. Medium probability, medium impact.**
`inbox` and `proposals` are out of v1, but their tables and their commands exist in the laboratory and are easy to port "while we are at it".
*Mitigation:* the laboratory's rule already works and is inherited: adding a tool or a command is a product decision, and there is a test that refuses the drift back. The production evidence backs the exclusion: 0 proposals, 0 messages, 0 runs.

**R7. The absence of the media companion leaves a discovery gap. Medium probability, low impact.**
The laboratory offered media integration, while v1 keeps media outside the core binary and exposes no plugin command family.
*Mitigation:* public documentation names the separate companion and its installation route when one is available; the CLI does not advertise commands it does not implement.

---

## 8. Open decisions

These are the only decisions this PRD does **not** take, because they are product decisions for the owner to take. None reopens an already closed decision. All three are recorded as structured items in the backlog, with durable identity: `laroca-prd-decision-hooks-scope-v1`, `laroca-prd-decision-cli-lifecycle-surface` and `laroca-prd-decision-public-repo-identity`.

**A-1. Do the session-lifecycle hooks enter v1?**
The decided scope is "memory + query + teach", and `inbox` and `proposals` go to v2. The hooks appear on neither list. But the hooks are the transport for job J3 (context between sessions), which is the only job served on its own without anybody typing a command, and the pruning law says hooks are kept as functionality. In the laboratory they are 1,795 lines and support a single runtime (`claude`), against the MCP installer's five runtimes.
Options: (a) they enter v1 complete; (b) they enter v1 only for the `claude` runtime, which is the only one supported today; (c) they go to v2 with `inbox` and `proposals`, and v1 serves J3 through `roca query`.
The PM's recommendation: **(b)**. J3 is a real and demonstrated job, and limiting it to `claude` reflects what actually works today without inventing scope.

**A-2. Resolved: what is the CLI's lifecycle surface in a daemonless product?**
`roca mcp serve` is the only serving command. It runs MCP over stdio in the
foreground and on demand; there is no resident process. The integration group is
hidden from root help while the command remains callable by agent configurations.

**A-3. The public repository's identity.**
The name, owner and visibility of the "La Roca" repo were explicitly left pending consent in the decision of 2026-08-05. It blocks publication, not construction: the start is a local git repo with no remote.

---

## Annex A: method and evidence

### A.1 What I did

Produced in a disposable reference worktree: read mode over the sources, execution mode over the reference. Nothing pushed to any remote, no PR opened, no writes outside the worktree beyond this report.

1. I read the five decisions, the Roca section of the essence contract, the complete cleanup-campaign log, the Mac mini battery record, the reference human flow and the five essence audits.
2. I checked out the campaign branch and read the real post-pruning code: CLI surface, MCP surface, query cascade, ingest matrix, schema, layers, model-provider contract, installer and uninstaller.
3. I ran the laboratory in an isolated HOME under `.tmp/` to measure startup, compiler-route latency, classifier training cost, artefact size and resident memory.
4. I read the reference production database **strictly read-only** (`sqlite3 "file:...?mode=ro"` with `PRAGMA query_only=1`), to contrast the v1 scope against real usage. Nothing was written.
5. I reproduced three minor laboratory defects the new product must not inherit (Annex B).

### A.2 Commands run and relevant outputs

**Checkout and size of the campaign branch:**

```
$ git fetch origin release/saneamiento-20260805 && git checkout FETCH_HEAD
HEAD is now at 577a40c refactor(mcp): un unico mensaje de solo-lectura para las dos superficies

$ git log --oneline b0387e7..HEAD | wc -l
52
$ git diff --shortstat b0387e7 HEAD
 150 files changed, 10428 insertions(+), 23749 deletions(-)
$ git diff --name-status b0387e7 HEAD | grep "^D" | wc -l
41
```

**Size of the post-pruning laboratory:**

```
$ git ls-files 'py/roca/*.py' 'py/roca/**/*.py' | xargs wc -l | tail -1
   29785 total
$ git ls-files 'py/roca/*.py' 'py/roca/**/*.py' | wc -l
      98
$ git ls-files 'py/tests/*.py' | xargs wc -l | tail -1
   46428 total
```

Contrast with the audits' starting point (2026-08-04): 34,206 production lines in 104 files, 57,582 of tests.

**Transport modules deleted by the pruning** (15 production plus 5 service files). Production: `supervisor.py`, `runtime_host.py`, `runtime_configuration.py`, `runtime_wiring.py`, `runtime_probes.py`, `write_endpoint_reclaim.py`, `runtime/__init__.py`, `runtime/write_queue.py`, `runtime/write_worker.py`, `mcp/readiness.py` and the five probes `compiler_model_readiness.py`, `cron_readiness.py`, `database_readiness.py`, `plugin_readiness.py`, `write_worker_readiness.py`. Service: `scripts/render_launchd.py`, the two example plists, the example systemd unit and the launchd README. With them fell 18 test files.

**CLI startup (isolated HOME, Python 3.14.6, Apple Silicon):**

```
$ /usr/bin/time -p python -m roca.cli --version   # three runs
reference 2.14.0 (577a40c90a6756527d98a941b103ee449f45266d)
real 0.21   /   real 0.07   /   real 0.07
```

**Query cascade, first run (includes training the classifier):**

```
$ python -m roca.cli query "count all memories" --no-llm --json
Read 0M words / Number of words: 261 / Number of labels: 34
{
  "confidence": 0.9983,
  "latency_ms": 611.3,
  "path": "compiler",
  "queryplan": "TEMPLATE=count_memories | LIMIT=1",
  "sql": "SELECT COUNT(*) FROM memories WHERE supersedes IS NULL LIMIT 1000"
}
```

**The same query warm, with resident memory:**

```
$ /usr/bin/time -l python -m roca.cli query "count all memories" --no-llm
"latency_ms": 66.2
        0.10 real         0.05 user         0.01 sys
    30851072  maximum resident set size
```

**Classifier artefact and virtual environment:**

```
$ ls -la <tmp>/roca-queryplan-runtime/<hash>/
-rw-r--r--  20803  queryplan_train.txt
-rw-r--r--  66776  queryplan.ftz
$ du -sh py/.venv
103M
```

**Production database, read-only:**

```
$ ls -la <reference-db>
-rw-------  685137920  4 Aug 21:47

$ sqlite3 "file:<reference-db>?mode=ro" "PRAGMA query_only=1; ..."
memories|333        sessions|3943       exchanges|72670
thinking_blocks|90915                   tool_uses|142696
proposals|0         runs|0              messages|0
queryplan_teach_examples|0              ingest_file_state|0

# memories per layer
feedback|165  project|107  pattern|52  user|8  handoff|1

# sessions per source agent
opencode-build|1617  claude-code|1358  opencode-explore|372  pi|351
opencode-general|98  hermes-deepseek-v4-pro|85  opencode|41  opencode-explorer|7
opencode-gordon|6  hermes-deepseek-v4-flash|3  hermes-glm-5.2|2  opencode-plan|1
hermes-unknown|1  hermes-default|1

# memories per source agent
claude-code|311  codex|20  config|2

# time range
2026-01-21T00:02:07.187000Z | 2026-08-04T16:01:03.311000Z
```

**Private vocabulary in the distributed corpus:**

```
$ grep -o -i -E "galactic|wallpapi|plus500" py/roca/queryplan_examples.txt | sort | uniq -c
  36 galactic
  31 plus500
  19 wallpapi
$ wc -l < py/roca/queryplan_examples.txt
     406
```

**Environment contract:**

```
$ grep -c "^- var:" .slop/env-contract.yml
98
$ grep -A1 "^- var:" .slop/env-contract.yml | grep "scope:" | sort | uniq -c
   2 scope: deleted   32 scope: internal   36 scope: public   28 scope: test
```

### A.3 Code references cited

| PRD claim | File and line |
|---|---|
| 6 MCP tools, all passthrough | `py/roca/plugins/core/plugin.py:12-68` |
| Definitions and reason for each tool | `py/roca/plugins/core/tools.py:1-19` |
| Complete CLI surface | `py/roca/cli.py:71-320` (lifecycle) and `:337-500` (kernel) |
| Query cascade | `py/roca/core/query.py:1091-1154` |
| Classifier confidence threshold (0.85) and model cache | `py/roca/core/query.py:59-60`, `:804-856` |
| Provider contract and default order | `py/roca/kernel/llm_providers.py:22-45`, `:83-131` |
| Ingest source matrix | `py/roca/ingest/cron.py:646-715` |
| Agent runtimes supported by the MCP installer | `py/roca/mcp/config_installer.py:85-91` |
| Hooks supported only for `claude` | `py/roca/cli.py:30` |
| Session context under a measured budget | `py/roca/hooks/context.py:1-43`, `py/roca/hooks/budget.py` |
| Schema and tables | `py/roca/schema.sql:4-203` |
| Twelve semantic layers | `py/roca/layers.yaml` |
| A single optional process, no supervisor | `py/roca/service.py:1-23` |
| Native concurrency with 8 real processes | `py/tests/test_native_write_concurrency.py:1-41` |
| The CLI off the hooks' hot path | `py/tests/test_hooks_cli.py:347-372` |
| The installer's security contract | `install.sh:1-22` |
| Idempotency fingerprint only on the live route | `py/roca/ingest/ingest_live.py:66-96` |

---

## Annex B: laboratory findings the new product must not inherit

Six findings reproduced today over the audited HEAD, with their evidence. Five are minor and block neither the campaign branch nor justify touching it: they are inputs for building v1 and, if warranted, small tickets against the laboratory. The sixth (L-6) is blocking, but for opening the public repository, not for the branch.

A work recommendation that should ship independently of La Roca: L-2 (the `roca doctor` text that names a deleted supervisor) and L-3 (one measurement with two values in the same tree) are small, localized corrections that are verifiable against the current laboratory, and L-3 is exactly the pattern the law against normalizing debt forbids leaving alone.

**L-1. The deterministic mode lies about why it does not answer.**
With the `--no-llm` flag, a question the compiler does not classify returns `error: LLM not configured`, which is the message of an installation with no model.

```
$ python -m roca.cli query "what files did I edit recently" --no-llm --json
error: LLM not configured
```

Origin: `py/roca/plugins/core/query_handlers.py:558-570`. The path that emits that message is the same with and without the flag, so the operator's explicit intent ("answer only with the compiler") and a configuration gap are counted the same. Requirement C6 of this PRD: they are two different messages.

**L-2. `roca doctor` names a component the product no longer has.**
The pruning deleted `supervisor.py` and the probe stack. The diagnosis the operator reads still talks about a native supervisor and per-component readiness:

```
[ok] service/lifecycle_supported_supervisor_not_probed:
     Lifecycle commands are supported; native supervisor state is not probed
     action: Run `roca status` to probe native supervisor and component readiness
```

Origin: `py/roca/diagnostics.py:332-337` and `:516`. `roca doctor` is the second and the fourth command the operator types in the reference flow, so it is high-traffic text.

**L-3. One and the same measured number, two different values in the same tree.**
The agent contract claims "eight simultaneous writers, 48 lost transactions" (`CLAUDE.md:205` and `AGENTS.md:205`), and the test that proves it documents "8 writers, 57 lost transactions, 114 rows missing" (`py/tests/test_native_write_concurrency.py:21`). Which number is correct is secondary: what matters is that there are two copies of one measurement and they have already diverged. It is exactly the pattern the rule forbids normalizing. In v1, a measured number lives in one place and the prose points at it.

**L-4. Ten resolvers for one question.**
Ten production modules independently resolve where `roca.db` lives: `diagnostics`, `backup`, `cli`, `lifecycle`, `core/service`, `core/notify`, `ingest/cron`, `hooks/hook_stop`, `kernel/db`, `kernel/deploy`. Requirement I5 of this PRD.

**L-5. Two commands the operator types that never existed.**
The reference shell history on the Mac mini contains `roca serve` and `roca update`. Neither of them is declared in the CLI, on the campaign branch or on `main`:

```
$ git show b0387e7:py/roca/cli.py | grep -n '"update"\|"serve"'
(no output)
$ grep -n 'add_parser("update' py/roca/cli.py
(no output)
```

The absence of a command the user already types is a product gap. `update` enters the v1 scope; serving enters as the hidden integration command `roca mcp serve`.

**L-6. The training corpus is distributed as package data with private vocabulary inside.**
`queryplan_examples.txt` is listed in `package-data` in `py/pyproject.toml`, so the 85 lines with client and private project names travel in every installation. The OSS portability guard does not detect it by design: detecting it would require embedding that vocabulary in the detector, which is precisely what the law forbids. It is a direct blocker for a public repository and the reason the v1 corpus is born synthetic (requirement C5).
