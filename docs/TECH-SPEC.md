# La Roca v1: technical specification

| Field | Value |
|---|---|
| Document | La Roca v1 tech spec (final product, new repo, Go, single binary) |
| Author | Tech Lead (disposable reference worktree) |
| Date | 2026-08-05 |
| Status | Draft for the product owner's review. After the blessing, it is consecrated in the product repo |
| Reference implementation | frozen Python reference, branch `release/saneamiento-20260805`, HEAD `577a40c` |
| Scope | v1 = memory, query and teach. It decides architecture, not product |

---

## 0. Purpose, scope and method

### 0.1 What this document is and is not

This document translates into architecture a set of decisions **already closed** by decision. It reopens none of them. Each section declares which decision it obeys and, where the Python laboratory already proved a semantics, cites the exact module of `release/saneamiento-20260805` that defines it, so the port has a verifiable reference instead of an interpretation.

The reading rule is: **the laboratory rules on semantics, this spec rules on form**. Where the laboratory says "a question of this shape is answered like this", La Roca copies it. Where the laboratory says "this is done with a supervisor, a worker and a TCP queue", La Roca does not copy it, because the pruning already showed that form was not necessary.

What this document does not do: it does not choose a repo name, it does not decide what happens to the operator's live production database, and it does not fix the default frontier provider. Those are section 7 items and they are recorded as the decisions.

### 0.2 Evidence base: what was done to write it

Everything claimed here comes from one of these four sources, all reproducible:

**a) Reading the real post-pruning code.** Checkout of the campaign branch in a disposable worktree:

```
$ git fetch origin release/saneamiento-20260805 && git checkout FETCH_HEAD
HEAD is now at 577a40c refactor(mcp): un unico mensaje de solo-lectura para las dos superficies
```

Inventory of the resulting tree:

```
$ find py/roca -name '*.py' | wc -l          ->  98 files
$ find py/roca -name '*.py' | xargs wc -l    ->  29,785 LOC
$ find py/tests -name '*.py' | xargs wc -l   ->  46,428 LOC
$ git ls-files | wc -l                       ->  296 tracked files
```

Contrast with the Opus 5 audit of `main` before the campaign (34,206 production LOC, 57,582 of tests, 336 files): the pruning has removed **4,421 production LOC and 11,154 of tests** without losing capabilities. That is the baseline the port starts from.

**b) Measuring the classifier.** The laboratory's real fastText classifier was trained and evaluated against its own corpus, with the repo's venv. The numbers are in section 4.2 and they are the foundation of the port recommendation.

**c) Campaign records.** The Mac mini battery record (`data/roca-cam-mini/informe-bateria.md`) contributes the only latencies measured against real hardware and a real model, and the D-1..D-7 matrix with evidence. The reference human flow contributes the command sequence the product has to support without friction.

**d) Third-party verification.** The existence and real capabilities of the Go libraries this spec proposes were checked before proposing them, in particular the absence of an authorizer in `modernc.org/sqlite`, which is what forces the SQL gate to be redesigned (section 1.6.3). No library is proposed without verification.

### 0.3 Closed decisions this spec obeys

| Decision | Source | Architectural consequence |
|---|---|---|
| Go, one static binary per platform | `decisions/la-roca-go-binario-20260805.md` | Sections 1, 6 |
| New repo with clean history; the Python reference remains read-only | the v1 decisions (Roca), same decision | The whole document |
| The CLI is the product; the MCP is a thin plug | `decisions/roca-cli-first-20260805.md` | Sections 1.7, 1.8 |
| v1 = memory + query + teach; inbox and proposals to v2 | `decisions/la-roca-alcance-v1-20260805.md` | Sections 1.2, 2.2 |
| Media is an optional companion outside the core binary | idem | Sections 1.1, 6.5 |
| Frontier with a local floor: provider by credential, cascade to Ollama | idem | Section 3 |
| fastText is wanted functionality; deleting it is vetoed | `decisions/roca-coordinacion-destilado-20260804.md` | Section 4 |
| Code gets pruned, functionality does not | `decisions/roca-poda-ley-funcionalidad-20260804.md` | Sections 2.1, 5.1 |
| No daemon of its own; process on demand | Tech Lead brief, coherent with the pruning already executed | Section 1.3 |
| The release artefacts are produced by the channel, not by hand-made builds | the v1 decisions (Delivery and testing) | Section 6 |
| D-1..D-8 and their contracts are the minimum quality bar | `data/roca-campana-release-20260805.md` | Section 8.5 |

---

## 1. General architecture

### 1.1 The product's form

La Roca v1 is **one static executable per platform and nothing else**. There is no interpreter, no virtual environment, no release tree, no mandatory resident process, no system service unit. Installing is copying a file onto the PATH; uninstalling is deleting it and, with explicit confirmation, deleting its data.

Two things stay outside the binary, by decision:

- **Media (video and vision)**: an optional companion installed separately. The core links nothing of vision or video, and does not declare its tools.
- **The language model**: it is always an external service, be it a frontier API or a local Ollama. The binary contains no LLM weights. The only model that travels inside is the route classifier, which takes about 120 KB (section 4.2).

What does travel inside, via `go:embed`, is everything the product needs to start with no network: the SQL schema, the layer registry, the semantic layer handed to the model, and the classifier's training corpus.

### 1.2 Layers

Four layers, with a strict dependency direction: the surfaces depend on the service, the service depends on the capabilities, the capabilities depend on the store. Nothing points upwards.

```
  surfaces         CLI (cobra)                MCP stdio (official Go SDK)
                        \                        /
                         \                      /
  service                 +--> Service <-------+     (one object, one operation per method)
                                  |
  capabilities     ingest ---- query cascade ---- teach ---- store
                                  |
  store                     embedded SQLite (WAL, busy_timeout, BEGIN IMMEDIATE)
```

The rule that makes this true and not a drawing is the one the laboratory already won in the pruning and wrote into its contract: **every MCP handler is a one-line passthrough over the service**. It is visible in the whole of `py/roca/plugins/core/plugin.py`: six handlers, none with logic. The test `TestServiceIsTheOneSurface` pins it. La Roca inherits that rule and its test: a capability that lives in the handler and not in the service is a capability a shell, a hook or a script cannot reach, and that contradicts "the CLI is the product".

**v1 scope within the capabilities.** `store`, `query` and `teach` are v1. `inbox` and `proposals` are v2 and do not exist in the v1 binary: no command, no table created, no MCP tool. Section 2.4 explains why that does not break the adoption of a laboratory database that does have them.

### 1.3 Process model: no daemon

**There is no daemon of its own.** Every invocation is a process that opens the database, does its work and exits. The MCP server is launched on demand over stdio, started by the agent that uses it, and dies when that agent closes the pipe.

This decision is coherent with what the pruning already did in the laboratory and with where the protocol is going:

- The laboratory already removed the supervisor, launchd/systemd, the readiness graph and the write worker. Its own contract declares it: "The runtime is one optional process, and nothing supervises it" (`AGENTS.md`, Roca-Specific Runtime Rules). The only resident process it has left exists for a reason that disappears in Go: sharing one model loaded in memory between several agents (`py/roca/service.py`, docstring). La Roca never loads an LLM in its own process, so there is nothing to share.
- The laboratory's MCP module already starts in stdio by default: `_module_transport()` in `py/roca/mcp/main.py:490` returns `"stdio"` unless explicitly overridden. The resident HTTP transport is the exception, not the norm.
- The 2026-07-28 MCP revision removes sessions at the protocol level: no `initialize` handshake, no session header, no GET stream. A server that keeps no state between requests gains nothing from being resident. The declared north star is that revision.

**Consequence for concurrency.** With no single writer, several processes write the same SQLite at once. That is not a theoretical risk: the laboratory measured it. Eight simultaneous writers lost 48 transactions before the fix. The configuration that solves it is in `py/roca/kernel/db.py` and is mandatory in the port, with its three pieces together:

1. `PRAGMA journal_mode = WAL` (`db.py:378`).
2. `PRAGMA busy_timeout = 15000` (`db.py:21`, applied at `:365` and `:379`).
3. `BEGIN IMMEDIATE` with bounded retry and jitter on contended acquisition (`db.py:558-559`, `_retry_while_busy` at `:390`).

The `IMMEDIATE` is not a style detail. A transaction that reads before writing takes a read snapshot, and the promotion fails immediately with `SQLITE_BUSY_SNAPSHOT`, which the busy handler **never retries**. A port that uses a plain `BEGIN` reproduces the measured bug. The proof has to be the same as in the laboratory: real processes over a barrier, not goroutines in the same process, because an in-process pool proves nothing here.

### 1.4 Query cascade

The laboratory's cascade has **five stages**, not four. The fifth (keyword rescue behind the LLM) is what makes the model path degrade instead of failing, and it is ported the same. The order is fixed by `try_queryplan` in `py/roca/core/query.py:1091-1154` and by `roca_query` in `py/roca/plugins/core/query_handlers.py:391`:

```
question
  |
  0. taught shortcut: if the normalized question was taught, it skips the gate
  |
  1. GATE (out_of_domain_template, queryplan.py:474)
  |     out of scope (greeting, write order, poem)             -> refusal with a reason
  |     ambiguous (stacks count + latest + list)               -> refusal with a reason
  |
  1b. DECLARED SEARCH (explicit_search_plan, queryplan.py:548)
  |     "what do we know about X", "busca en la roca X"        -> search template
  |
  2. fastText CLASSIFIER (predict_queryplan, queryplan.py:1269)
  |     confidence >= 0.85 and the template compiles           -> deterministic SQL
  |
  3. TERM RESCUE (term_search_plan, queryplan.py:561)
  |     free text with no structural markers                   -> search by term
  |
  4. LLM (roca_sql / roca_query, query_handlers.py:309 and :391)
  |     the model generates SQL, the gate validates it and it runs
  |
  5. KEYWORD RESCUE (keyword_fallback, query_handlers.py:111)
        the LLM failed or returned 0 rows                      -> direct search,
                                                                 match_mode=relaxed
```

The two latencies measured on the Mac mini with Ollama `qwen3.5:4b`, from the battery record, give the real scale: compiler path **16 ms**, LLM path **22,533 ms**. Three orders of magnitude. That is the reason the classifier goes first and the reason section 4 does not treat it as decoration: every question the classifier resolves is a question that does not cost 22 seconds.

The confidence threshold is 0.85 (`query.py:60`) and in La Roca it remains configurable the same way.

A detail that must be ported carefully: **what was taught beats the gate**. If the operator taught a phrase, `try_queryplan` does not pass it through the gate or the declared search (`query.py:1112`). It is deliberate: what the operator asserted is field truth, not a generalization by the classifier.

### 1.5 The SQL gate

Every piece of SQL that does not come from a deterministic template passes through a gate before touching the database. In the laboratory it is `validate_sql` (`py/roca/core/query.py:749-798`) and it does six things, in this order:

1. Parse with sqlglot and keep the first query; reject everything that is not a SELECT.
2. Four repairs over the AST, which exist because small local models emit syntactically invalid SQL in predictable ways: collapse repeated `ORDER BY`s, remove `ORDER BY` terms a compound SELECT cannot resolve, push a branch's `ORDER BY`/`LIMIT` into a subquery, and qualify ambiguous `ORDER BY` in joins.
3. Table allowlist.
4. `LIMIT` requirement.
5. Re-serialize in the SQLite dialect.
6. **Prepare that exact string** against an in-memory database with only the schema, under a `set_authorizer` hook, so that the table, column and function rules are imposed by the engine that is going to run the query and not by a similar-looking grammar (`_prepare_under_authorizer`, `query.py:712`).

Step 6 is what gives the strong guarantee: `{"valid": True}` means SQLite accepted this string, not that a validator believes it would. That property has to be preserved.

**The port's problem, and its solution.** The pure-Go SQLite driver that makes the static binary possible (`modernc.org/sqlite`) **does not expose `set_authorizer`**. This was verified in its published documentation: it exposes `Limit()` over `sqlite3_limit`, it exposes `_query_only` in the DSN, it exposes pre-update/commit/rollback hooks, and it exposes neither an authorizer nor per-table or per-column restriction. Linking the C library with cgo to get it back would destroy the static binary, which is the product's whole point.

The replacement keeps the "the engine says so" property by splitting the work in two:

- **Table and column existence, and syntax: the engine says so.** The statement is prepared against an in-memory database that contains **only the visible tables** from the catalog. A reference to a table the query must not see no longer needs to be forbidden by an authorizer: it simply does not exist in that database, and `prepare` fails. This is strictly stronger than an allowlist in the AST, because it also covers non-existent columns and ambiguities the parser does not see.
- **Write verbs, functions and `LIMIT`: the AST plus the engine say so.** The AST is walked with a SQLite parser in Go and the function allowlist and the `LIMIT` requirement are applied. On top of that, the execution connection is opened with `_query_only=1` and `sqlite3_stmt_readonly` is checked over the prepared statement, which is the engine's own assertion that the statement does not write.

For the AST, `github.com/rqlite/sql` is used: a pure-Go parser of the SQLite grammar with an AST and formatting, maintained inside the rqlite project. It is the piece that replaces sqlglot for the repairs and for the function allowlist.

**A sequencing decision, not a scope decision.** The four repairs in step 2 exist through empirical observation of what a small model emits. La Roca builds them **after** having the golden query bench (section 3.4) running against `qwen3.5:4b`, and builds only the ones the bench proves necessary. Porting four repairs with no data to justify them is copying scars; porting zero and going without them is repeating the cut. The bench decides, and the decision is recorded in the bench itself.

### 1.6 External dependencies

A closed list. Adding one is an architecture decision, not a convenience.

| Dependency | What for | Why this one |
|---|---|---|
| `modernc.org/sqlite` | Store | Pure-Go SQLite. It allows `CGO_ENABLED=0` and therefore a static binary and trivial cross compilation. Known limitation compensated for in 1.5 |
| `github.com/modelcontextprotocol/go-sdk` | MCP surface | The protocol's official SDK, maintained with Google. Brings `StdioTransport` and JSON schema generation from Go structs |
| `github.com/spf13/cobra` | CLI surface | The de facto standard for Go CLIs with subcommands and help; it is the shape operators recognize from the tools they use |
| `github.com/rqlite/sql` | SQL gate | Pure-Go parser of the SQLite grammar with an AST. A replacement for sqlglot |
| TOML and YAML | Config and embedded data | User config in TOML (compatible with the laboratory); embedded `layers.yaml` |

Everything else is the standard library. In particular: **zero LLM provider SDK clients**. The adapters speak HTTP with `net/http` and JSON with `encoding/json` (section 3.1). A provider SDK puts its dependency chain and its version cadence inside the binary in exchange for saving about two hundred lines.

### 1.7 CLI surface: the product

The CLI exposes the whole kernel. Root help exposes only `init`, `query`, `store`,
`teach`, `ingest`, `login`, `doctor`, `update` and `uninstall`; every other row
remains callable but hidden. The post-pruning laboratory inventory, read from
`py/roca/cli.py`, is the reference. Translated to the v1 scope:

| Command | Origin | Notes |
|---|---|---|
| `roca query <question>` | ported | The complete cascade. `--layer`, `--sql-only`, `--no-llm`, `--max-chars`, `--json` |
| `roca exec <sql>` | ported | Runs a SELECT validated by the gate. The natural companion of `--sql-only` |
| `roca store` | ported | Writes a memory |
| `roca teach` | ported | Teaches the classifier an example and retrains |
| `roca layers` | ported | The layer registry and its contents |
| `roca health` | ported | Live data checks, structured JSON |
| `roca ingest` | ported | Full reconciliation from disk |
| `roca init` | ported | Creates or adopts config and database |
| `roca doctor` | ported | Configuration and availability diagnosis |
| `roca mcp install <agent>` | ported | Installs Roca in an agent's MCP config. It is in the reference flow |
| `roca mcp status` | ported | Which agents have Roca configured |
| `roca schema status` / `archive-orphans` | ported | Database adoption (section 2.4) |
| `roca backup create/list/verify` | ported | The precondition of any in-place repair |
| `roca uninstall` | ported | With `--keep-data` / `--purge`, and an interactive question by default |
| `roca mcp serve` | **new** | MCP stdio server in the foreground, nested with the integration commands. The laboratory launches it as a module; in La Roca it is the only way to serve |
| `roca update` | **new** | Downloads, verifies the checksum and replaces the binary. It was run on the Mini and today it does not exist |
| `roca version` | **new** | Today there is only the `--version` flag (`py/roca/cli.py:70`). As a subcommand it also answers with the source SHA and the platform |
| `roca eval --golden` | **new** | Golden query bench per adapter (section 3.4) |
| `roca inbox …` | **v2** | Outside the v1 binary |
| `roca proposals …` | **v2** | Outside the v1 binary |
| `roca runs …` | **v2** | Outside the v1 binary |
| `roca start` / `stop` / `status` | **do not exist** | There is no daemon to start |
| `roca plugins …` | **does not exist in v1** | See below |
| `roca hook …` | see section 7 | An open decision |

The four new commands are not scope added through the back door: `mcp serve` is the same capability reachable today by invoking the MCP module, promoted to a command because in La Roca it is the only way to serve; `update` and `version` are in the reference flow and are a gap today; `eval` is the tool that makes section 3.4 measurable.

Two absences that deserve explicit justification, because the pruning law forbids losing functionality:

- **`start`/`stop`/`status` are not lost, they stop having a referent.** They were the capability "govern the resident process". With no resident process there is nothing to govern. What they really answered (is this installation healthy?) is answered by `roca doctor` and `roca health`, which are in v1.
- **`plugins` does not exist in v1 because the only real plugin is media, and media leaves the binary by decision.** A plugin system with zero plugins is the definition of ceremony. The media companion is installed and configured as what it is: another binary with its own MCP entry. If one day there is a second companion, the command comes back with a real referent.

Every kernel command's contract, inherited from the laboratory: it reads one database (`--db-path`), answers `--json`, and audits with `source: cli` just as the plug audits with `source: mcp`.

### 1.8 MCP surface: the plug

Six tools, exactly the ones decided for v1. Each one has to be defended by a concrete caller; adding one is a product decision.

| Tool | v1 | The caller that defends it |
|---|---|---|
| `roca_exec` | yes | Agents that receive SQL from `roca_sql` and have no shell; decision A, 2026-08-05 |
| `roca_query` | yes | The product's job: answering from memory to a shell-less agent |
| `roca_store` | yes | The other half: writing a memory back |
| `roca_teach` | yes | Correcting the classifier in place with no redeploy |
| `roca_health` | yes | An agent that cannot run `roca doctor` |
| `roca_sql` | yes | Agents that need to inspect SQL before `roca_exec` runs it; and a probe for compiler and model availability |
| `roca_list_runs` | **not in v1** | Its caller was the cron probe of a runtime that no longer exists, and `runs` is v2 |

Six tools in v1, at the top of the 3-to-6 band the decision fixes. stdio transport, on demand, with the official Go SDK. The protocol's north star is the stateless 2026-07-28 revision, which fits naturally with a process that keeps nothing between calls.

A single read-only message for both surfaces, as the laboratory has just fixed in `577a40c`: the read-mode rejection happens in the service, before any database I/O, and the two surfaces each render it their own way.

### 1.9 Repository tree

```
cmd/roca/                 entry point, ~50 lines
internal/
  cli/                    cobra commands: parse, call the service, render
  mcpplug/                MCP stdio server: 6 passthrough handlers
  service/                the single object both surfaces call
  store/                  SQLite: opening, schema, adoption, transactions, backup
  ingest/                 scanning, and under parsers/ the pure per-source parsing
  query/                  cascade, and under sqlgate/ the SQL gate
  classify/               classifier: training, prediction, model format
  provider/               model adapters and provider cascade
  config/                 TOML and environment
  doctor/                 diagnosis
data/                     embedded: schema.sql, layers.yaml, semantic-layer.md,
                          queryplan_examples.txt, classifier.roca
docs/
.github/workflows/
install.sh
```

`internal/` in the Go sense: none of this is public API. La Roca is a product, not a library. If one day something needs exposing, a concrete piece is promoted to `pkg/` with its own decision.

---

## 2. Data schema

### 2.1 Laboratory inventory and per-table verdict

The schema lives in `py/roca/schema.sql` (204 lines) and its declared catalog in `py/roca/tables.yaml`. The catalog was crossed with a real count of writers and readers in the production code:

```
$ for t in <each table>; do
    grep -rn "INSERT INTO $t" py/roca --include='*.py' | wc -l
    grep -rn "FROM $t\|JOIN $t" py/roca --include='*.py' | wc -l
  done
```

| Table | State in `tables.yaml` | Writers | Readers | Verdict for La Roca v1 |
|---|---|---:|---:|---|
| `sessions` | live, substrate | 5 | 27 | **Ported as is** |
| `exchanges` | live, substrate | 4 | 23 | **Ported as is** |
| `thinking_blocks` | live, substrate | 6 | 9 | **Ported as is** |
| `tool_uses` | live, substrate | 5 | 7 | **Ported as is** |
| `memories` | live, curated | 5 | 43 | **Ported as is** |
| `layers` | live, registry | 1 | 6 | **Ported as is** |
| `ingest_file_state` | empty, runtime_state | 2 | 2 | **Ported as is** (idempotency, section 5.3) |
| `queryplan_teach_examples` | empty, training | 2 | 4 | **Ported as is** (teach, section 4.6) |
| `messages` | **deprecated, unused** | **0** | 2 | **Not ported.** Zero writers, the catalog already declares it empty legacy |
| `layer_stats` (view) | derived | n/a | 0 in production | **Not ported.** No production consumer; `roca layers` uses `layer_rows` |
| `proposals` | live, governance | 1 | 4 | **v2.** Outside the v1 scope |
| `proposal_annotations` | live, governance | 1 | 4 | **v2** |
| `runs` | **drop_pending**, legacy | 1 | 4 | **v2**, with the catalog already asking for its withdrawal |
| `run_logs` | **drop_pending**, legacy | 1 | 1 | **v2** |
| `flow_patterns` | external_archive | 0 | 0 | **Not ported.** It is not even provisioned |

This does not contradict the pruning law. `messages`, `layer_stats` and `flow_patterns` are not functionality: they are dead objects the laboratory's own catalog already marked as such before this spec. `proposals`, `runs` and their satellites are functionality, and they are not deleted: they are deferred to v2 by the v1 scope decision, which is a different decision from the pruning.

### 2.2 The v1 schema

Eight tables, no views, no new tables. `schema.sql` is copied literally for all eight, including its `CHECK`s, its `DEFAULT (datetime('now'))`s and its indexes. The reason to copy and not rewrite: a laboratory database and a La Roca database have to be **structurally identical** for the adoption in 2.4 to be a no-op, and the structural comparison looks at type affinity, NOT NULL, default expression and primary key position. Any "improvement" to the DDL turns a clean adoption into a migration.

The indexes ported as they are, because each one has its measured reason:

- `idx_memories_layer`, `idx_memories_status`, `idx_memories_project`, `idx_memories_origin`: the four filters the compiler's templates use.
- `idx_exchanges_session`, `idx_tool_uses_session`, `idx_thinking_session`: the join by session, which is the shape of almost every drill-down query.
- `idx_exchanges_session_number` (UNIQUE): ingest hardening. It is what makes re-ingesting not duplicate.
- `idx_ingest_state_project`, `idx_ingest_state_source_agent`.
- `idx_queryplan_teach_examples_template`, plus the `UNIQUE` over `normalized_question`, which is what makes teaching the same phrase twice idempotent.

One real and bounded simplification: **`layers.schema_file` stops making sense**. It is a column pointing at a per-layer schema file, inherited from a design in which each layer had a shape of its own. Today every memory lives in `memories`. In La Roca v1 the column is kept in the DDL for structural identity with the laboratory, and stops being read. If v2 decides to withdraw it, it is withdrawn with its own migration and its own decision, not on the sly.

### 2.3 Layer registry

Twelve layers, declared as data in `layers.yaml` and resynchronized into the `layers` table on every startup with write permission. All twelve are ported:

`user`, `feedback`, `project`, `pattern`, `pill`, `discovery`, `handoff`, `handover`, `question`, `review`, `issue`, `protocol`.

Five of them are marked `is_coordination: true` (`handoff`, `handover`, `question`, `review`, `issue`). That flag is taxonomy only. Term search and FTS exclude the layers marked `search_excluded: true` (`question`, `review`, `issue`) so private messaging does not appear in a memory query; handoff stays searchable because it is session continuity (job J1), not private messaging. Inbox being v2 **does not take them out of v1**: they are memory layers like the rest, they are written with `roca store`, they are read with `roca query`, and a classifier template (`list_pending_questions`) queries them. What is deferred to v2 is the command that manages them as an inbox, not the place where they live.

Eight are marked `is_classifier_label: true` and feed the classifier's labels. That mark is the source `classifier_labels()` reads, and in Go the same embedded YAML is read.

### 2.4 Adoption of laboratory databases

The campaign's defect D-4 was exactly this: a four-month-old production database, identical column by column, rejected by textual comparison. The fix is in `py/roca/kernel/schema_compat.py` and its contract is what La Roca ports, not the implementation.

**The classification is by structure, never by DDL text.** The comparison goes through `PRAGMA table_info` / `index_list` / `index_xinfo`: table and view names, and per column the type affinity, NOT NULL, default expression and primary key position. Four verdicts:

| Verdict | Meaning | What La Roca does |
|---|---|---|
| `current` | structurally identical | Adopts it untouched |
| `migratable` | every difference has a safe in-place repair | Verified backup, repairs, adopts |
| `incompatible` | some difference has no safe repair | A diagnosis naming the difference; touches nothing |
| `foreign` | the identity tables are missing or have another shape | Refuses; it is not a Roca database |

The identity tables are `sessions`, `memories`, `exchanges`.

**The repair boundary is the contract, not a detail.** It may create tables, views, indexes and columns owned by the updater, and it may replace derived views. It may **never** drop a data table nor delete rows to make a constraint fit: duplicate keys under a missing unique index are blocking, never silently deduplicated. Every repair goes behind a verified backup.

**Orphan tables.** A laboratory database will bring `messages`, `proposals`, `runs`, `run_logs`, and possibly leftovers of withdrawn features like `garden_*`. They are **reported and do not block**. Archiving is explicit opt-in and **renames, it does not delete**: `roca schema archive-orphans --yes` moves `garden_notes` to `roca_archived__garden_notes` behind a backup, and the row is still there afterwards. Verified on the Mac mini during the battery.

This has a pleasant consequence for v2: when it arrives, `proposals` and `runs` will be intact in the databases that already had them.

What the structural comparison deliberately does not cover, and it has to be said: `CHECK`s and trigger bodies are invisible to `PRAGMA table_info`. They are neither compared nor repaired. They are imposed on write by the schema the product creates, never by adoption.

**Data path.** La Roca v1's home is always `~/.roca`. Adoption is by copy: `roca init` copies the lab's database into `~/.roca/roca.db` using SQLite online backup and operates on the copy from then on. The live lab database is never opened after that copy. See section 7, item 2 for the decision record.

### 2.5 Audit

The reference writes a 26 MB JSONL audit log on the reference machine. La Roca keeps the capability with two hygiene changes that size justifies: rotation by size with a declared retention, and one line per operation with `source: cli` or `source: mcp`. Without rotation, an audit file is a slow disk leak nobody looks at until it hurts.

---

## 3. Model adapters

### 3.1 The interface

The decision is **frontier with a local floor**: provider adapters by default when there is a credential, an automatic cascade to local Ollama with no network or no credential. The local one is the guaranteed floor, not the product's identity.

The laboratory already has the right shape and it is ported with Go names. In `py/roca/kernel/llm_providers.py` there is a registry contract with three pieces: normalized name, factory, and availability probe (`ProviderRegistration`, `ProviderReadiness`). In Go:

```go
// Provider is everything the cascade needs to know about a provider.
type Provider interface {
    Name() string                                  // normalized: lower case and hyphens
    ModelID() string                               // the concrete model that will answer
    Ready(ctx context.Context) Readiness           // can I use it right now?
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

type Readiness struct {
    Ready   bool
    ModelID string
    Reason  string   // why not, in the operator's language
    Action  string   // what to do to fix it
}
```

`Reason` and `Action` are not decoration: they are what `roca doctor` prints, and the laboratory already learned that a diagnosis that does not name the remedy forces the operator to read code.

Three implementations in v1, none with a provider SDK:

| Adapter | Class | Endpoint | Credential |
|---|---|---|---|
| `frontier` | subscription or API | OpenAI-compatible (`/v1/chat/completions`) | Yes |
| `http` | generic local or remote | OpenAI-compatible | Optional |
| `ollama` | local | `/api/chat`, listing through `/api/tags` | No |

`frontier` and `http` share transport and differ in policy (mandatory credential, headers, timeout). The laboratory already has both clients written and tested against real providers: `OllamaAdapter` in `py/roca/kernel/llm.py:99` and `HttpAdapter` at `:162`. The request shape, the handling of Ollama's `keep_alive` and the unrolling of repeated responses (`_deloop`) are ported from there.

### 3.2 Provider cascade

Default order, resolved at startup and re-evaluable: **`frontier`, `http`, `ollama`**. The laboratory resolves the order in `resolve_provider_order` (`llm_providers.py:84`), with two guarantees that are ported:

- **An unknown name in the order is a named error**, with the list of available ones. It is not silently ignored.
- **A duplicate is an error.** An order with the same provider twice hides a config confusion.

And a third one the laboratory won the hard way: the default order **may not end in a provider that is impossible on that platform**. In the laboratory, ending in MLX on Linux masked real Ollama failures during `init`. La Roca has no MLX, but the rule is kept: the last element of the default order is always a provider that can exist on any supported platform.

**The fall is by availability, not by exception.** Before using a provider it is asked `Ready`. If a provider is available and the request then fails, that is a query failure, it is reported as such and it falls to the keyword rescue of stage 5 of the cascade. It does not silently retry with the next provider: doing so turns "the frontier provider is returning 500" into "the answers are odd today".

`roca doctor` prints the resolved order, which one is available, which one is going to serve and why the previous ones do not.

### 3.3 Configuration and credentials

Config in TOML, at the user's config path, with two rules the laboratory won with defect D-1:

- **A key written at the root of the document resolves the same as one under `[defaults]`**, and `[defaults]` wins on collision. Reading only `[defaults]` made a hand-written `workspace_roots` key invisible and left encoded sessions without project identity.
- **Every message to the operator names the key and the file, never a TOML table.** Both halves have to keep being true.

And the general law the campaign wrote down, which in a product that updates itself is worth gold: **data the operator persisted outlives the release that understood it**. It is treated as data to be survived, not as a contract to be imposed. A provider name this build does not provide is ignored with a warning that names the remedy, and the known ones keep loading. What killed D-5 for hours was the nuance of provenance: if a caller reads the operator's selection and passes it to the resolution function as an explicit argument, the degradation disappears and the exception comes back. In Go the provenance travels in the type, not in an optional parameter:

```go
type Selection struct {
    Names  []string
    Source Source   // SourceConfig, SourceEnv, SourceCode
}
```

A `Selection` with `SourceConfig` degrades. A `SourceCode` is still a contract and fails. The provenance cannot be lost along the way because there is no way without it.

**Credentials.** API ones are a key, and they live in config or in an environment variable, never in the database and never in the log, and `roca doctor` reports presence without printing a value. **Subscription** ones are a different thing: they are not a key of the user's but a session of another product, and reading them means touching a third party's credential store. That is a product decision, not the Tech Lead's: section 7, item 4.

### 3.4 Golden query bench

**It does not exist today.** This was verified by searching the laboratory tree: there is no golden query bench, no evaluation file, no relevance suite. The model's relevance has never been measured; it has been observed in anecdotes, including the best of them all, the one in the Mac mini record.

It is this spec's most important gap and v1 closes it. Without a bench, "the cascade works" is an opinion, and changing provider is a bet.

**What it is.** A versioned data file, `data/golden.yaml`, with cases like these:

```yaml
- id: term-write-worker-port
  question: "what port does the roca write worker use?"
  expect_path: compiler                    # compiler | llm | keyword
  expect_template: search_all_sources_by_term
  expect_rows_contain: ["8765"]            # substring some row must carry
  max_latency_ms: 500
  source: mac-mini-battery-record-20260805
- id: nl-recent-edits
  question: "what files did I edit recently?"
  expect_path: llm
  expect_min_rows: 1
  expect_sql_valid: true                   # the gate accepts the generated SQL
  source: mac-mini-battery-record-20260805
```

**How it is populated, in this order of priority.** First, the real queries with a documented result from the Mac mini record, which are the only ones with field truth. Second, one case per compiler template, taken from the training corpus but **with a phrasing that is not in the corpus**, because a golden case that is literally a training example measures nothing. Third, gate cases: greetings, write orders, ambiguous questions, which have to come out as a refusal with a reason. Fourth, real questions from the reference history, which are the corpus that really matters.

**How it is run.** `roca eval --golden data/golden.yaml --provider <name>` produces, per adapter, a table with: passed and failed cases, distribution by cascade path, p50 and p95 latency per path, and the itemized list of failures. It is the **per-adapter** relevance test the decision asks for.

**What it governs.**

1. It is the acceptance criterion for a new adapter: an adapter that does not pass the bench is not declared supported.
2. It is the arbiter of the SQL repairs in section 1.5: the repair the bench proves necessary against `qwen3.5:4b` is built, and no other.
3. It is a ratchet in CI for the deterministic part. The part that depends on a real LLM cannot run in CI with no network and no credentials: it runs in the local battery on the real machine, which is where real acceptance is already required.

---

## 4. fastText inference in Go

### 4.1 What the laboratory does today

The classifier routes a question to one of 35 labels (33 templates plus `out_of_scope` and `ambiguous`). Its corpus is declared as data, not as code, in `py/roca/queryplan_examples.txt` (406 examples), in the same `__label__` format `teach` writes.

Training hyperparameters, in `train_classifier` (`py/roca/core/queryplan.py:1202-1248`):

```
epoch=60, lr=0.8, wordNgrams=2, dim=32, minn=0, maxn=0,
thread=1, minCount=1, loss="hs"
then quantize(retrain=True, epoch=25, qnorm=True, cutoff=1000, dsub=2)
```

Two facts in that list rule over the whole port's design:

- **`minn=0, maxn=0` means no subwords.** There are no character n-grams. The model is a bag of word unigrams and bigrams. That makes it radically more portable than a generic fastText.
- **The model is trained in place, not distributed.** `_load_queryplan_runtime_model` (`py/roca/core/query.py:831`) looks for `queryplan.ftz` in a per-database cache directory, and when it is not there it **trains on the spot**, and invalidates that cache when `teach` writes. Any Go design that assumes "the model comes pre-trained and that is that" breaks `teach`, which is v1 functionality.

### 4.2 Measurement

The real classifier was trained and evaluated with the repo's venv. Commands and output:

```
$ PYTHONPATH=py uv run python measure_ft.py
Number of words:  261
Number of labels: 34
train_seconds: 0.45
train_path: queryplan_train.txt          26,518 bytes
bin_path:   queryplan.bin           256,043,515 bytes
ftz_path:   queryplan.ftz                66,848 bytes
examples: 406   labels_declared: 35   labels_in_corpus: 34
train_set_accuracy: 406/406 = 1.0000
predict_latency_us_mean: 4.0
confidence mean=0.996 min=0.951 p10=0.989
below_gate_0.85: 0/406
```

And the real feature space, plus a 5-fold cross validation comparing the two loss functions with identical hyperparameters:

```
$ PYTHONPATH=py uv run python measure_ft2.py
distinct_unigrams: 260   distinct_bigrams: 644   total_features: 904
dense_matrix_bytes_dim32_float32: 115,712
labels: 34               output_matrix_bytes: 4,352
loss=hs:      heldout_accuracy = 335/406 = 0.8251
loss=softmax: heldout_accuracy = 357/406 = 0.8793
loss=softmax in_sample: 406/406
loss=softmax confidence min=0.952 mean=0.996
loss=softmax below_gate_0.85: 0/406
```

Five conclusions, all with a number:

1. **The useful model is tiny**: 904 distinct features. At `dim=32` in float32 that is **115,712 bytes** of input matrix and **4,352** of output. About **120 KB in total**, dense and uncompressed.
2. **The 256 MB of `queryplan.bin` are empty allocation.** fastText reserves the whole hashing bucket (2,000,000 rows by 32 dimensions by 4 bytes = 256 MB) even though only 904 rows are used. The 65 KB `.ftz` is the same thing after product quantization, a format whose decoder is not trivial. Neither format deserves to be ported.
3. **The `softmax` loss is better than `hs` at generalizing**: 87.93% against 82.51% in cross validation, with the same hyperparameters. That is 5.4 points, and 22 questions out of 406 that stop being routed badly.
4. **`softmax` is also trivially portable**: the prediction is a dense matrix-vector product and a normalization. `hs` forces walking a Huffman tree. The performance argument in favour of `hs` does not apply with 34 labels.
5. **Training is instantaneous**: 0.45 s in Python. In Go, with 904 features and 34 labels, it will be comparable or lower. Retraining on every `teach` needs no cost justification.

### 4.3 The three options, evaluated

| Option | What it implies | Verdict |
|---|---|---|
| **A. Our own linear inference port** | Reimplement prediction (and training) in pure Go; our own model format | **Recommended** |
| **B. cgo against libfasttext** | Link C++ | **Rejected** |
| **C. Alternative format, training outside** | Train in Python offline, embed the artefact, Go only predicts | **Rejected as a complete solution** |

**Why B is rejected.** cgo destroys the product's whole premise. It forces `CGO_ENABLED=1`, puts `libstdc++` in the binary or as a dynamic dependency, turns cross compilation for three platforms into three toolchains, and transforms a single-runner release matrix into the three-runner matrix the laboratory already suffers today. All that for 120 KB of floating-point arithmetic that fits in a 300-line file. It is the worst possible change in the project's cost-benefit ratio.

**Why C is rejected as a complete solution.** If training stays outside the binary, `teach` can no longer retrain and becomes only an exact-phrase pinning. Generalization, which is half the value of teaching, is lost. `teach` is explicit v1 functionality and the classifier is wanted functionality with a deletion veto. C alone breaks both.

### 4.4 Recommendation

**Option A: implement in pure Go the training and prediction of a linear bag-of-n-grams classifier, equivalent to the supervised fastText the laboratory uses, with `softmax` instead of `hs`, and persist it in a format of La Roca's own.**

The complete algorithm, which is what makes the recommendation defensible:

*Features.* Normalize the text with exactly the same function as the laboratory (`normalize_text`, lower case and folding of diacritics and punctuation). Tokenize on whitespace. The features are the unigrams plus the consecutive bigrams. **No hashing bucket**: with 904 observed features, a `map[string]int32` is simpler, faster and collision-free. fastText hashes because it assumes vocabularies of millions; here it would be importing a compromise that does not apply to us.

*Prediction.* Average the rows of the input matrix for the present features, multiply by the output matrix, apply softmax, take the maximum. With `dim=32` and 34 labels it is negligible arithmetic. The prediction returns a label and a confidence, and the confidence keeps being compared against the same 0.85 threshold.

*Training.* SGD over cross entropy, with the same `epoch=60` and a linearly decreasing `lr=0.8`, and a fixed seed so it is reproducible. A feature that appears in a new example is added to the vocabulary and gets a fresh row.

*Unseen features.* A feature absent from the vocabulary simply does not contribute. With `softmax`, a question made only of unknown features produces a flat distribution and therefore a low confidence, which falls below 0.85 and goes to the term rescue. The correct behaviour comes free from the design.

**Target accuracy, with honesty about what is measured.** The 87.93% is 5-fold cross validation over a corpus of 406 examples with 34 labels and a very skewed distribution (69 examples for `latest_memories_by_layer`, and several labels with fewer than five). It is not a measure of production quality: it is the bar the port has to match or beat with the same protocol. Real quality is measured by the golden bench in section 3.4.

### 4.5 Artefact format

Our own file, `classifier.roca`, binary and versioned:

```
header:  magic "ROCACLF1" | format version uint16 | dim uint16
         | nFeatures uint32 | nLabels uint32 | corpus hash [32]byte
vocab:   nFeatures * (length uvarint | feature bytes)
labels:  nLabels * (length uvarint | label bytes)
input:   nFeatures * dim * float32 (row order)
output:  nLabels * dim * float32
```

About 120 KB for the base model. The corpus hash is what makes the cache safe: if the embedded corpus plus what was taught does not match the hash, the cached artefact is discarded and it retrains. It is the Go equivalent of the invalidation `teach` does today by deleting artefacts (`py/roca/core/teach.py`).

Where it lives: an artefact pre-trained on the base corpus is embedded with `go:embed`, so that **the first query of a freshly made installation pays no training**. As soon as something is taught, the effective model is retrained and cached in Roca's data directory, keyed by database just as it is today.

### 4.6 teach

`roca teach --question "..." --template "..."` does four things, in order:

1. Normalizes the template, accepting the loose shapes the laboratory already accepts (canonical label, `TEMPLATE=` prefix, compact form with `|`, a sentence whose first word is the label, and legacy aliases). It is in `_normalize_template` (`py/roca/core/teach.py`), and it exists because whoever teaches is usually an agent that copied `roca_sql`'s output.
2. Validates against the known labels and, failing that, fails naming the allowed ones.
3. Inserts into `queryplan_teach_examples` with a unique `normalized_question`, so that teaching the same thing twice updates instead of duplicating.
4. Retrains and replaces the cached artefact atomically (write to a temporary file and rename), and invalidates the process's in-memory model.

A taught label this build no longer provides is **skipped** when training, it does not break the training. It is the same "the operator's data outlives the release" law from section 3.3.

And the shortcut from section 1.4 still holds: a taught phrase is recognized by exact match before the gate. Teaching pins that phrase **and** generalizes to similar phrases through the retraining. Both halves have tests.

### 4.7 The port's acceptance criteria

The port is not accepted by code review but by measured parity:

1. **Corpus parity**: 406/406 on the training corpus, the same as today.
2. **Generalization parity**: 5-fold cross validation equal to or above 0.8793 with the same protocol and the same seed.
3. **Routing parity**: for the corpus's 406 questions plus the golden bench's cases, the path chosen by the Go cascade matches the Python cascade's. This is the strong test, because it covers gate, classifier and rescue together, not only the classifier.
4. **teach idempotency**: teaching the same phrase twice leaves one row and one model.
5. **Reproducibility**: two training runs with the same seed and the same corpus produce byte-for-byte identical artefacts.

---

## 5. Ingest

### 5.1 v1 source matrix

The pruning law is explicit: **every current source is kept as functionality**; each adapter may be slimmed down but may not disappear. The matrix comes from `run_all_ingest` (`py/roca/ingest/cron.py:559`) and from the eight phases the CLI counts when reporting (`py/roca/cli.py:1233-1237`).

| Source | What it reads | Default root | Format | Destination |
|---|---|---|---|---|
| Claude Code sessions | Session transcripts | `~/.claude/projects` | JSONL | `sessions`, `exchanges`, `thinking_blocks`, `tool_uses` |
| Claude memories | Per-project memory files | `~/.claude/projects` | Markdown | `memories` (origin `cron`, source `claude-code`) |
| Codex | Sessions, memory/rule/skill files and state metadata | `~/.codex`, sessions under `sessions/` | JSONL, Markdown, SQLite | `sessions`, `exchanges`, `memories` |
| Claude Desktop and Cowork | Sessions | Configurable roots | JSONL | Same as Claude Code |
| Subagents | Subagent transcripts | Under the Claude root | JSONL | `sessions` with their own source agent |
| OpenCode | The agent's database | `~/.local/share/opencode/opencode.db` | SQLite | `sessions`, `exchanges` |
| Pi | Sessions | `~/.pi/agent/sessions` | JSONL | `sessions`, `exchanges` |
| Hermes | State database | `~/.hermes/state.db` | SQLite | `sessions`, `exchanges` |

These eight source families enter v1. The global `~/.claude/CLAUDE.md` and the
repository `AGENTS.md`/`CLAUDE.md` files do not: they are instructions and not
memory, and the 2026-08-05 scope addendum removes them as ingest content.
Configured workspace roots remain solely to resolve session project
identity.

**The roots are configuration, never constants.** This is the laboratory's OSS portability law and in Go it is easier to keep than to break: the roots are derived from `os.UserHomeDir()` and from the configured workspace roots, and an absolute path with a machine, user or personal mount name in production code is a guard failure, not a style decision.

**The ambiguous-project warning is ported as it is.** A session directory with a hard-coded absolute path that no configured prefix explains produces a diagnosis that names the remedy, and does **not** persist a raw absolute path. It was checked on the Mac mini that it is a legitimate diagnosis over genuinely ambiguous data, not a false positive.

### 5.2 Pure parsing

The parsing layer touches neither the database nor the clock nor the disk beyond reading the file it is given. Signature:

```go
// Parse turns an artefact into normalized records. It does not open the
// database, it does not consult the clock, and it is deterministic: same
// content, same result.
func Parse(source SourceKind, content []byte, meta FileMeta) (Records, error)
```

This is not purism. It is what makes the ingest suite a table of cases with example files and expected output, with no database and no integration marker, and that is the difference between a 40-second suite and a 40-minute one. The laboratory already has its parsers separated (`py/roca/ingest/parsers/`) and its own audit pointed at them as the repo's cleanest area.

Writing lives apart, in a function that takes `Records` and a transaction. It is the only one that knows SQL.

### 5.3 Idempotency by fingerprint

Two levels, both already proven in the laboratory:

**File level.** `ingest_file_state` stores per path the `source_kind`, the agent, the project, a `fingerprint`, the last sync and the last error. Before parsing, the current fingerprint is compared with the stored one; when they match, the whole file is skipped. It is `_fingerprint` and `_is_changed` (`py/roca/ingest/ingest_live.py:59` and `:100`). The writer uses `INSERT ... ON CONFLICT(path) DO UPDATE`, so re-ingesting never duplicates state.

**Record level.** The file fingerprint is not enough for a log that grows: a session file changes on every turn and its fingerprint changes whole. That is why there is a second defence, and it is structural: `idx_exchanges_session_number`, a **unique** index over `(session_id, exchange_number)`. Re-ingesting a grown file inserts only the new exchanges. The OpenCode and Pi adapters also carry a per-exchange fingerprint in their session metadata, so as not to rewrite what did not change.

**La Roca's contract:** ingesting the same input twice produces exactly the same state. It is a test, not an aspiration, and it is the test that protects the reference flow, which runs `roca ingest` repeatedly.

Real reference figures, from the battery's second cycle on the Mac mini, so that a regression is visible:

```
scanned: codex_files=4 session_files=7 opencode_databases=1
delta:   memories=0 sessions=4 exchanges=104 thinking_blocks=285 tool_uses=205
```

### 5.4 Live ingest

The laboratory ingests incrementally during the session, triggered by hooks. Whether the hooks enter v1 is open decision 3 in section 7. Whatever the answer, the incremental engine (`ingest_live`) is ported in v1 because it is what makes a repeated `roca ingest` cheap: without it, every run reprocesses everything. What the decision governs is the trigger, not the engine.

---

## 6. Distribution

### 6.1 Build matrix

```
GOOS=darwin  GOARCH=arm64   roca-<version>-darwin-arm64
GOOS=linux   GOARCH=arm64   roca-<version>-linux-arm64
GOOS=linux   GOARCH=amd64   roca-<version>-linux-x64
GOOS=windows GOARCH=amd64   roca-<version>-windows-x64.exe
```

It is the matrix the laboratory already declares and defends, with the same deliberate absence of darwin x64 (the macOS Intel runners were retired, and reopening that route needs new runner evidence, not an opinion), plus windows-x64, which the distribution decision added to the wave that built the channel. Windows is the one artefact whose name has a different shape, because without the `.exe` the operating system will not run the file at all; `install.sh` already promised that exact name to the Windows operator it turns away, and `release.ArtefactName` is where the rule lives for everybody else.

**Build priority: darwin-arm64 first**, which is the reference machine and the Mac mini's, where it is really tested. Linux after.

With `CGO_ENABLED=0` all four come out **of a single runner**. That is the biggest operational change in the whole port: today's `release.yml` needs three jobs on three different runners (macOS, `ubuntu-24.04-arm`, ubuntu x64) because PyApp compiles per platform. Pure Go cross compiles with no toolchain. It goes from three lanes that break separately to one that either breaks whole or does not break.

Build flags:

```
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION -X main.commit=$SHA -X main.date=$BUILD_DATE" \
  -o "$OUT" ./cmd/roca
```

`-trimpath` strips build-machine paths from the binary, which is also exactly what the OSS portability guard requires.

### 6.2 The channel produces the artefacts

The rule, literally: the release artefacts are produced by GitHub Actions; the channel is not replaced with manual builds uploaded by hand as "the" release. The flow:

1. A `vX.Y.Z` tag triggers `release.yml`.
2. One job runs `make check` and `make accept` over the tag, then `make dist` with the tag as `VERSION`, which is how the version the binary reports IS the tag. The names come from the Makefile and from nowhere else: a `go build` written in the workflow would be a second spelling of the artefact names in the one place where nobody runs the tests that catch it, and a test reads both files to hold them together (`internal/release/contract_test.go`).
3. The artefacts go up **bare**, not compressed. What the brief asked to publish is the binaries, the licence travels in the repository and on the release page, and the Windows operator was already promised a bare `.exe` by `install.sh`. Compressing would buy some bandwidth at the price of a second artefact shape in four places, and both clients already try the bare name first.
4. The same job computes `checksums.txt` with one `<sha256>  <name>` line per artefact and uploads it **last**, after all the binaries, in a command of its own so the order is guaranteed and not hoped for. That order matters: a `checksums.txt` published before the binaries is a window in which the installer can verify against a file describing artefacts that are not there yet.
5. The workflow accepts `workflow_dispatch` by tag, to rebuild without burning a new version. The laboratory already learned it is needed, and the rerun converges on the release that is there instead of creating a second one.

### 6.3 Installation

`install.sh` published in the repo. It detects the platform, downloads the artefact of the requested release, **verifies the sha256 against `checksums.txt` and aborts when it does not match**, and copies the binary to `~/.local/bin/roca` (or the prefix it is told). There is no release tree, no `current` symlink, no swap by rename: there is nothing to make atomic, because installing is writing a file.

Two things the reference flow forces us to support:

- **Private repo.** The anonymous one-liner to `raw.githubusercontent.com` gives 404 in a private repo. While the repo is private, the real path is an authenticated `curl` against the contents API with `Accept: application/vnd.github.raw`, and `GITHUB_TOKEN` for release downloads. The installer has to work that way and the README cannot claim the anonymous curl works.
- **`roca update`.** A command of its own: it queries the latest release, compares it with the current version, downloads, verifies the checksum and replaces the binary in place. A single file makes this a trivial operation.

On signing on macOS: a binary downloaded with `curl` and run from the terminal does not carry the quarantine attribute, so Gatekeeper does not block it. Signing and notarization are only needed if one day it is distributed through a browser or a `.pkg` installer. It is not done in v1 and the reason is stated.

### 6.4 Uninstallation

`roca uninstall`, with the same contract the laboratory won with D-7:

- Interactive by default, asking whether to keep the data; scripts pass `--keep-data` or `--purge`.
- The purge **converges over the state it finds**: it runs on machines a previous attempt left halfway, and it cannot treat its own artefacts as foreign. That was exactly the failure in #451: capturing the inventory before creating its own lock directory made the purge refuse to delete its own directory as one that "appeared after the inventory", report `purged: no`, and leave residue with the CLI already deleted.
- The refusal to delete a path Roca did not create **is kept**. What is removed is the race, not the protection.
- It reverts the MCP entries it installed in the agents' configs, leaving them as they were.
- It is re-runnable: running purge twice ends in `ok` both times.

In Go the residue surface is much smaller than in the laboratory: binary, config, database, and the MCP entries. No venv, no PyApp cache, no release tree, no launchd cache.

A hygiene observation from the battery record that disappears here by construction: old and aborted releases piled up under `production/releases/` with no pruning, growing without a ceiling on a machine that reinstalls a lot. With no release tree there is nothing to pile up.

### 6.5 The media companion

Outside the core binary, by decision. It is distributed as a binary of its own with its own release, and it plugs in as one more MCP server in the agent's config. The core does not know it, does not discover it and does not declare it. It is the literal reading of "the core binary stays pure".

---

## 7. Open decisions

Four. None of them is technical: the technical ones are decided above. These four change the product or touch its live data, and that is why they stay open. Each one is recorded as a durable decision.

**1. The identity of La Roca's public repo.** Name, owner and visibility. It was already left pending explicit consent in the decision of 5 August; it is repeated here because it blocks the start of wave 0 (creating the repo). The local spike with no remote can start without this answer; the first publication cannot.

**2. Data path and explicit adoption.** Resolved: La Roca's home is always `~/.roca`. On a fresh interactive init, the operator chooses new or adopt; adoption asks the operator to type a source path, copies that database into `~/.roca/roca.db` using SQLite online backup, and leaves the source untouched. No source path is detected or suggested. An existing home database is kept or destructively reinitialized only by explicit answer. Non-interactive initialization selects only a location with `--db-path`.

**3. Session hooks in v1.** The pruning law keeps the hooks as functionality **in the laboratory**. The v1 scope decision fixes "memory + query + teach" and does not mention them. The gap is real: the hooks are what feeds the corpus live when a session ends. Without them, ingest is only `roca ingest` on demand, which works but leaves the corpus colder. Options: (a) hooks in v1, with their install and uninstall command; (b) hooks to v2, and in v1 only `roca ingest`, which the user can schedule themselves. The incremental engine is ported in both cases (section 5.4); what changes is whether the v1 binary ships the hook commands and their integration with Claude Code.

**4. Subscription credentials.** The decision says "provider adapters (subscription/API)". An API credential is a key of the user's and handling it is routine. A **subscription** credential is not: it is another product's session, stored by that product, and using it means reading its credential store and depending on a format that product can change without warning. Options: (a) v1 only supports explicit API credentials, and subscription plans are used through whatever each one exposes as a compatible endpoint; (b) v1 reads subscription credentials from the named products, accepting the fragility and the security surface. The Tech Lead's recommendation: **(a)** for v1, and reopen it with a concrete provider on the table.

**Durable record.** All four are recorded as decisions in the backlog, with stable identities, so they do not depend on somebody reading this document again:

```
laroca-spec-decision-repo-identity
laroca-spec-decision-db-path-adoption
laroca-spec-decision-hooks-in-v1
laroca-spec-decision-subscription-credentials
```

None of them blocks wave 0 of the plan in section 8: the local repo with no remote, the simulator and the translation into stories can start today. Number 1 blocks the first publication. Numbers 2 and 3 block the closing of waves 1 and 5 respectively. Number 4 blocks the providers lane of wave 4, only in its frontier half.

---

## 8. Construction plan

### 8.1 The framework

The construction framework is a fixed set of roles, a fixed flow, the flow modelled as a static state machine, a simulator driven by that machine with jitter and monte carlo, and the human apex that escalates what makes no sense.

**Roles.** Analyst, gherkin author, QA author, implementer, cleaner, hardener, QA tester, architect, reviewer.

**Flow, fixed and with no shortcuts:**

```
theme -> stories -> gherkin + QA plan (BEFORE the code)
      -> code with unit and acceptance tests
      -> cleaned -> hardened -> QA -> architecture
```

The rule that is not negotiated: **gherkin and QA are written before a single line of production code**. It is the form of the TDD law already in force, and it fits the campaign law ("every defect dies with its regression test in front"). In a port it also has a specific advantage: the gherkin of a port story **is written by reading the equivalent Python test**, so the acceptance corpus exists before starting and does not have to be invented.

**State machine.** Each story is a token that walks these states, with one and only one exit transition per state except the rejections, which return it to the previous state:

```
BACKLOG -> ANALYZED -> SPECIFIED(gherkin+QA) -> IMPLEMENTED
        -> CLEANED -> HARDENED -> QA_PASSED -> ARCHITECTURE_OK -> DONE
                                     |             |
                                  rejection     rejection
                                     v             v
                                IMPLEMENTED    SPECIFIED
```

**Simulator.** Before the first real story enters the machine, the simulator is built: the state machine, per-state times extracted from the campaign waves already executed, jitter (delays and failures with a probability per state) and monte carlo over the set of stories from waves 1 to 5. What the simulator has to answer before spending a single real worker: where the bottleneck is, how many implementers saturate one QA tester, and what rejection rate makes the flow stop converging. It is wave 0 and it is not optional.

**Human apex.** A token that bounces twice off the same state, or any finding that contradicts this spec, escalates to the lead. The lead does not fix the token: they decide whether the problem is with the story or with the spec.

### 8.2 The waves

Each wave declares which worker builds what, in what order, and with what gate. The dependency rule between waves is hard: **a wave does not start until the previous one's gate is green**. Within a wave, the stories marked parallel do not touch each other.

---

**Wave 0: the foundation.** Sequential. It blocks everything.

| Worker | Builds | Gate |
|---|---|---|
| Architect | Go repo, module, `internal/` layout, PR CI (build, vet, test, lint), `.gitignore` | `go build ./...` and green CI on an empty PR |
| Architect | The state machine as code, plus the simulator with jitter and monte carlo | The simulator runs 1,000 iterations over the set of waves 1 to 5 and produces a bottleneck and a critical rejection rate |
| Analyst | Translation of this spec into themes and stories, with the reference Python test cited in each one | Every story in waves 1 to 5 has its gherkin file and its QA plan before wave 1 opens |

---

**Wave 1: store and schema.** Parallel in two lanes.

| Worker | Builds | Gate |
|---|---|---|
| Implementer A | `internal/store`: opening with WAL, busy_timeout, `BEGIN IMMEDIATE` with retry and jitter; embedded schema; transactions | Concurrency test with **real processes over a barrier**: 8 writers, zero lost transactions |
| Implementer B | `internal/store/adopt`: structural classification into four verdicts, in-place repair inside its boundary, orphan archiving by rename | Aged database fixture (historical ALTERs and `garden_*` tables): verdict `migratable`, adoption with no blocking, orphans reported and not deleted |
| Implementer B | `internal/store/backup`: dated and verified snapshot | Every repair has its verified backup in front of it, checked by test |
| Hardener | Guard: zero absolute machine paths in production | Portability guard in CI, failing the build when one shows up |

---

**Wave 2: ingest.** Parallel per source, one worker per adapter. The most parallelizable wave of all.

| Worker | Builds | Gate |
|---|---|---|
| Implementer x5 | Pure parsers: Claude JSONL, Codex, OpenCode SQLite, Pi, Hermes | A table of cases with example files and expected output, **with no database**; no test marked as integration |
| Implementer | Scanning sessions, Claude global/per-project memories, and Codex memory/rule/skill files; workspace roots resolve identity only | The ambiguous-project warning emitted as a diagnosis, with no absolute path persisted |
| Implementer | Writer and incremental state: `ingest_file_state`, fingerprint, upsert by path | **Ingesting twice produces the same state**, byte for byte, over every source |
| QA tester | Acceptance against a copy of the real database | The counts reproduce the record's reference: `exchanges=104`, `thinking_blocks=285`, `tool_uses=205` |

---

**Wave 3: classifier and cascade.** The heart. Two lanes with a synchronization in the middle.

| Worker | Builds | Gate |
|---|---|---|
| Implementer A | `internal/classify`: features, softmax training, prediction, `classifier.roca` format | The five criteria in 4.7, with the numbers from 4.2 as the bar |
| Implementer A | `teach`: persistence, template normalization, atomic retraining, invalidation | Teaching twice leaves one row and one model; an unknown label is skipped without breaking |
| Implementer B | `internal/query/sqlgate`: preparation against a restricted schema, `_query_only`, `stmt_readonly`, function allowlist and `LIMIT` requirement through the AST | Every negative case in the Python gate suite is rejected in Go too |
| Implementer B | The compiler's 33 deterministic templates | Each template produces the same SQL as the Python one for the same inputs |
| *synchronization* | | |
| Implementer C | The complete cascade: gate, declared search, classifier, term rescue, LLM, keyword rescue | **Routing parity** over the corpus's 406 questions and the golden bench |
| QA author | The golden bench `data/golden.yaml` populated per 3.4 | Runs against real `qwen3.5:4b` and produces its per-path table |

---

**Wave 4: providers and surfaces.** Parallel in three lanes.

| Worker | Builds | Gate |
|---|---|---|
| Implementer A | `internal/provider`: interface, `ollama`, `http`, `frontier`, cascade with `Ready`, order validated with typed provenance | An unknown name and a duplicate fail by naming; a config selection degrades and a code one fails |
| Implementer B | `internal/service` and `internal/cli`: the v1 commands from the table in 1.7, with `--json` and `source: cli` audit | A complete `roca --help`; every command with its output contract test |
| Implementer C | `internal/mcpplug`: stdio with the official SDK, 6 tools, one-line passthrough handlers | A test that **fails when a handler has logic of its own**, equivalent to `TestServiceIsTheOneSurface` |
| Implementer C | `roca mcp install/status` for the agents in the reference flow | Installs and uninstalls leaving the agent's config as it was, verified byte for byte |
| Hardener | Read-only mode refusing in the service, before any I/O, with `query_only` underneath | A single message for both surfaces |

---

**Wave 5: distribution and lifecycle.** Sequential.

| Worker | Builds | Gate |
|---|---|---|
| Implementer | `init`, `doctor`, `version`, `update` | `doctor` names the remedy for each failure, not only the failure |
| Implementer | `uninstall` with convergent and re-runnable keep-data and purge | Purge twice ends `ok` both times; it does not delete what it did not create |
| Architect | `release.yml` with the build matrix, checksums last, `workflow_dispatch` by tag | A test release with the four artefacts and a verifiable `checksums.txt` |
| Implementer | `install.sh` with checksum verification and the private-repo path | Installation on a clean machine through the authenticated route |

---

**Wave 6: the real battery.** Sequential, on the Mac mini. It is the only gate that counts.

An exact replica of the reference flow, plus the protocol of the campaign's wave 2 battery:

1. Sweep away previous residue and note what was there.
2. Install through the authenticated route; `roca --version`; bare `roca`; `roca doctor`.
3. `roca init`; `roca doctor` second pass.
4. `roca ingest` against real artefacts; counts compared with the reference.
5. A real NL query over both surfaces with local Ollama `qwen3.5:4b`: compiler path and LLM path, with their latencies.
6. `roca mcp install codex`, `claude`, `opencode`; verify in the configs; verify the rollback.
7. `roca mcp serve` directly, and a real MCP call over stdio.
8. `roca teach` and a check that the taught question changes path.
9. `roca update` from an earlier version.
10. `roca uninstall` with purge, answering `n`; verified zero residue.
11. Installation interrupted halfway and reinstalled on top: it converges.
12. The whole cycle a second time.

An honest verdict per step. **Entirely green is the only approval.** Any red is a report and not approved, with the same rule the campaign already applied and that produced the honest red of D-5.

### 8.3 Stories that cross waves

Three pieces belong to no wave because they cross all of them, and they are built in wave 0 with their own gate:

- **The embedded files** (`schema.sql`, `layers.yaml`, `semantic-layer.md`, `queryplan_examples.txt`, `classifier.roca`) and a test that verifies the embedded content matches the laboratory's where it must.
- **Failure rendering.** The D-3 lesson as a Go type: an error that is only summarized has lost its trace forever. One function for the line the operator sees and another for the full report that goes to the log. Rendering a failure with the error's default interpolation is forbidden.
- **The provenance law** from section 3.3, as a shared type, because config, plugins and providers all use it.

### 8.4 Foreseeable bottlenecks

What to watch, and what wave 0's simulator has to quantify before it happens:

- **Wave 3 is the bottleneck.** Classifier, SQL gate and cascade are the part with the least parallelism and the most risk. The synchronization in the middle of the wave is a real blocking point: if the gate lane falls behind, the cascade lane cannot close.
- **Wave 2 is the counterweight.** Nine almost independent sources: it is where the plan can absorb any slack.
- **Wave 6 does not parallelize.** One machine, one cycle, one verdict. Putting workers there speeds nothing up and dirties the result.

### 8.5 The campaign's defects as the birth suite

D-1 to D-8 are not history: they are the port's birth suite, by explicit order. Each one is translated into a test that exists **before** its wave's code:

| Defect | Birth test | Wave |
|---|---|---|
| D-1 config only read `[defaults]` | A key at the root of the TOML resolves and changes measurable behaviour | 0 |
| D-2 startup on a virgin machine | A clean installation answers the first query with no manual step | 6 |
| D-3 swallowed exception | A composite failure delivers the real cause, not a summary | 0 |
| D-4 schema compared by text | An aged database is adopted, orphans do not block | 1 |
| D-5 the operator's data kills the runtime | A persisted unknown name degrades with a warning, the rest carries on | 4 |
| D-6 stop leaves an orphan | With no daemon, it does not apply. It is replaced by: no process is left after any command | 6 |
| D-7 purge fails on its own race | A re-runnable and convergent purge | 5 |
| D-8 full cycle on a fresh machine | The twelve steps of wave 6 green, twice | 6 |

D-6 deserves an explicit note: **it disappears by construction, not by fixing**. With no resident process there is no possible orphan. It is the best argument in favour of the "no daemon" decision this spec obeys, and that is why it is worth writing down.

---

## 9. Annexes

### 9.1 Summary of the Tech Lead's recommendations

1. **fastText: our own Go port with `softmax`, not cgo, not the fastText format.** Backed by measurement: 904 features, 120 KB dense, `softmax` 5.4 points better than `hs` in cross validation, training in under half a second.
2. **The SQL gate is rebuilt, not translated.** `modernc.org/sqlite` has no authorizer; preparing against a restricted schema is stronger than an allowlist in the AST and preserves the "the engine says so" property.
3. **The SQL repairs are built against the golden bench, not by copying.** Four scars with no data to justify them are four scars.
4. **The golden query bench is v1, not v2.** It is the laboratory's biggest gap and without it no adapter can be accepted and no relevance regression can be measured.
5. **`messages`, `layer_stats` and `flow_patterns` are not ported.** The laboratory's own catalog already declares them dead, with zero measured writers.
6. **The data path should be its own with an explicit import** while the laboratory is still alive, so Python and Go can be compared over the same data. It is a decision (section 7, item 2).
7. **Audit log rotation enters v1.** 26 MB on a single machine, and growing, is a slow leak.

### 9.2 Traceability: decision to section

| Decision | Sections that implement it |
|---|---|
| Go, one static binary | 1.1, 1.6, 6.1 |
| The CLI is the product, the MCP is the plug | 1.2, 1.7, 1.8 |
| v1 = memory, query, teach | 1.2, 1.7, 2.1, 2.2 |
| Media outside the core | 1.1, 1.7, 6.5 |
| Frontier with a local floor | 3.1, 3.2 |
| fastText is wanted functionality | All of 4 |
| Code gets pruned, functionality does not | 1.7 (justification of absences), 2.1, 5.1 |
| No daemon, process on demand | 1.3, 1.8, 8.5 (D-6) |
| Installing is copying, uninstalling is deleting | 6.3, 6.4 |
| The channel produces the artefacts | 6.2 |
| D-1..D-8 as the minimum bar | 8.5 |
| The construction framework | All of 8 |

### 9.3 Laboratory references cited

All over `release/saneamiento-20260805`, HEAD `577a40c`.

| Topic | File and line |
|---|---|
| Query cascade | `py/roca/core/query.py:1091-1154` |
| Confidence threshold 0.85 | `py/roca/core/query.py:60` |
| Domain gate | `py/roca/core/queryplan.py:474` |
| Declared search and term rescue | `py/roca/core/queryplan.py:548`, `:561` |
| Classifier training and prediction | `py/roca/core/queryplan.py:1202`, `:1257` |
| In-place training and cache | `py/roca/core/query.py:831` |
| SQL validation | `py/roca/core/query.py:749-798` |
| Preparation under the authorizer | `py/roca/core/query.py:712` |
| Keyword rescue | `py/roca/plugins/core/query_handlers.py:111` |
| Query and SQL handlers | `py/roca/plugins/core/query_handlers.py:391`, `:309` |
| SQLite concurrency | `py/roca/kernel/db.py:21`, `:365`, `:378-379`, `:390`, `:558-559` |
| Model adapters | `py/roca/kernel/llm.py:99`, `:162` |
| Provider registry and order | `py/roca/kernel/llm_providers.py:84` |
| Structural schema adoption | `py/roca/kernel/schema_compat.py` |
| Ingest orchestration | `py/roca/ingest/cron.py:559` |
| Fingerprint and incremental | `py/roca/ingest/ingest_live.py:59`, `:100` |
| The 6-tool MCP surface | `py/roca/plugins/core/tools.py` |
| Passthrough handlers | `py/roca/plugins/core/plugin.py` |
| The single service | `py/roca/core/service.py` |
| The optional process's lifecycle | `py/roca/service.py` |
| stdio transport by default | `py/roca/mcp/main.py:490` |
| The resident server's port and transport | `py/roca/mcp/endpoint.py:15`, `:18` |
| Schema | `py/roca/schema.sql` |
| Table catalog and layer registry | `py/roca/tables.yaml`, `py/roca/layers.yaml` |
| Classifier corpus | `py/roca/queryplan_examples.txt` |
| Current release matrix | `.github/workflows/release.yml` |

### 9.4 Verified external sources

- The official Go SDK for MCP: [github.com/modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk), with `StdioTransport` and schema generation from structs.
- The protocol's stateless 2026-07-28 revision: [modelcontextprotocol.io/specification/2026-07-28/changelog](https://modelcontextprotocol.io/specification/2026-07-28/changelog) and [blog.modelcontextprotocol.io/posts/2026-07-28](https://blog.modelcontextprotocol.io/posts/2026-07-28/).
- Pure-Go SQLite driver: [pkg.go.dev/modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite). It was verified in its documentation that it exposes `Limit()` and `_query_only`, and that it does **not** expose an authorizer; hence the redesign in section 1.5.
- SQLite parser in Go: [github.com/rqlite/sql](https://github.com/rqlite/sql), a pure-Go parser of the SQLite grammar with an AST.

### 9.5 Method of the classifier measurements

The two measurement scripts were run with the repo's venv from `py/`:

```
$ PYTHONPATH=/…/reference/py uv run python measure_ft.py
$ PYTHONPATH=/…/reference/py uv run python measure_ft2.py
```

The first trains with the laboratory's real function (`train_classifier`) and measures artefact sizes, corpus accuracy, prediction latency and confidence distribution. The second counts distinct unigrams and bigrams with the same production normalization, and runs 5-fold cross validation (seed 7) comparing `hs` and `softmax` with identical hyperparameters. Both scripts live in this session's scratchpad and are disposable: what has to be kept is the numbers, which are complete in section 4.2, and the fact that they are obtained with two calls to the laboratory's own public API.

A note on the environment: this worktree's `py/.venv` runs Python 3.14 and `fasttext-community`, which is the wheels-for-ARM replacement the laboratory's own build already uses (`scripts/build-p2a.sh:314-318`). The numbers come from that implementation, which is the same one running on the reference machines.
