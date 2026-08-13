# Operations: audit logs, redaction, retention

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

Every CLI command and MCP tool call writes one redacted JSONL record under the
selected data directory's `logs/`, whether it succeeds or fails. A logging
failure warns on stderr but never changes the observed command or tool result.
No operational log is stored in SQLite.

## Streams and contents

The dated `executions`, `mcp-audit`, `ingest`, and `migrations` JSONL streams
retain at most 30 days. Each file is capped at 5 MiB and each stream keeps at
most six files, so a busy installation cannot grow a stream beyond 30 MiB.
Consumers should glob `<stream>-*.jsonl`; rotated segments have the same prefix.
An individual record larger than the file cap is dropped under the same
non-failing writer contract.

`executions` and `mcp-audit` share one top-level call contract. Surface-specific
fields are `command` plus `flags` for CLI and `tool` for MCP:

```json
{"timestamp":"2026-08-12T10:30:00Z","source":"mcp","tool":"roca_query","args":{"query":"find the synthetic lighthouse"},"ok":false,"error":"the generated SQL was rejected","error_type":"invalid_sql","duration_ms":184,"question":"find the synthetic lighthouse","sql":"SELECT missing FROM memories","model_sql":"SELECT missing FROM memories","sql_provider":"codex","sql_model":"gpt-synthetic","row_count":0,"fallback_reason":"invalid_sql","retry_type":"gate_rejection","retry_reason":"no such column: missing","correlation_id":"qf_0123456789abcdef"}
```

The stable fields are:

- `timestamp`, `source`, `args`, `ok`, `duration_ms`, and `row_count` on every
  call; `command` or `tool` identifies the operation.
- `args` is present on every call and its JSON shape follows the surface: a
  string array of the positional arguments for CLI commands and plugin
  commands, and the tool argument object for MCP calls, or the raw argument
  text as a string when that payload is not valid JSON.
- `error` and `error_type` on failures. `error_type` is a declared category and
  never a Go type name: `invalid_sql`, `model_error`, `model_unavailable`,
  `sql_execution_error`, `sql_execution_timeout`, `not_initialized`,
  `invalid_usage`, `not_found`, `permission_denied`, `already_exists`,
  `timeout`, `canceled`, `command_failure`, `tool_error`, or
  `unclassified_error` when this build cannot categorize the failure. Further
  categories may be declared; the vocabulary is the contract, and the Go error
  behind it may change without changing the line.
- `correlation_id` on every recorded failure, expected or not, so what the
  operator saw always names its log line: a read-only gate rejection, a rejected
  SQL statement, a degraded query and an external plugin that exited non-zero
  are correlated exactly like an unexpected exception. An error carries the ID
  in its own text. A failure with no error text to carry it, which is every
  command that exits non-zero after reporting the failure itself, gets a
  `correlation_id` line on the shell's error stream, where no answer is parsed
  from; the MCP surface appends the same line to the tool result it marks as an
  error. The one failure that is recorded without being surfaced is an external
  plugin's own non-zero exit: its arguments, streams and exit status cross the
  plugin seam untouched, so its ID is read back from the log through
  `roca doctor` rather than written into output the plugin owns.
- Query calls add `question`, `sql`, `model_sql`, `sql_provider`, `sql_model`,
  phase timings, and any `degraded`, `fallback_reason`, `retry_reason`, provider
  note, or `queryplan`. `sql` is the cleaned model-generated statement,
  including when a deterministic rescue later answers the call. `model_sql` is
  the model's exact answer; `raw_sql` remains its compatibility alias when that
  answer differs from `sql`. The provider field names match the query result
  envelope; they do not depend on memory authorship schema changes.
- Both query surfaces record route and retry provenance at the top level:
  `path`, `retried`, `retried_sql`, `retry_type`, `model_sql`, the failed
  `first_model_sql`, and the `sql_retry_inference_ms` and
  `sql_retry_provider_latency_ms` subsets of the SQL phase. A degraded query
  keeps its non-empty diagnostic payload there even when the literal rescue
  later supplies rows.

Optional fields are omitted when they do not apply. New compatible fields may
be added, but the names and meanings above are the consumer contract. Query
records never contain result row contents. The CLI's existing row-free `result`
metadata remains for compatibility.

`ingest` records retain each run's verdict and complete bounded ingest envelope,
including file errors, exact excluded and discarded totals, collapsed reasons,
and up to 100 source-record details with path, parser, record position, and
reason. `migrations` records retain each `roca init` schema-adoption verdict,
repairs, and failure. Both streams are plain files beside the call audit.

## Reading query failures

`roca doctor` reads the two call streams without touching the database. It
reports the number of failed `query`/`roca_query`/`roca_sql` calls in the last
24 hours and renders the five newest errors with their source, type, and
correlation ID. `roca doctor --json` exposes the same data under
`query_failures` for automation. It only opens the dated segments that can hold
a record inside the window. Malformed historical lines are skipped and counted
in `malformed_lines`, and a segment that cannot be read is skipped and counted
in `unreadable_files`: the count and the five newest errors still describe
everything that could be read, and the read failure is a warning, not a failed
diagnosis.

## Redaction

Before a line reaches disk, redaction covers sensitive field names; bearer
and key/value secrets; PEM private keys; OpenAI `sk-*`, GitHub `gh[pousr]_*`
and `github_pat_*`, Slack `xox*`, JWT `eyJ*`, AWS `AKIA*`, and Google `AIza*`
credential shapes.

Log directories and files are created with operator-only permissions.
The contract, reader, rotation, retention, and redaction are owned by
`internal/distribution/logfile`.

## Read-only boundary

`ROCA_READ_ONLY=1` refuses writes in the shared service before database I/O,
so CLI and MCP enforce the same boundary.

## Data directory

The default data directory is `~/.roca`. It contains `roca.db`,
configuration, backups, `prompt.md`, and operational JSONL under
`logs/`. La Roca does not edit agent instruction files; the operator decides
whether to use the generated prompt or install the bundled skill.

Experimental plugin packages are not part of the selected data directory: they
live under `~/.roca/plugins`, and protected removals are archived beside them.
See [Plugins](plugins.md).
