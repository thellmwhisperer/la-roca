# Plugins

La Roca is a federating kernel. A plugin owns its durable databases and
describes them in a `plugin.json` manifest; the kernel validates those
declarations, attaches the databases read-only, and composes their table
descriptions into the catalog used by natural-language queries.

The kernel's own attach point is an empty in-memory SQLite database. While the
migration below is in progress, queries still attach to the compatibility
connection that holds the rows no plugin owns yet. Either way a plugin database
is opened read-only and reached only through its declared alias.

The kernel does not own a domain database. Its durable state is configuration
and installed manifests. Database retention, pruning, compaction, and scale are
plugin policy: one plugin cannot prune another plugin's history.

The manifest engine is landing incrementally. `roca-corpus` is its first
manifest-backed consumer. `roca-ops` and `roca-cron` already own separate
databases and are the next bundled domains to move from their legacy package
descriptors. Existing data that has not yet moved remains readable during this
transition; that compatibility path is not a plugin API.

## Current system map

```text
CLI and MCP
    |
    v
+----------------------------------------------------------+
| kernel                                                   |
| init | manifest validation | read-only gate | NL-to-SQL |
| config + manifests on disk | SQLite attach hub in memory |
+--------------------------+-------------------------------+
                           |
             read-only attachments and executable calls
             +-------------+-------------+
             |                           |
             v                           v
    +------------------+       +------------------+
    | roca-corpus      |       | roca-ops         |
    | ingest           |       | store/query/SQL  |
    | corpus database  |       | ops database     |
    | archive policy   |       | operations policy|
    +------------------+       +------------------+

    roca-cron owns its own journey database for run history and errors.
```

`roca-corpus` proves the engine without changing the product contract: it is
resident, ingest still writes and indexes the same data, query results are
unchanged, and readable query output still begins with the same consulted
database list.

## The manifest

`plugin.json` schema 1 has five required parts:

- identity: `name`, `version`, and `binary`;
- databases: every SQLite file, its declared attach alias, attachment mode,
  custody, and plugin-owned retention policy;
- semantic fragment: the tables and questions served by each database;
- verbs: one canonical command name and description, projected to CLI and MCP;
- capabilities: named executable calls used when SQL cannot perform the work.

The canonical verb `inspect`, for example, names CLI command `inspect` and MCP
tool `roca_inspect`. Both resolve to the same capability. A manifest cannot give
the two surfaces different implementations. Registration lands in steps: this
build seats a declared verb as a CLI command for bundled manifest plugins only.

Here is a complete two-database plugin. The second database demonstrates the
same shape used by a scheduler that keeps run history and errors apart from its
domain records.

```json
{
  "schema": 1,
  "name": "receipts",
  "version": "1.0.0",
  "binary": "roca-receipts",
  "databases": [
    {
      "name": "records",
      "path": "receipts.db",
      "alias": "receipts_records",
      "attachment": "on-demand",
      "custody": true,
      "retention": "Keep receipt records until the operator removes them."
    },
    {
      "name": "runs",
      "path": "receipts-runs.db",
      "alias": "receipts_runs",
      "attachment": "on-demand",
      "retention": "Keep failures; prune successful runs after 30 days."
    }
  ],
  "semantic": {
    "databases": [
      {
        "database": "records",
        "description": "Purchase receipts and their totals.",
        "questions": ["Which receipts were recorded?"],
        "tables": [
          {
            "name": "receipts",
            "description": "One row per purchase receipt.",
            "questions": ["How much did a purchase cost?"],
            "columns": ["id", "title", "amount_cents"]
          }
        ]
      },
      {
        "database": "runs",
        "description": "Import attempts and their errors.",
        "questions": ["Which receipt imports failed?"],
        "tables": [
          {
            "name": "import_runs",
            "description": "One receipt import attempt.",
            "columns": ["id", "started_at", "error"]
          }
        ]
      }
    ]
  },
  "verbs": [
    {
      "name": "receipts",
      "description": "Import and inspect purchase receipts.",
      "capability": "receipts"
    }
  ],
  "capabilities": [
    {
      "name": "receipts",
      "command": ["receipts"]
    }
  ]
}
```

The engine rejects unknown fields, unsupported schema versions, unsafe paths or
aliases, repeated names, a semantic fragment that names an undeclared
database, a table declaration that disagrees with the real SQLite schema, and
a verb that names a missing capability. Discovery reports malformed manifests
as actionable errors; it never silently ignores them. Attach aliases are
explicit and collisions are errors rather than names the kernel rewrites.

### Database declarations

`path` is one regular file shipped in the package. It must be a safe filename,
not a path outside the package. `alias` is the exact SQLite schema used in
qualified SQL, such as `receipts_records.receipts`.

`attachment` is `resident` or `on-demand`. A resident database is available to
every query connection. An on-demand database is selected when its semantic
fragment matches the question or explicit SQL names its alias. Both modes use
SQLite read-only URI mode, the same SQL gate and timeout, and the same
attachment limit.

`custody: true` says the database contains operator-owned data that uninstall
must archive instead of delete. `retention` documents the policy owned and
implemented by the plugin. The kernel displays and validates the declaration;
it does not enforce another plugin's pruning rules.

When a plugin declares one database, result provenance is `plugin:<name>`.
With several databases, provenance is `plugin:<name>/<database>`. Filesystem
paths never enter query output.

### Semantic fragments

Every database needs exactly one semantic entry. Its description, at least one
question across the database or its tables, every visible table description,
and each ordered column list are required. The declaration is checked against
the actual database before it reaches the model.

SQLite internal tables and La Roca's hidden bookkeeping tables stay outside the
catalog. No plugin table may declare a column named `database`; that name is
reserved for row provenance.

### Verbs and capabilities

A verb is the public name. A capability is the executable call behind it. The
engine derives both public names from that one record, so a verb cannot reach
the two surfaces as two different contracts. The current build registers that
CLI command from the record for bundled manifest plugins. A third-party verb is
validated and reserved, and until its own registration step lands it reaches the
CLI only when a `roca-<verb>` executable sits on `PATH`; the MCP server likewise
still declares its own tool list. A capability's `command` is prepended to the
arguments the caller supplies and executed through the declared `binary`.

SQL remains the preferred path for reads. Capabilities are for work SQL cannot
perform, such as importing a source, contacting a device, or producing a
derived artifact. Executable capabilities run with the user's permissions and
are not a sandbox.

## Build your own plugin

1. Create the SQLite files and executable named by the manifest.
2. Write `plugin.json`, including a semantic entry for every database.
3. Publish a `checksums.txt` containing one SHA-256 for every immutable payload
   and every initial database file.
4. Test that each declared table and ordered column list matches the database.
5. Install from a local directory while developing, then publish the same
   directory in a Git repository.

A source package for the example above contains:

```text
plugin.json
receipts.db
receipts-runs.db
roca-receipts
checksums.txt
```

Its checksum file is:

```text
<sha256>  plugin.json
<sha256>  receipts.db
<sha256>  receipts-runs.db
<sha256>  roca-receipts
```

Enable third-party plugin lifecycle commands, then install the directory:

```toml
[features]
plugins = true
```

```sh
roca plugin install ./receipts-plugin
```

The generated `.roca-plugin.json` is local installation inventory. Plugin
authors write `plugin.json`; they must not write or distribute the local
inventory file. [Verified packages and lifecycle](#verified-packages-and-lifecycle)
below is what the installer verifies, asks, and preserves.

## Executable-only packages

A package that ships a command instead of data owns no database and needs no
semantic fragment. Its `plugin.json` declares identity and the kind:

```json
{
  "schema": 1,
  "name": "receipts",
  "version": "1.0.0",
  "kind": "executable"
}
```

Its `checksums.txt` lists exactly `plugin.json` and the `roca-<name>`
executable. The package may declare one `state_directory`, a safe
single-component name for state the command derives and rewrites. The installer
creates it after verification, preserves it across updates, refuses a rename,
and records the namespace in the installed manifest, so uninstall and purge own
its contents. It is derived rather than published, so it carries no checksum;
a package whose derived state cannot be regenerated sets `custody: true`
alongside the kind.

Such a package is always classified **EXECUTABLE**. It never enters data-plugin
discovery, attachment, or the semantic catalog: it is reached only by running
its command.

## Verified packages and lifecycle

An installable source is a local directory, a Git URL, or `owner/repo`, which is
cloned from GitHub with the user's existing Git credentials, including for a
private repository.

`checksums.txt` publishes one SHA-256 for every payload file: `plugin.json`,
each declared database, the optional `rides.toml`, and the optional
`roca-<name>` executable. The installer rejects missing, extra, changed,
symlinked, or non-regular payloads before it writes anything, and it installs
each payload from the same open file it verifies, so a source swapped for a
symlink or another file between the consent screen and the copy is refused
rather than installed. The displayed package checksum is the deterministic
SHA-256 fingerprint of those verified source checksums.

```sh
roca plugin install <path|url|owner/repo>
```

The consent screen always names the source, version, checksum, and one of two
risk levels:

- **DATA-ONLY** has databases and their semantic fragments, no executable, and
  no ride manifest. It is near-harmless; its worst case is lying content
  entering model context.
- **EXECUTABLE** is full trust. It runs code with the user's privileges, either
  from its `roca-<name>` executable or from the ride commands the [cron
  train](#scheduled-rides) hands to a shell.

Install, update, and uninstall all show that screen and wait for an answer.
`--yes` accepts that risk without prompting; `--json` never prompts and refuses
the operation until `--yes` states the decision, so no script consents by
accident. An update also names the checksum it replaces, because a source
takeover and an ordinary version bump otherwise look the same.

The package directory is installed under `~/.roca/plugins/`. An executable goes
to `$ROCA_PREFIX`, or `~/.local/bin` when that variable is absent. The generated
`.roca-plugin.json` records source, version, package checksum, payload
checksums, and installed paths.

`roca plugin update <name>` re-resolves and verifies that recorded source. It
refreshes the immutable package files but preserves every declared database and
any declared state directory, because those are the plugin's writable,
user-owned state. A change to the database file list, the state directory name,
or the package kind is refused instead of guessing at a migration.

`roca plugin uninstall <name>` removes an ordinary verified installation. When a
declaration carries custody, it never deletes the folder: it atomically moves
the complete directory to `~/.roca/plugin-custody/<name>-<UTC timestamp>` and
reports that path. A lifecycle operation also refuses to overwrite or delete an
installed executable whose checksum changed outside the installer.

A plugin bundled with the binary asks for no consent and resolves no source:
[installation and update](lifecycle.md#install) place it from the release
artefact itself, verify the same checksums, and write the same manifest. Because
nothing but its packaged files changes between versions, it is refreshed inside
the directory it already occupies, so the database it owns is never unlinked
from a process that holds it open.

Removing La Roca itself removes the installed packages and asks separately
before it touches those archives: see [Uninstall](lifecycle.md#uninstall).

## Stable integration surfaces

Plugins should treat the `roca` process as the public API:

- use `roca query`, `roca sql`, and `roca exec` for gated reads;
- use `roca store --origin plugin:<name>` for attributed operational writes;
- use `--json` for machine-shaped CLI output;
- use `roca mcp serve` when MCP is the natural transport.

Every query and explicit SQL answer declares consulted databases, omitted
databases, warnings, and row provenance. Direct writes to another plugin's
database are outside the contract.

## The bundled roca-corpus plugin

`roca-corpus` is the resident first consumer of the manifest engine. It owns
the perennial archive: sessions, exchanges, reasoning blocks, tool uses, and
files harvested as memories. Installation places its manifest and database
under `~/.roca/plugins/roca-corpus/`; no feature flag is required.

Its manifest declares the `ingest` verb, the `plugin_roca_corpus` attach alias,
the semantic fragment used by NL-to-SQL, custody, and plugin-owned archive
retention. Existing ingest and query behavior is unchanged by the migration.

## The bundled roca-ops plugin

`roca-ops` owns operational agent writes and their query surface. It has a
separate custodial database so its retention policy can differ from the corpus.
In the current staged rollout it remains behind:

```toml
[features]
roca_ops = true
```

Its manifest migration follows corpus. Until that step lands, its legacy
descriptor preserves the existing `store`, `query`, `sql`, and `exec`
contracts without turning the compatibility database into kernel ownership.

## Scheduled rides

`roca cron` is a lightweight train: an external observer that invokes work
already owned by the kernel or a plugin. It does not ingest, embed, or keep a
daemon alive. System cron calls `roca cron run`; omitting the train selects
`nightly`. The command is default-off:

```toml
[features]
cron = true
```

With `features.cron` absent or false, the command is not registered and behaves
as though it does not exist. The kernel registers its own `roca ingest` command
as the first nightly ride, so direct ingest remains available unchanged, and the
plugin name `core` is reserved for that built-in ride namespace.

The train is not behind `features.plugins`: it reads ride manifests from
installed plugin payloads either way. Only a checksum-verified installation
whose recorded consent is **EXECUTABLE** may contribute rides, because a
declared ride command is an execution surface and never data-only. An unmanaged
directory, a changed payload, or an installation whose manifest and checksums no
longer agree contributes nothing. That check re-reads every declared payload
except the plugin's own writable database.

### Declaring rides

A plugin opts in with `rides.toml`. Ride, train, and gate names are identifier
style; use underscores rather than hyphens. `train` defaults to `nightly`:

```toml
[ride.import_delta]
command = "roca-receipts import --delta"
gate = "after_ingest"
```

`roca cron list` aggregates the built-in and installed plugin manifests in
stable plugin/ride order. `roca cron run [train] --dry-run` prints that order
and each gate's current status without invoking or recording anything.

A gate named `after_<ride>` opens only when that dependency's latest recorded
journey ended with exit code zero. The dependency is the ride of that name
declared by the same plugin, so two plugins may name a ride alike without
deciding each other's gates. `after_ingest` is the sole cross-plugin exception:
a plugin that declares no `ingest` ride of its own reads the built-in ingest
journey. Any other gate whose dependency is absent from that plugin is reported
as an unusable manifest rather than deferred forever. The train does not reorder
rides into a dependency graph, and one ride it cannot observe or record is
reported against that ride while the rides behind it still take their trip.

### What the observer guarantees

Before each invocation the train probes the existing `logs/.roca.lock` flock and
releases the probe immediately. It never keeps or creates that lock. That flock
guards the log directory rather than a whole ingest, so an occupied probe means
a record is being written or a purge holds the tree, not that some long command
is halfway through: it is a courtesy check rather than mutual exclusion, which
is why rides must stay idempotent. An occupied lock or a closed dependency
defers the ride to the next train instead of waiting in a daemon. Invoked
commands keep their standard behavior while the train observes exit code,
duration, streams, and timestamps from outside. There is deliberately no
per-ride timeout: that policy belongs to each ride or its external scheduler,
not to the observer.

Every attempted or deferred trip is stored in the custodial `roca-cron` database
at `~/.roca/plugins/roca-cron/roca-cron.db`. Its `journeys` table is the
canonical cross-plugin signal and records train, ride, plugin, timestamps,
duration, exit code, error, gate status, stdout, and stderr. Both streams are
kept as a redacted excerpt of at most 64 KiB, with the dropped byte count noted
in place, so a talkative ride can neither grow the database without bound nor
leave a credential in a queryable column. Dry-runs write no journey, and
`ROCA_READ_ONLY=1` refuses a train run because recording one is a write, while
leaving `roca cron list` and `--dry-run` available. Journey history is kept
whole: unlike the operational log, it is neither rotated nor pruned. See
[Operations](operations.md) for logs, redaction, and retention.

### Calling it from system cron

The train expects an ordinary crontab entry. The built-in ride addresses the
running binary by its absolute path, so it survives cron's minimal environment;
a plugin ride command is resolved by the shell, so give it an absolute path or
declare `PATH` in the crontab:

```crontab
PATH=/usr/bin:/bin:/home/you/.local/bin
17 3 * * * /home/you/.local/bin/roca cron run nightly
```

`roca-cron` owns its journey database outside corpus and ops so its retention
policy stays its own. The manifest schema can already describe that database;
migrating cron to it is a later federation step.
