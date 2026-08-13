# The MCP plug

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

The way La Roca reaches an agent that is not typing commands: the MCP server it
can call.

`roca_query` uses the same factory order as the CLI: La Roca detects an already
signed-in supported agent CLI and needs no separate login or MCP credentials. Models
authenticate through their own CLIs; La Roca stores no secrets. Ollama and the
keyword rescue remain available when no agent CLI can serve.

---

## 1. `roca mcp serve`: the MCP over stdio

One command, in the foreground, on demand. The agent launches it, it answers
over its standard input and output, and it dies when that pipe closes. There is
no daemon, no port, no supervisor and no unit file, and that is the whole
lifecycle.

```
roca mcp serve
```

Nothing but the protocol goes to standard output. A print there corrupts the
session, which is why every diagnostic in this path writes to standard error.

### The six tools

| Tool | What it does | The caller that defends it |
|---|---|---|
| `roca_exec` | Runs a SELECT under the same gate as `roca exec` | Agents that received SQL from `roca_sql` and have no shell |
| `roca_explore` | Runs plain or deep investigation with prose, terrain, next probes, and generated SQL | Agents following evidence without a shell |
| `roca_query` | Answers a question from memory | The product's job, for an agent with no shell |
| `roca_store` | Writes one memory back | The other half of the same job |
| `roca_health` | The non-destructive checks over live data | An agent that cannot run `roca doctor` |
| `roca_sql` | Compiles a question into SQL without running it | Agents that need to inspect the SQL before `roca_exec` runs it |

`roca_query`, `roca_explore`, and `roca_sql` reject empty questions and share the CLI's generous
1000-character cap before any model is called, and the rest of that same input
gate with it: [what happens on a query](models.md#what-happens-on-a-query).

`roca_explore` is a separate tool rather than a mode on `roca_query`. That keeps
the established query schema and rows-first answer untouched while making the
investigation mode explicit at dispatch. Omitted or false `deep` is the plain
radius mission; `deep: true` is the full terrain mission. Both call the same
`Service.Explore` and `axi.Explore` as the CLI, so the MCP result has full output
parity—prose and generated SQL, with terrain and next probes required by the
selected mission—rather than returning rows for the agent to reinterpret.

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

Parity is measured, not asserted: the same question over both surfaces returns
the same `sql`, the same `queryplan`, the same rows and the same build; explore
also shares its service call and text renderer byte-for-byte
(scenario F08-04, and `internal/distribution/mcpplug/plug_test.go`).

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
| **Prompt** | A one-line aviso from `roca init` that a skill exists | Automatic on every init; install is never implied |
| **Skill** | Canonical `SKILL.md` that teaches query/store/exec and the MCP tools | `roca skill install <runtime>` or `--all` — copies one file into each selected runtime's personal skills directory |
| **MCP** | Six passthrough tools for agents with no shell | `roca mcp install <runtime>` |

```
roca skill                 # list runtimes and where the skill would land
roca skill install --all   # every supported runtime, explicitly selected
roca skill install claude  # one runtime
```

Paths (personal/global only — Roca never writes a project-local skill):

| Runtime | Skill file |
|---|---|
| `claude` | `$CLAUDE_CONFIG_DIR`/`~/.claude/skills/roca/SKILL.md` |
| `codex` | `$CODEX_HOME`/`~/.codex/skills/roca/SKILL.md` |
| `opencode` | beside `$OPENCODE_CONFIG`, else `~/.config/opencode/skills/roca/SKILL.md` |
| `hermes` | `$HERMES_HOME`/`~/.hermes/skills/roca/SKILL.md` |
| `pi` | `$PI_CODING_AGENT_DIR`/`~/.pi/agent/skills/roca/SKILL.md` |

Only that file is created or replaced. Re-running is a no-op when the bytes
already match the embedded canonical text. The skill body lives in
`internal/distribution/skill/SKILL.md` and ships inside the binary via `go:embed`.

---

## 4. Read-only mode

`ROCA_READ_ONLY=1` refuses every write **in the service, before any database
I/O**, so both surfaces refuse with the same words and only render them
differently. Measured in scenario F08-08. Anything that does not write —
`query`, `health` — still answers, which is exactly when an operator who
suspects something reaches for it.
