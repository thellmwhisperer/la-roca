# The plug and the hooks

The two ways La Roca reaches an agent that is not typing commands: the MCP
server it can call, and the session hooks that hand it context before it asks.

Sources: PRD 3.5 (the v1 MCP surface) and job J3, TECH-SPEC 1.7 and 1.8, and the
the decision on open question A-1, option (b): the hooks enter v1 for the
`claude` runtime.

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
| `roca_query` | Answers a question from memory | The product's job, for an agent with no shell |
| `roca_store` | Writes one memory back | The other half of the same job |
| `roca_teach` | Teaches the compiler an example and retrains in place | Correcting the route with no redeploy |
| `roca_health` | The non-destructive checks over live data | An agent that cannot run `roca doctor` |
| `roca_sql` | Compiles a question into SQL without running it | Agents that need to inspect the SQL before `roca_exec` runs it |

`roca_list_runs` is **not** in v1: `runs` is v2 scope and this binary creates no
such table. A tool with nothing behind it is a tool that lies.

### The law of this surface

**Every handler is a single call into the service.** It is not a comment, it is
two structural tests over `internal/mcpplug/handlers.go`
(`passthrough_test.go`): the body of a handler must be one return statement into
the service, and the file may contain no control flow at all. A handler that
needs an `if` needs it in the service, where the shell can reach it too.

Parity is measured, not asserted: the same question over both surfaces returns
the same `sql`, the same `queryplan`, the same rows and the same build
(scenario F08-04, and `internal/mcpplug/plug_test.go`).

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
(`internal/agentcfg/agentcfg_test.go`, five runtimes).

Two more things the shared spine gives every edit: the previous bytes are backed
up first (`<file>.bak`, and an earlier copy is never overwritten), and a file
that changed underneath us aborts instead of clobbering the runtime that owns
it.

**One declared boundary.** A `codex` config that writes `mcp_servers` as an
inline table is refused by name, with the remedy, instead of being edited. The
laboratory handles that shape; this version does not, and corrupting somebody's
config is worse than asking them to spell it the ordinary way.

By default, the binary written into the entry is the absolute path of the
`roca` executable performing the installation. It is deliberately not a bare
command resolved by `PATH`: two products can share that name, and an agent must
launch the binary that wrote its declaration. `--executable` and `ROCA_BIN`
select a different binary explicitly and are normalized to absolute paths.
After an install, the command prints the runtime, configuration path, exact
declared command and backup path; JSON includes the executable too.

---

## 3. `roca hook`: the session lifecycle (job J3)

**The law:** a hook is a subprocess on the critical path of somebody's session.
It reaches the kernel by running a command and reading its standard output.
Never the database directly, never the MCP. Scenario F11-06 measures it over the
settings file itself.

### Wiring it up

```
roca hook install claude
roca hook uninstall claude
roca hook status
```

It writes into `$CLAUDE_CONFIG_DIR`/`~/.claude/settings.json` — which is not the
same file as the MCP config — one command per lifecycle event:

| Event | Command | Why |
|---|---|---|
| `SessionStart` | `roca hook context --runtime claude` | Hands the session what it should already know |
| `PreCompact` | `roca hook record --trigger precompact` | The context is about to be lost |
| `SessionEnd` | `roca hook record --trigger session_end` | The session ended |

`Stop` is deliberately absent. It fires on every turn, and in the laboratory it
exists to keep the live session hot through the incremental ingest; in v1 that
engine is `roca ingest`, so a `Stop` hook would be a subprocess on every single
turn with nothing to do. It comes back the day it has a referent.

Roca owns the entries whose command runs `roca hook`, recognized by shape and
not by exact string, so a hook installed with an absolute path is still
withdrawn by a plain `uninstall`. A hook the user wrote survives both. Outside
the `hooks` member not one byte moves; inside it Roca re-serializes, which is
the laboratory's own trade and the honest one.

### The transport

```
roca hook context [--runtime claude] [--json] [--max-chars N] [--project P] [--pills a,b]
roca hook handoff [--json] [--max-chars N]
roca hook record --trigger precompact|session_end [--session-id ID] [--cwd DIR]
```

`record` reads the runtime's event payload from standard input when there is
one, so the session and the working directory come from the runtime itself and
not from what a settings file happened to hard-code.

**Exit codes are a contract.** The transport commands never exit non-zero: they
run on the critical path of a session, and a hook that fails is a hook that can
break it. Whatever went wrong goes to standard error and the session carries on
with less context. On a machine with no Roca on it they stay silent rather than
printing noise into every session. The wiring commands are ordinary tools and
report failure with 1.

### The injection budget

Everything a hook pushes into a fresh session competes with the user's own
prompt for the same window, so the block is capped, trimmed against a published
rule, and reported in numbers a caller can assert on.

The rule is **fair-share water filling**: when the sections do not fit, each gets
an equal slice of what is left; a section that needs less than its slice gives
the surplus back; and a section that cannot even hold its floor
(`MinSectionChars`, 160) is dropped whole instead of cut into a fragment that
reads like a whole thing saying something else. One oversized handoff therefore
cannot starve the pills.

Whatever did not fit is not lost: the block always ends by pointing back at La
Roca, so the agent digs on demand.

| Setting | Where | Default |
|---|---|---|
| Injection cap | `ROCA_SESSIONSTART_MAX_CHARS`, then `hooks_max_chars` in the config | 12000 |
| Project scope | `--project`, then `ROCA_PROJECT` | the memories with no project |
| Pill roster | `--pills` (declare it empty to serve none) | what La Roca serves |

`--json` returns the block together with the budget report: the limit, what was
used, what the pointer cost and the state of every section (`full`, `trimmed`,
`dropped`) with its characters before and after.

### What a session receives

The rostered pills and the three newest handoffs of its scope. The roster is
decided by data and not by code: a pill joins it by existing and leaves it by
setting `session_start` false in its own metadata; ordering comes from
`pill_order`, then the slug. A recompiled pill is a newer row for the same slug,
so the newest row is the live one.

---

## 4. Three adoption layers

An agent learns La Roca three different ways. They stack; none replaces another.

| Layer | What it is | How the operator turns it on |
|---|---|---|
| **Prompt** | A one-line aviso from `roca init` that a skill exists | Automatic on every init; install is never implied |
| **Skill** | Canonical `SKILL.md` that teaches query/store/teach/exec and the MCP tools | `roca skill install [runtime]` — copies one file into each runtime's personal skills directory |
| **MCP** | Six passthrough tools for agents with no shell | `roca mcp install <runtime>` |

```
roca skill                 # list runtimes and where the skill would land
roca skill install         # every supported runtime
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
`internal/skill/SKILL.md` and ships inside the binary via `go:embed`.

---

## 5. Read-only mode

`ROCA_READ_ONLY=1` refuses every write **in the service, before any database
I/O**, so both surfaces refuse with the same words and only render them
differently. Measured in scenario F08-08. Anything that does not write —
`query`, `health`, `hook context` — still answers, which is exactly when an
operator who suspects something reaches for it.
