# The MCP plug

First-time path: [install and initialize search](lifecycle.md#install).

The way La Roca reaches an agent that is not typing commands: the MCP server it
can call.

La Roca is built agent-first, following the AXI convention (agent ergonomic
interface) shared by a family of agent-facing tools: route narration above
the data, compact TOON rows, bounded text previews, and deterministic next
commands in every answer. An agent never has to guess what it just got or
what to run next.

`roca_vector_query` is the fast semantic search. `roca_query` is the hybrid
path: full-text plus an optional vector leg, used when exact terms matter.
Neither calls an answering model. The model-backed `roca_sql` and
`roca_explore` tools require the [optional playground plugin](plugins.md#optional-human-answering).
`roca_handoff_latest` and `roca_pill_show` load session-continuity records
without hybrid retrieval.

---

## 1. `roca mcp serve`: the MCP over stdio

The command is a foreground stdio shim. It forwards each MCP session to the
one La Roca resident for that data directory; the shim owns no database handle.
The resident shares one service and its database pools across sessions, serves
MCP and CLI calls over a private Unix socket, and stays alive when a client pipe
closes. Database selection follows the [serving layout](architecture.md).
Its default socket is `<data-directory>/resident/resident.sock`, normally under
`~/.roca`.
If none is listening, `mcp serve` starts it and waits for readiness.

`roca exec`, `roca query`, `roca store`, `roca handoff latest`, `roca health`,
`roca doctor`, and `roca vector query` use the resident when it is running.
These CLI calls do not start the database resident: they use their existing
local path if it is unavailable before dispatch. Errors after dispatch are
returned without replaying the call locally. Read-only CLI invocations and
commands outside this set use their local paths.

An attached MCP shim reconnects on the next request after a disconnect and
replays its MCP initialize handshake. Pending requests receive an error saying
their outcome is unknown; they are not replayed, including writes. If the
replacement cannot be started or connected, the shim exits with an error.
Writes attempt a WAL checkpoint after committing; contention can leave WAL
frames until another write attempts checkpointing. The maintenance semantics
are owned by [`store.DB.Checkpoint`](../internal/store/store.go).

The resident is separate from the embedding resident. When semantic search is
enabled, `roca-vector` is the single model holder at
`<data-directory>/vector-resident/resident.sock`; the database resident asks it
for vector results. Different data directories have separate pairs of
residents. When it connects to the database resident, `roca doctor` reports its
PID, uptime, attached clients, connections in its service pools, and discovered
`*.db-wal` sizes beneath its data directory. These are not machine-wide counts.

Closing an MCP session disconnects only that client. Both resident sockets are
private and stale sockets from a killed process are replaced on the next start.

`ROCA_RESIDENT_SOCKET` overrides the database resident socket. For the model
resident, `ROCA_VECTOR_RESIDENT_SOCKET` overrides its socket. Both place their
startup lock alongside the socket. Use a separate pair for each data context:
the resident retains the database and plugin configuration of the session that
started it.
On Unix, the socket directory must be owned by the current user and private,
and the socket path must be shorter than 100 bytes. The model resident exits
after five minutes with no clients attached by default. A positive Go duration
in `ROCA_VECTOR_RESIDENT_IDLE` overrides that period when the model resident
starts; its internal `_resident --idle` flag takes precedence. The database
resident has no idle exit and can retain a model client between CLI queries.

The detached database resident appends stdout and stderr, including model
preparation progress and MCP session diagnostics, to
`<data-directory>/logs/resident.log` by default. A socket override places this
log in `../logs/resident.log` relative to the socket directory. The model
resident uses `<data-directory>/logs/vector-resident.log`. CLI calls that
connect directly to the model resident print preparation progress on stderr.
If the database resident cannot connect to or start the model resident for an
MCP session, it logs a notice and keeps core tools available without
`roca_vector_query`. If a model client receives a
query's result or query error immediately before a disconnect, it preserves that
reply. A disconnect without a reply for that query remains an error. Subsequent
vector calls on the disconnected MCP session fail; a new MCP session can start
or connect to a model resident. The database resident drops a failed CLI vector
connection so a later vector query can reconnect. If one model resident reply
exceeds the client read limit, the query succeeds with a notice and no vector
results instead of failing with a scanner error; hybrid search keeps any
full-text results.

On the local CLI query path, failure to establish a ready model resident
connection falls back to the standalone query path, which may load the model
for that invocation. After establishing a model connection, query errors are
returned without retrying locally. A vector query dispatched to the database
resident returns connection errors without this standalone fallback.

After a companion update, if the primary model resident does not advertise both
`expand_templates` and `min_score`, the CLI leaves it running and connects to
or starts a separate resident using the updated companion. Its socket and
startup lock append `.current` to the primary paths, including an overridden
socket path; the Unix path-length limit also applies to this socket. This
applies even to plain CLI queries. Existing MCP vector clients stay on their
original resident until disconnected. Hybrid `roca query` uses this CLI path,
with template expansion only when enabled in query settings.

If the selected resident still lacks a requested query option, the query fails
with a restart instruction. Disconnect its clients and let the idle period
expire before retrying with the updated companion.

Plugin child processes follow the declaration, ownership, and degradation
contract in [session companions](plugins.md#session-companions).

```
roca mcp serve
```

Nothing but the protocol goes to standard output. A print there corrupts the
session, which is why every diagnostic in this path writes to standard error.

### The tools and optional plugins

| Tool | What it does | The caller that defends it |
|---|---|---|
| `roca_vector_query` (vector companion) | Returns fast semantic evidence from selected local indexes; `k`, `limit`, or `top` sets the hit count | An agent's first memory search when meaning is enough |
| `roca_exec` | Runs a SELECT under the same gate and [table-name contract](queries.md#table-names-in-authored-sql) as `roca exec` | Agents that received SQL from `roca_sql` and have no shell |
| `roca_handoff_latest` | Loads the current unsuperseded handoff for one project without hybrid retrieval | A session resuming project work |
| `roca_pill_show` | Loads one complete project pill by slug without hybrid retrieval | A session following a known operating instruction |
| `roca_query` | Returns labeled hybrid FTS/vector evidence; `top`, `require_both`, and `databases` match the CLI | An agent searching memory without a shell |
| `roca_store` | Writes one memory back | The other half of the same job |
| `roca_health` | The non-destructive checks over live data | An agent that cannot run `roca doctor` |
| `roca_explore` (playground plugin) | Runs plain or deep investigation with prose, terrain, next probes, and generated SQL | Agents following evidence without a shell |
| `roca_sql` (playground plugin) | Compiles a question into SQL without running it | Agents that need to inspect the SQL before `roca_exec` runs it |

`roca_store` follows [memory authorship](operations.md#memory-authorship), the
same [handoff write policy](operations.md#handoff-writes) as the CLI, and the
[audit record contract](operations.md#streams-and-contents).
`roca_handoff_latest` and `roca_pill_show` follow the
[session-context contract](queries.md#session-context): omitting `project` uses
the working-directory basename, and a miss names the available projects or pill
slugs. When a `roca_query` question mentions a handoff, handover, or inbox, its
help points to these direct session-context tools before the generic search
follow-ups.

With semantic search enabled and its companion available, the same server also
exposes `roca_vector_query`. It searches selected local indexes by meaning and
uses the shared resident, pre-prepared model described above. Core tools remain
available whether or not semantic search is enabled. `roca_sql` and
`roca_explore` are registered only when the playground executable is installed
at server startup; restart the MCP server after installing it.

The two search tools accept the same input aliases: `query`, `question`, or
`text` for the search text, and `k`, `limit`, or `top` for the result count.
`roca_query`, `roca_explore`, and `roca_sql` reject empty questions and share
the CLI's generous 1000-character cap before work begins. The model-backed
tools also follow the playground input and SQL gate:
[what happens in the playground](models.md#what-happens-in-the-playground).

`roca_explore` is a separate tool rather than a mode on `roca_query`. That keeps
the established query schema and rows-first answer untouched while making the
investigation mode explicit at dispatch. Omitted or false `deep` is the plain
radius mission; `deep: true` is the full terrain mission. Both dispatch to the
plugin's `explore` verb and render its JSON result as
TOON: prose and generated SQL, with terrain and next probes for the selected
mission.

`roca_list_runs` is **not** in v1: `runs` is v2 scope and this binary creates no
such table. A tool with nothing behind it is a tool that lies.

### Answers are TOON text

Every tool answers as compact TOON rows in plain text, the same shape the CLI
prints: cheap for a model to read, with the route narration above the data.
The server never returns row envelopes in `StructuredContent`; that contract
is pinned by `internal/distribution/mcpplug/toon_contract_test.go`.
The `max_chars` argument follows the shared [text-budget contract](queries.md#text-budgets).
See [Memory identifiers for clients](queries.md#memory-identifiers-for-clients)
for identifier encoding and `roca_store` input compatibility.

### The law of this surface

Core handlers call the shared service. The optional answering handlers dispatch
to the playground executable through `internal/distribution/mcpplug/playground.go`
and pass the selected database and read-only policy. TOON rendering stays in the
MCP wrappers; the inference implementation and its acceptance scenarios belong
to the plugin. `internal/distribution/mcpplug/plug_test.go` pins the core tool
list; `playground_test.go` checks text budgets through the plugin boundary.

### On the protocol version

The SDK's latest revision is 2026-07-28 and tool handlers do not depend on
prior tool calls. A client that still opens with the legacy `initialize`
handshake negotiates `2025-11-25`, because `initialize` is
precisely what 2026-07-28 removes: the SDK caps the legacy path on purpose. Both
answer the same through the shared service.

---

## 2. `roca mcp`: declaring the server in an agent's config

```
roca mcp install <runtime>     # claude, claude-desktop, codex, hermes, opencode, pi, zcode
roca mcp uninstall <runtime>   # or --all
roca mcp status [runtime]
```

Where each runtime keeps its configuration, and what Roca writes into it:

| Runtime | File | Key | Entry |
|---|---|---|---|
| `codex` | `$CODEX_HOME`/`~/.codex/config.toml` | `mcp_servers` | `[mcp_servers.roca]` with `command` and `args` |
| `claude` | `$CLAUDE_CONFIG_DIR`/`~/.claude.json` | `mcpServers` | `{"type": "stdio", ...}` |
| `claude-desktop` | macOS `~/Library/Application Support/Claude/claude_desktop_config.json`; Windows `%APPDATA%/Claude/claude_desktop_config.json`; Linux `$XDG_CONFIG_HOME`/`~/.config/Claude/claude_desktop_config.json` | `mcpServers` | same stdio entry as `claude` |
| `opencode` | `$OPENCODE_CONFIG`/`~/.config/opencode/opencode.json` | `mcp` | `{"type": "local", "command": [...]}` |
| `hermes` | `$HERMES_HOME`/`~/.hermes/config.yaml` | `mcp_servers` | a nested mapping |
| `pi` | `$PI_CODING_AGENT_DIR`/`~/.pi/agent/mcp.json` | `mcpServers` | `command` and `args` |
| `zcode` | `$ZCODE_HOME`/`~/.zcode/cli/config.json` | `mcp.servers` | `{"type": "stdio", "command", "args"}` |

**The file belongs to the operator.** Roca owns exactly one entry inside it and
candidate edits preserve surrounding comments, ordering, blank lines, the
JSONC OpenCode tolerates and neighbouring servers through surgical text ranges.
Publishing an edit to an existing configuration, including withdrawal, follows
the [conditional file boundary](lifecycle.md#conditional-file-publication).
An unchanged declaration is a no-op; a missing configuration can be created.
The refusal tests in `internal/distribution/agentcfg/agentcfg_test.go` verify
that existing files keep their bytes, identity and permissions.

Three more things the shared spine gives every edit: the previous bytes are
backed up first (`<file>.roca.bak`, and an earlier copy is never overwritten);
an existing symlink or other non-regular path is refused before backup or
mutation; and a file that changed underneath us aborts instead of clobbering the
runtime that owns it.

**One declared boundary.** A `codex` config that writes `mcp_servers` as an
inline table is refused by name, with the remedy, instead of being edited.
Corrupting somebody's config is worse than asking them to spell it as a table.

By default, the binary written into the entry is the absolute path of the
`roca` executable performing the installation. It is deliberately not a bare
command resolved by `PATH`: two products can share that name, and an agent must
launch the binary that wrote its declaration. `--executable` and `ROCA_BIN`
select a different binary explicitly and are normalized to absolute paths.
After an install, the command prints the runtime, configuration path, exact
declared command and backup path; JSON includes the executable too.

---

## 3. Three adoption layers

An agent learns La Roca three different ways. They stack; none replaces another.

| Layer | What it is | How the operator turns it on |
|---|---|---|
| **Prompt** | The generated `prompt.md` block for agent instructions | Init attempts installation; see [Initialize](lifecycle.md#initialize) |
| **Skill** | Three embedded skills (`roca`, `roca-operations`, `roca-vector`) plus `roca-semantica`, the semantic catalog generated from the installed plugin manifests | `roca init` installs all four (the three embedded skills plus `roca-semantica`) into every detected skill seat and names `roca` and `roca-operations` as must-read; `roca skill install <runtime>` or `--all` writes the same four as registered, zoned files |
| **MCP** | Core passthrough tools for agents with no shell, plus conditional semantic-search and playground tools | `roca mcp install <runtime>` |

```
roca skill                 # list runtimes and where the skill would land
roca skill install --all   # every supported runtime, explicitly selected
roca skill install claude  # one runtime
```

Paths (personal/global only — Roca never writes a project-local skill). Each
runtime receives `skills/roca/SKILL.md`, `skills/roca-operations/SKILL.md`,
`skills/roca-vector/SKILL.md`, and the generated catalog at
`skills/roca-semantica/SKILL.md` under the same root:

| Runtime | Skill root |
|---|---|
| `claude` | `$CLAUDE_CONFIG_DIR`/`~/.claude` |
| `codex` | `$CODEX_HOME`/`~/.codex` |
| `cursor` | `$CURSOR_HOME`/`~/.cursor` (user skills at `skills/`; never `skills-cursor/`) |
| `grok` | `$GROK_HOME`/`~/.grok` |
| `opencode` | beside `$OPENCODE_CONFIG`, else `~/.config/opencode` |
| `hermes` | `$HERMES_HOME`/`~/.hermes` |
| `pi` | `$PI_CODING_AGENT_DIR`/`~/.pi/agent` |
| `qwen` | `$QWEN_HOME`/`~/.qwen` |
| `zcode` | `$ZCODE_HOME`/`~/.zcode` (opt-in: init and update do not seed it) |

Only those files are installation targets. [Artifact lifecycle](lifecycle.md#update)
owns their SYSTEM/USER zones, no-op detection, backups, force and current
refresh refusals. The embedded skill sources live in
`internal/distribution/skill/agents.md` (which generates `roca`),
`internal/distribution/skill/OPERATIONS.md`, and
`internal/distribution/skill/VECTOR.md`, and ship inside the
binary via `go:embed`; the catalog body is composed at install time from the semantic
fragments of the installed plugin manifests, the same fragments the query
catalog composes. Plugin install, update and uninstall attempt to refresh
registered copies under that same artifact lifecycle.

### How a runtime earns a skill seat

A runtime is added only after its user-skill surface is measured on a real
machine, the same discipline `docs/agent-parsers.md` demands of a parser
store. The measurements behind the current list:

- **grok** (Grok Build): `grok inspect` lists skills that exist only under
  `~/.grok/skills/` as discovered `user` skills, both the ones this machine
  already held there before the change and all four La Roca skills after
  `roca skill install grok`, on a machine where neither `~/.claude/skills/`
  nor `~/.agents/skills/` held them. Skills installed only under
  `~/.claude/skills/` are discovered through grok's compatibility tier,
  tagged `[claude]`, and `GROK_HOME` moves the native tier.
- **qwen** (Qwen Code): with `QWEN_HOME` pointed at a probe directory holding
  `skills/probe-skill/SKILL.md`, qwen's skill-manager debug log records
  "Loading user level skills from" that directory.
- **glm**: not claimed. `~/.glm/skills/` holds skill files on this machine, but
  no `glm` binary exists anywhere measured (PATH, npm globals, shell aliases),
  so no reader can be verified; the GLM plan is used through Claude Code with
  `CLAUDE_CONFIG_DIR`, which the `claude` seat already honors.
- **cursor**: first-class skill seat. `cursor-agent` is installed at
  `~/.local/bin/cursor-agent` (symlink into `~/.local/share/cursor-agent/versions/`),
  verified by executing that path; a PATH miss alone is not "not installed".
  `~/.cursor` is the live config root (`cli-config.json`, `mcp.json`). User
  skills are `~/.cursor/skills/<name>/SKILL.md`. `~/.cursor/skills-cursor/` is
  reserved for Cursor's built-in skills and is never written. Detection is the
  `~/.cursor` directory existing; Cursor does not create `skills/` itself.
- **zcode** (Z.ai ZCode): user skills live at `~/.zcode/skills/<name>/SKILL.md`
  with YAML `name` and `description` frontmatter. MCP is nested
  `mcp.servers` in `~/.zcode/cli/config.json`. The seat is opt-in:
  `roca skill install zcode` writes it; init and update never do, even
  when `~/.zcode` already exists. Claude Desktop MCP is a separate
  `claude-desktop` target and is not written by this seat.

---

## 4. Read-only mode

`ROCA_READ_ONLY=1` refuses every write **in the service, before any database
I/O**, so both surfaces refuse with the same words and only render them
differently. Measured in scenario F08-08. Anything that does not write —
`query`, `explore`, `health` — still answers, which is exactly when an operator
who suspects something reaches for it.
