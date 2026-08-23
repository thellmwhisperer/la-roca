# The MCP plug

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

The way La Roca reaches an agent that is not typing commands: the MCP server it
can call.

La Roca is built agent-first, following the AXI convention (agent ergonomic
interface) shared by a family of agent-facing tools: route narration above
the data, compact TOON rows, bounded text previews, and deterministic next
commands in every answer. An agent never has to guess what it just got or
what to run next.

`roca_query` is the same deterministic hybrid search as the CLI: it uses
full-text plus an optional template-expanded vector leg and never calls an
answering model. The model-backed `roca_sql` and `roca_explore` tools use
already signed-in supported agent CLIs or local Ollama; La Roca stores no
provider secrets.

---

## 1. `roca mcp serve`: the MCP over stdio

One command, in the foreground, on demand. The agent launches it, it answers
over its standard input and output, and it dies when that pipe closes. There is
no daemon, no port, no supervisor and no unit file, and that is the whole
lifecycle.

When semantic search is enabled, the server also owns one vector companion over
private pipes for the lifetime of that session. It prepares the embedding model
in the background as the session starts, so the first semantic query does not
pay the startup cost. The companion has no port or pid file and stops with the
MCP server; one-shot CLI queries load the model for that invocation only. Its
preparation status goes to standard error, leaving protocol output untouched.

The same session parent raises every installed plugin that declares a
`companion` in `plugin.json`. Each child is exec'd from the plugin directory
with that declaration's fixed argv, over private pipes, and is reaped when
serve exits. A companion crash is logged and retried with bounded backoff; a
companion that keeps dying is reported once and left down. Queries continue
either way. Plugins that omit the field are unchanged. The declaration and
degradation contract live in [the plugin manifest](plugins.md#session-companions).

```
roca mcp serve
```

Nothing but the protocol goes to standard output. A print there corrupts the
session, which is why every diagnostic in this path writes to standard error.

### The core tools and semantic search

| Tool | What it does | The caller that defends it |
|---|---|---|
| `roca_exec` | Runs a SELECT under the same gate as `roca exec` | Agents that received SQL from `roca_sql` and have no shell |
| `roca_explore` | Runs plain or deep investigation with prose, terrain, next probes, and generated SQL | Agents following evidence without a shell |
| `roca_query` | Returns labeled hybrid FTS/vector evidence; `top`, `require_both`, and `databases` match the CLI | An agent searching memory without a shell |
| `roca_store` | Writes one memory back | The other half of the same job |
| `roca_health` | The non-destructive checks over live data | An agent that cannot run `roca doctor` |
| `roca_sql` | Compiles a question into SQL without running it | Agents that need to inspect the SQL before `roca_exec` runs it |

With semantic search enabled and its companion available, the same server also
exposes `roca_vector_query`. It searches selected local indexes by meaning and
uses the session-resident, pre-prepared model described above. The six core
tools remain available whether or not semantic search is enabled.

`roca_query`, `roca_explore`, and `roca_sql` reject empty questions and share
the CLI's generous 1000-character cap before work begins. The model-backed
tools also follow the playground input and SQL gate:
[what happens in the playground](models.md#what-happens-in-the-playground).

`roca_explore` is a separate tool rather than a mode on `roca_query`. That keeps
the established query schema and rows-first answer untouched while making the
investigation mode explicit at dispatch. Omitted or false `deep` is the plain
radius mission; `deep: true` is the full terrain mission. Both call the same
`Service.Explore` and `axi.Explore` as the CLI, so the MCP result has full output
parity: prose and generated SQL, with terrain and next probes required by the
selected mission, rather than returning rows for the agent to reinterpret.

`roca_list_runs` is **not** in v1: `runs` is v2 scope and this binary creates no
such table. A tool with nothing behind it is a tool that lies.

### Answers are TOON text

Every tool answers as compact TOON rows in plain text, the same shape the CLI
prints: cheap for a model to read, with the route narration above the data.
The server never returns row envelopes in `StructuredContent`; that contract
is pinned by `internal/distribution/mcpplug/toon_contract_test.go`.

### The law of this surface

**Every handler is a single call into the service.** It is not a comment, it is
two structural tests over `internal/distribution/mcpplug/handlers.go`
(`passthrough_test.go`): the body of a handler must be one return statement into
the service, and the file may contain no control flow at all. A handler that
needs an `if` needs it in the service, where the shell can reach it too.

Parity is measured, not asserted: `roca_query` and the CLI return the same
labeled hits and build from one service call; explore likewise shares its
service call and text renderer byte-for-byte
(`internal/distribution/mcpplug/plug_test.go`).

### On the protocol version

The SDK's latest revision is 2026-07-28 and the server keeps no state between
calls, which is what that revision is for. A client that still opens with the
legacy `initialize` handshake negotiates `2025-11-25`, because `initialize` is
precisely what 2026-07-28 removes: the SDK caps the legacy path on purpose. Both
answer the same, since there is nothing in the process to carry over.

---

## 2. `roca mcp`: declaring the server in an agent's config

```
roca mcp install <runtime>     # codex, claude, opencode, hermes, pi
roca mcp uninstall <runtime>   # or --all
roca mcp status [runtime]
```

Where each runtime keeps its configuration, and what Roca writes into it:

| Runtime | File | Key | Entry |
|---|---|---|---|
| `codex` | `$CODEX_HOME`/`~/.codex/config.toml` | `mcp_servers` | `[mcp_servers.roca]` with `command` and `args` |
| `claude` | `$CLAUDE_CONFIG_DIR`/`~/.claude.json` | `mcpServers` | `{"type": "stdio", ...}` |
| `opencode` | `$OPENCODE_CONFIG`/`~/.config/opencode/opencode.json` | `mcp` | `{"type": "local", "command": [...]}` |
| `hermes` | `$HERMES_HOME`/`~/.hermes/config.yaml` | `mcp_servers` | a nested mapping |
| `pi` | `$PI_CODING_AGENT_DIR`/`~/.pi/agent/mcp.json` | `mcpServers` | `command` and `args` |

**The file belongs to the operator.** Roca owns exactly one entry inside it and
every other byte comes back untouched: comments, ordering, blank lines, the
JSONC OpenCode tolerates and the neighbouring servers. That is why the edits are
surgical text-range edits and not a parse-and-reserialize round trip, which is
easy and eats comments. It is measured the only way that is not a matter of
opinion: installing and then withdrawing gives back the exact previous bytes
(`internal/distribution/agentcfg/agentcfg_test.go`, five runtimes).

Two more things the shared spine gives every edit: the previous bytes are backed
up first (`<file>.roca.bak`, and an earlier copy is never overwritten), and a file
that changed underneath us aborts instead of clobbering the runtime that owns
it.

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
| **Prompt** | The generated `prompt.md` block for agent instructions | Automatic on every init |
| **Skill** | Three embedded skills (`roca`, `roca-operations`, `roca-vector`) plus `roca-semantica`, the semantic catalog generated from the installed plugin manifests | `roca init` installs the three embedded skills into every detected skill seat and names `roca` and `roca-operations` as must-read; `roca skill install <runtime>` or `--all` writes all four as registered, zoned files |
| **MCP** | Six passthrough tools for agents with no shell | `roca mcp install <runtime>` |

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

Only those files are created or refreshed. Explicit markers divide the shipped
SYSTEM zone from the operator's USER zone. Re-running is a no-op when SYSTEM
already matches; otherwise USER is transplanted verbatim and the previous file
is backed up. An edited SYSTEM zone is left alone unless the operator passes
`--force`. The embedded skill sources live in
`internal/distribution/skill/agents.md` (which generates `roca`),
`internal/distribution/skill/OPERATIONS.md`, and
`internal/distribution/skill/VECTOR.md`, and ship inside the
binary via `go:embed`; the catalog body is composed at install time from the semantic
fragments of the installed plugin manifests, the same fragments the query
catalog composes, and every `roca plugin install`, `update` and `uninstall`
regenerates it in each runtime where it is registered.

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

---

## 4. Read-only mode

`ROCA_READ_ONLY=1` refuses every write **in the service, before any database
I/O**, so both surfaces refuse with the same words and only render them
differently. Measured in scenario F08-08. Anything that does not write —
`query`, `explore`, `health` — still answers, which is exactly when an operator
who suspects something reaches for it.
