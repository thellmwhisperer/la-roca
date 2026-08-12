# Operations: logs, redaction, retention

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

## Memory authorship

Every new memory stores a system-stamped harness, model and write surface in
`memories.source_agent`, `source_model` and `source_surface`. MCP uses the
connected client's handshake `clientInfo`; the CLI's primary path is explicit
`roca store --agent <harness> --model <model>`. Best-effort CLI environment and
process detection accepts only one unambiguous harness and otherwise records
`unknown`. Existing rows remain NULL and are rendered as unknown; La Roca never
retroactively guesses. The three names are reserved: `agent`, `model` and
`surface` in `--metadata` are refused with the flags to use instead, so no tag
is ever silently dropped.

`roca hooks install claude` adds a Claude Code `PreToolUse` hook for Bash. It
signs `roca store` commands with `--agent claude` and the latest model recorded
in Claude's own transcript, or `unknown` when that direct evidence is absent.
The entry launches this executable's absolute path, overridable with
`--executable` or `ROCA_BIN`, because Claude runs hooks in a non-interactive
shell that does not read an interactive `PATH`. Reinstalling repoints an entry
whose binary moved instead of adding a second one.

`roca hooks uninstall claude` withdraws that entry and leaves every other
setting, and every hook that is not La Roca's, exactly as it was. `roca
uninstall` does the same withdrawal before it unlinks the binary, so no hook
survives calling a command that is gone. Settings La Roca cannot read stop the
install, which cannot safely edit what it cannot parse, but never stop either
withdrawal: the file is left byte for byte as it is, and one warning line on
stderr names it and the `hooks run claude` entry to delete by hand.

Other harnesses can use the same client-side pattern: intercept the shell tool,
read identity only from a harness-owned session source, and inject both flags;
no other hook installer ships yet.

Every CLI execution writes a redacted JSONL record under the selected data
directory's `logs/`. A logging failure warns once on stderr but never changes
a command or tool result.

## Streams and contents

The dated `executions`, `mcp-audit`, and `ingest` JSONL streams retain 30
days.

Execution records store the command, changed flags, database path, duration,
exit code, error and result metadata. Query records keep the question, route,
provider, model, SQL, timings, degradation, provider failure text and row
count; they never store result row contents. MCP audit records store the
tool, redacted arguments, verdict, degraded state, duration and result row
count. Ingest records retain the complete ingest envelope, including every
file error, exact excluded and discarded totals, collapsed reasons, and up to
100 source-record details with their path, parser, record position and reason.

No log is stored in SQLite and no run tables exist.

## Redaction

Before a line reaches disk, redaction covers sensitive field names; bearer
and key/value secrets; PEM private keys; OpenAI `sk-*`, GitHub `gh[pousr]_*`
and `github_pat_*`, Slack `xox*`, JWT `eyJ*`, AWS `AKIA*`, and Google `AIza*`
credential shapes.

Log directories and files are created with operator-only permissions.
Retention and redaction are owned by `internal/distribution/logfile`.

## Read-only boundary

`ROCA_READ_ONLY=1` refuses writes in the shared service before database I/O,
so CLI and MCP enforce the same boundary.

## Data directory

The default data directory is `~/.roca`. It contains `roca.db`,
configuration, credentials, backups, `prompt.md`, and operational JSONL under
`logs/`. La Roca does not edit agent instruction files; the operator decides
whether to use the generated prompt or install the bundled skill.
