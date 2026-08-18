# Operations: memory layers, audit logs, redaction, retention

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

That exact command hook inside `PreToolUse` is the artifact's SYSTEM fragment;
the enclosing group, surrounding Claude settings, and every other hook are its
USER zone. Its `hooks run claude` command is the explicit ownership marker
recorded in `~/.roca/artifacts.json`. Refresh never rewrites the surrounding
settings, and an edited fragment is left alone until
`roca hooks install claude --force` replaces it. See
[Update](lifecycle.md#update) for the shared zone, divergence and registry
contract.

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

## Memory layers

`roca store --layer <name>` accepts only a name in the live layer registry and
lists the registered layers when it refuses a write. This validation is shared
by the CLI and MCP store paths.
The supported catalogue surface is the unqualified `layers` compatibility view
and the `roca layers` registry commands; explicitly qualified physical
references such as `main.layers` intentionally expose physical storage.

Register a deliberate custom layer with `roca layers add <name>`. To repair
existing rows that used the wrong layer, run
`roca layers migrate <from> <registered-to>`. `roca doctor` reports
`runtime_layers_not_in_registry` drift and prints the exact `roca layers add`
command for each unknown runtime layer; migration remains available when the
right repair is to move those memories into an existing layer instead. Both
repair commands follow the same selected database and `roca-ops` routing as
`roca store`; the command printed by doctor includes the matching `--db-path`.

Every CLI command and MCP tool call dual-writes one redacted record to the
bundled ops database and to JSONL under the selected data directory's `logs/`,
whether it succeeds or fails. Either sink may fail independently: the surviving
sink is still written, one warning is emitted, and the observed command or tool
result never changes. Query result rows are written to neither sink.

## Streams and contents

The dated `executions`, `mcp-audit`, `ingest`, and `migrations` JSONL streams
retain at most 30 days. Each file is capped at 5 MiB and each stream keeps at
most six files, so a busy installation cannot grow a stream beyond 30 MiB.
Consumers should glob `<stream>-*.jsonl`; rotated segments have the same prefix.
An individual record larger than the file cap is dropped under the same
non-failing writer contract. This DATA SPLIT stage deliberately keeps rotation
and both call-stream writes unchanged as the rollback path; retiring those two
JSONL streams requires a separately proven rollback transition. The ops copy
has no automatic expiry, and its retention policy cannot prune corpus or cron.

`executions` and `mcp-audit` share one top-level call contract. Surface-specific
fields are `command` plus `flags` for CLI and `tool` for MCP:

```json
{"timestamp":"2026-08-12T10:30:00Z","source":"mcp","tool":"roca_query","args":{"query":"find the synthetic lighthouse"},"ok":false,"error":"the generated SQL was rejected","error_type":"invalid_sql","duration_ms":184,"question":"find the synthetic lighthouse","sql":"SELECT missing FROM memories","model_sql":"SELECT missing FROM memories","sql_provider":"codex","sql_model":"gpt-synthetic","row_count":0,"fallback_reason":"invalid_sql","retry_type":"gate_rejection","retry_reason":"no such column: missing","correlation_id":"qf_0123456789abcdef"}
```

The additive `call_id` is the durable ops identity: it equals the correlation ID
when one exists and otherwise is derived from the retained segment and line.
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

Retained `executions` and `mcp-audit` segments are backfilled into ops in one
bounded transaction per segment. Segment digests and source-line identities
make a retry idempotent and resumable; parseable rows commit once, while
malformed and unreadable counts keep their existing meaning. `roca doctor`
switches to the ops reader only after that retained set reaches parity. If the
ops parity or read fails, it rolls back to the JSONL reader without changing
the diagnosis.

Doctor reports the number of failed query calls in the last 24 hours, on either
surface: `query`, `explore`, `roca_query`, `roca_explore`, and `roca_sql`. It
renders the five newest errors with their source, type, and correlation
ID. `roca doctor --json` exposes the same data under
`query_failures` for automation. It only opens the dated segments that can hold
a record inside the window. Malformed historical lines are skipped and counted
in `malformed_lines`, and a segment that cannot be read is skipped and counted
in `unreadable_files`: the count and the five newest errors still describe
everything that could be read, and the read failure is a warning, not a failed
diagnosis.

## Support report

`roca doctor --report` is the shareable diagnosis for a remote maintainer. It
prints one fenced text block with a generation timestamp; `roca doctor --report
--json` emits the same snapshot as JSON. The collector is read-only: it does
not install plugins, adopt schema, prepare the federation hub, or change
`layout.serving`.

The block is ordered and bounded so it stays pasteable regardless of corpus
size:

1. **Identity** — version, commit, OS/arch, and install shape (`default-home`
   or `custom-data-dir`). Binary location is a shape (`home-local`, `prefix`,
   `other`), never an absolute path.
2. **Plugins** — each installed package's name, version, origin (`bundled` vs
   `external`), checksum, and whether its state directory exists. A local
   filesystem source is reported as `local-directory`.
3. **Feature flags** — on/off for every `features.*` switch.
4. **Federation** — first-class mode (`fresh`, `legacy-only`, `migrating`,
   `federated`), the `layout.serving` marker, where corpus text actually lives
   (`legacy-core`, `plugin-corpus`, `split`, or `empty`), which stores exist
   with family row counts, and named migration states. A fresh init, a
   core-only legacy home, and a cutover home are distinguishable at a glance.
5. **Health** — pass/warn/fail/skipped per check name, never finding rows.
6. **Vector** — when `plugins/vector/state/vector.db` exists: model,
   dimensions, chunk totals by kind, store size, and the last recorded delta
   counts.
7. **Ingest** — detected agent names and the latest `ingest_file_state`
   timestamp. No source paths.

The report never includes conversation text, memory bodies, file paths outside
the `~/.roca` layout names, or person names. Corpus-scale totals are the only
counts.

## Redaction

Before a record reaches either sink, redaction covers sensitive field names;
bearer and key/value secrets; PEM private keys; OpenAI `sk-*`, GitHub
`gh[pousr]_*` and `github_pat_*`, Slack `xox*`, JWT `eyJ*`, AWS `AKIA*`, and
Google `AIza*` credential shapes.

Log directories and files are created with operator-only permissions.
The public contract, JSONL adapter, rotation, retention, and redaction are owned
by `internal/distribution/logfile`; durable ops persistence is owned by
`internal/distribution/callhistory` and the schema in
`internal/distribution/rocaops/schema.sql`.

## Read-only boundary

`ROCA_READ_ONLY=1` refuses writes in the shared service before database I/O,
so CLI and MCP enforce the same boundary. Installing the bundled
[`roca-corpus`](plugins.md#the-bundled-roca-corpus-plugin) archive is itself a
write, so a read-only run never places it: on an installation that does not have
it yet, answers cover core only and carry that omission as a warning. The
durable half of the call log is database I/O under the same rule: a read-only
run writes and backfills no call history, and `roca doctor` reads its failure
history from JSONL, so an audit leaves the machine exactly as it found it.

## Exact duplicate maintenance

`roca dedup [database ...]` is an explicit, exact-payload maintenance surface.
With no path it targets only the federated `roca-corpus` and `roca-ops`
databases. An explicitly named legacy `roca.db` may be inspected as read-only
evidence, but apply refuses it: pre-federation rows remain untouched. The
default dry run opens each physical database read-only and reports, per
table, the exact and ambiguous groups observed at rest, then the certified
apply set after session IDs are canonicalized. This makes session-induced child
duplicates visible instead of hiding them inside a changed aggregate. Row
counts before and after and same-identity groups whose payloads differ travel
beside those two views. Divergent groups are evidence for a future key decision
and are never deleted. The four governed tables are
`memories`, `sessions`, `exchanges`, and `thinking_blocks`; session winners are
resolved first so child payloads are compared using canonical session IDs.

An apply is deliberately not inferred from a dry run and is restricted to the
two federated custody databases. First freeze writes,
create a verified copy with `--backup-out`, and retain the printed manifest
SHA-256. Apply exactly one physical database at a time with `--apply`, the same
`--expected-manifest`, and `--backup`. The command rechecks the manifest under
its immediate write lock, performs remaps, reference rewrites, deletes, FTS
rebuilds, and unique exact-payload index creation in one transaction, then runs
duplicate, FTS, foreign-key, and integrity acceptance checks before commit. A
changed source fails the drift gate without a partial cleanup. Repeating a
completed run is safe: no exact losers remain and all guards and audit tables
are created idempotently.

Every deleted public identity remains in its table-specific `*_id_remaps`
audit table. `roca memory resolve <id>` follows the memory remap and returns the
canonical ID explicitly, so references retained outside SQLite do not become
silent misses. Normal store writes use the same complete-payload law inside the
serialized write transaction and return `skipped_duplicate: true` with that
canonical ID on an exact retry. A difference in metadata, provenance, project,
status, supersedes, expiry, or authorship is not a duplicate.

## Data directory

The default data directory is `~/.roca`. It contains `roca.db`,
configuration, backups, `prompt.md`, and operational JSONL under
`logs/`. The machine-wide managed-artifact registry is `artifacts.json` in the
default `~/.roca` home even when a database is selected elsewhere. La Roca does
not edit agent instruction files; the operator decides whether to use the
generated prompt or install the bundled skill.

Experimental plugin packages are not part of the selected data directory: they
live under `~/.roca/plugins`, and protected removals are archived beside them.
The bundled `roca-cron` plugin keeps its canonical journey database there too;
it observes the selected data directory's existing `logs/.roca.lock` without
owning it. The durable half of the call log is plugin-owned the same way: it
lives in `roca-ops/roca-ops.db` under that tree whichever data directory the
JSONL copy is written to. See [Plugins](plugins.md#scheduled-rides).
