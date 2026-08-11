# Operations: logs, redaction, retention

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

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
file error and every discarded source record with its path, parser, record
position and reason.

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
