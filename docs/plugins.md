# Plugins

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

The plugin standard is experimental and defaults to off in this release. Enable
data discovery, attach-based querying, and `roca plugin` lifecycle commands in
`~/.roca/config.toml`:

```toml
[features]
plugins = true
```

With `features.plugins` absent or false, La Roca does not route to a semantic
layer, attach a plugin database, or resolve an installer source; the only
reader of the plugins directory left is the [cron train](#cron-rides). Existing
Git-style executable dispatch predates this standard and continues to behave as
before.

There are two package kinds. A data plugin is one directory under
`~/.roca/plugins/<name>/`. It contains
exactly one plain SQLite database (`.db`, `.sqlite`, or `.sqlite3`) and a
`semantic.yaml` file, and may also declare scheduled rides in `rides.toml`.
The database is the plugin's only writable store; La Roca opens it read-only,
either when its semantic layer is relevant to a question or, for a resident
plugin, for every query.
SQLite extensions, including `sqlite-vec`, are not part of this contract.

An executable-only plugin contains a `roca-<name>` binary instead of a semantic
layer and database. It may declare one writable `state_directory`; the installer
creates that directory, preserves it across package updates, and records the
namespace in the installed manifest so uninstall and purge own its contents.
Executable-only packages never enter data-plugin discovery or attachment.

## Semantic layer

The version 1 document describes what the database actually contains and the
questions it can answer:

```yaml
version: 1
attachment: on-demand
description: Purchase receipts and their totals.
questions:
  - Which receipts were recorded?
tables:
  - name: receipts
    description: One row per purchase receipt.
    questions:
      - How much did a purchase cost?
    columns: [id, title, amount_cents]
```

`attachment` is `on-demand` or `resident`; omitting it keeps the version 1
default, `on-demand`. An on-demand database is attached only when its semantic
layer matches the question or an explicit SQL statement names it. A resident
database is attached whenever a read connection is acquired and stays available
for that connection's query. Resident plugins still pass the same schema
validation, read-only URI, gate, timeout, provenance, and ten-database attachment
cap.

`description`, at least one question, and every table's description and ordered
column list are required. A plugin that holds user data moved out of core also
declares `custody: true`; lifecycle tooling must treat that data as protected.
No table may declare a column named `database`: that name is reserved for the
row provenance every answer carries.

At query time La Roca ranks installed semantic layers against the question and
validates each selected declaration against the database's real tables and
columns. SQLite's own internal tables are outside that comparison, so a
database that uses `AUTOINCREMENT` does not declare `sqlite_sequence`. A
mismatch skips that plugin and travels as a warning. Valid tables
are shown to the SQL model with a qualified schema such as
`plugin_receipts.receipts`. Punctuation in a plugin name becomes `_`; the rare
collision receives a deterministic suffix.

The same read-only gate validates core and qualified plugin SQL. Hidden table
names and forbidden functions stay forbidden in every attached schema. Plugin
databases are attached with SQLite's read-only URI mode. On-demand and resident
databases remain attached throughout execution and are detached before the read
connection is returned. The execution timeout still applies. When more eligible
plugins exist than SQLite can attach, La Roca uses at most ten and declares the
omitted databases in the answer.

Every query and explicit `roca exec` answer declares its consulted databases.
Rows returned while plugins are in scope carry a `database` value such as
`core` or `plugin:receipts`; cross-database rows use a `+`-joined label. This
provenance also reaches MCP's TOON output.

## Cron rides

`roca cron` is the lightweight train: an external observer that invokes work
already owned by core or a plugin. It does not ingest, embed, or keep a daemon
alive. System cron can call `roca cron run`; omitting the train selects
`nightly`. Core registers its existing direct `roca ingest` command as the
first nightly ride, so direct ingest remains available unchanged. The train
itself is not behind `features.plugins`: it reads verified ride manifests from
installed plugin payloads and records journeys whether or not the plugin
standard is enabled. An unmanaged directory, a changed payload, an installation
whose manifest and checksums no longer agree, or one whose recorded consent is
[data-only](#verified-packages-and-lifecycle) contributes no rides. That check
re-reads every declared payload except the plugin's own writable database, and
a directory named `core` contributes nothing either: that ride namespace is
reserved for the built-in rides.

A plugin opts in with `rides.toml`. Ride, train, and gate names are identifier
style; use underscores rather than hyphens. `train` defaults to `nightly`:

```toml
[ride.delta_ingest]
command = "roca vector ingest --delta"
gate = "after_ingest"
```

`roca cron list` aggregates core and every installed plugin manifest in stable
plugin/ride order. `roca cron run [train] --dry-run` prints that order and each
gate's current status without invoking or recording anything. A gate named
`after_<ride>` opens only when that dependency's latest recorded journey ended
with exit code zero. The dependency is the ride of that name declared by the
same plugin, so two plugins may name a ride alike without deciding each other's
gates. `after_ingest` is the sole cross-plugin exception: when the plugin does
not declare its own `ingest` ride, it reads the core ingest journey. Any other
gate whose dependency is absent from that plugin is reported as an unusable
manifest rather than deferred forever. The train does not reorder rides into a
dependency graph, and one ride it cannot observe or record is
reported against that ride while the rides behind it still take their trip.

Before each invocation the train probes core's existing `logs/.roca.lock`
flock and releases the probe immediately. It never keeps or creates that lock.
That flock guards core's log directory rather than a whole ingest, so an
occupied probe means core is writing a record or a purge holds the tree, not
that some long command is halfway through: it is a courtesy check rather than
mutual exclusion, which is why rides must stay idempotent. An occupied lock or
closed dependency defers the ride to the next train instead of waiting in a
daemon. Invoked commands keep their standard behavior while the train observes
exit code, duration, streams, and timestamps from outside. This version
intentionally imposes no per-ride timeout: timeout policy belongs to each ride
or its external scheduler, not to the observer.

Every attempted or deferred trip is stored in the bundled custodial
`roca-cron` plugin database at
`~/.roca/plugins/roca-cron/roca-cron.db`. Its `journeys` table is the canonical
cross-plugin signal and includes train, ride, plugin, timestamps, duration,
exit code, error, gate status, stdout, and stderr. Both streams are kept as a
redacted excerpt of at most 64 KiB, with the dropped byte count noted in place,
so a talkative ride can neither grow the database without bound nor leave a
credential in a queryable column. Dry-runs write no journey, and
`ROCA_READ_ONLY=1` refuses a train run because recording one is a write, while
leaving `roca cron list` and `--dry-run` available. Journey history is kept
whole in this version: unlike the operational log, it is neither rotated nor
pruned.

The train expects an ordinary crontab entry. Core's own ride addresses the
running binary by its absolute path, so it survives cron's minimal environment;
a plugin ride command is resolved by the shell, so give it an absolute path or
declare `PATH` in the crontab:

```crontab
PATH=/usr/bin:/bin:/home/you/.local/bin
17 3 * * * /home/you/.local/bin/roca cron run nightly
```

## Building against the stable surfaces

A plugin should treat the `roca` process as its API and compose these public
surfaces:

- CLI commands with `--json` when it needs machine-shaped output.
- `roca query`, `roca exec`, and `roca sql` for reads; explicit SQL still goes
  through La Roca's read-only gate. Query inherits the detected-agent-CLI
  factory default, so a plugin does not introduce a separate login step.
- `roca store` for writes. Use the documented layers and pass
  `--origin plugin:<name>` so the plugin's records remain attributable and can
  be selected or purged by origin. Plugin names may contain letters, digits,
  hyphens, underscores, and dots, and may not begin with a dot. Pass `--agent`
  and `--model` as well, or the write is stored as an unknown author: see
  [Memory authorship](operations.md#memory-authorship).
- `roca mcp serve` when an MCP client is the more natural integration surface.

Every answer these surfaces return declares the databases it consulted in
`databases`, the relevant ones it could not attach in `omitted_databases`, and
each degraded semantic layer in `warnings`. Each row carries its source in
`database`. That envelope is the contract a plugin author may rely on.

## Commands and core-memory writes

An optional `roca-<name>` executable on `PATH` remains the command surface.
When `roca <name>` is not built in, La Roca hands that executable the remaining
arguments, standard streams, and exit status unchanged. Built-ins win. The
current directory is never searched. `roca plugins` lists these executables;
data-plugin discovery does not change dispatch.

A plugin that intentionally writes a memory uses `roca store` or MCP with
`--origin plugin:<name>`. That write lands in core, or in the operational store
when [`features.roca_ops`](#the-bundled-roca-ops-plugin) routes it there. Direct
writes to `roca.db` are outside the plugin contract. Executables run with the
user's permissions and are not a sandbox.

## Verified packages and lifecycle

An installable source is a local directory or the root of a Git repository. A
URL is treated as a Git URL; `owner/repo` is cloned from GitHub using the user's
existing Git credentials, including for private repositories. The source adds
a `plugin.json` file:

```json
{
  "schema": 1,
  "name": "receipts",
  "version": "1.2.3",
  "kind": "data"
}
```

`kind` defaults to `data` for compatibility. An executable-only package uses
`"kind": "executable"` and may add a safe, single-component
`state_directory`. Data-package custody remains in `semantic.yaml`; an
executable-only package may set `custody: true` in `plugin.json` when its state
cannot be regenerated.

A `checksums.txt` beside it publishes one SHA-256 for each payload file:
`plugin.json`, `semantic.yaml`, the one SQLite database, optional `rides.toml`,
and the optional `roca-<name>` executable. For an executable-only package it
lists exactly `plugin.json` and `roca-<name>`; the writable state directory is
created only after verification and is not a published payload. The installer
rejects missing, extra, changed, symlinked, or non-regular payloads before it
writes anything, and it installs each payload from the same open file it
verifies, so a source swapped for a symlink or another file between the consent
screen and the copy is refused rather than installed. Its displayed package
checksum is the deterministic SHA-256 fingerprint of those verified source
checksums.

```text
<sha256>  plugin.json
<sha256>  semantic.yaml
<sha256>  receipts.sqlite
<sha256>  rides.toml
<sha256>  roca-receipts
```

Run `roca plugin install <path|url|owner/repo>`. The consent screen always names
the source, version, checksum, and one of two risk levels:

- **DATA-ONLY** has a database and semantic layer, no executable, and no ride
  manifest. It is near-harmless; its worst case is lying content entering model
  context.
- **EXECUTABLE** is full trust. It runs code with the user's privileges, either
  from its `roca-<name>` executable or from the ride commands the [cron
  train](#cron-rides) hands to a shell. The train runs a plugin's rides only
  while its manifest records that consent.

Install, update, and uninstall all show that screen and wait for an answer.
`--yes` accepts that risk without prompting; `--json` never prompts and
refuses the operation until `--yes` states the decision, so no script consents
by accident. An update also names the checksum it replaces, because a source
takeover and an ordinary version bump otherwise look the same.

The plugin folder is installed under `~/.roca/plugins/`. An executable goes to
`$ROCA_PREFIX`, or `~/.local/bin` when that variable is absent. The generated
`.roca-plugin.json` records source, version, package checksum, payload checksums,
and installed paths.

A plugin bundled with the binary asks for no consent and resolves no source:
[installation and update](lifecycle.md#install) place it from the release
artefact itself, verify the same checksums, and write the same manifest. Because
nothing but its packaged files changes between versions, it is refreshed inside
the directory it already occupies, so the database it owns is never unlinked
from a process that holds it open.

`roca plugin update <name>` re-resolves and verifies that recorded source. It
refreshes immutable package files but preserves the installed SQLite database,
because that file is the plugin's writable, user-owned state. A database filename
change is refused instead of guessing at a migration. For an executable-only
package it likewise preserves the manifest-declared state directory and refuses
a directory-name change.

`roca plugin uninstall <name>` removes an ordinary verified installation. When
`custody: true`, it never deletes the folder: it atomically moves the complete
directory to `~/.roca/plugin-custody/<name>-<UTC timestamp>` and reports that
path. A lifecycle operation also refuses to overwrite or delete an installed
executable whose checksum changed outside the installer.

Removing La Roca itself removes the installed packages and asks separately
before it touches those archives: see
[Uninstall](lifecycle.md#uninstall).

## Worked executable example: vector search

Vector search is deliberately an installable executable package, not a bundled
feature and not a data plugin. The core binary has no vector command, model,
index, or uninstall inventory. With the package absent, nothing vector-specific
is discovered or activated. `features.plugins` gates the verified install
lifecycle; generic executable dispatch works only after an operator has placed
the package's binary on `PATH` through that explicit lifecycle.

Core accepts `features.vector` as a default-off rollout marker, in the same
structural shape as `features.corpus`:

```toml
[features]
vector = false
```

The marker adds no vector path to core and does not override generic executable
dispatch. In this isolated phase, explicit package installation and its
**EXECUTABLE** consent are the activation boundary.

The source lives in `plugins/vector/` as its own Go module, and its
[module README](../plugins/vector/README.md) covers the binary's complete
command, storage, and quality-test contract. Build a verified package for the
current machine, then install it through the ordinary full-trust consent path:

```sh
make -C plugins/vector package
roca plugin install .tmp/vector-package
```

The installed `roca-vector` binary makes `roca vector` available through
executable dispatch. It reads corpus pages through the public `roca exec --json`
boundary and owns its sqlite-vec index, completion record, worker log, and locks
under `~/.roca/plugins/vector/state/`. Core neither imports the implementation
nor opens that index.

```sh
roca vector install
roca vector ingest --delta
roca vector query "the decision about local model routing" 10
```

The package ships OFF: release and ordinary installation do not place it. An
operator must first enable the experimental plugin lifecycle, explicitly
install the executable package, and accept its **EXECUTABLE** risk prompt.

## The bundled roca-corpus plugin

`roca-corpus` is the resident, data-only custody package for the perennial
harvest: sessions, exchanges, reasoning blocks, tool uses, and files harvested
as memories. Its memory schema admits only `harvest-corpus` and `harvest-file`
as first-class provenance values instead of encoding that boundary as layers.
Operational and curated `agent` and `promoted` records remain in core during
this split.

Every [installation and update](lifecycle.md#install) places the empty package
under `~/.roca/plugins/roca-corpus/`. This structural phase does not attach it,
route ingest into it, or move existing data. The rollout boundary is accepted
in configuration and remains off by default:

```toml
[features]
corpus = false
```

With that default, every CLI and MCP behavior remains on the existing core and
operational paths byte for byte. Activation and custody migration belong to the
separate follow-up phase.

## The bundled roca-ops plugin

`roca-ops` is the operational plugin La Roca ships with itself: a resident,
data-only package that declares `custody: true` over what agents write. Every
[installation and update](lifecycle.md#install) places it under
`~/.roca/plugins/roca-ops/`, and it stays inert until a second experimental
switch is set:

```toml
[features]
roca_ops = true
```

With `features.roca_ops` absent or false, `roca store`, `roca query`, `roca
exec`, and MCP `roca_store` behave exactly as they did before it existed and
every write lands in core. With it true, La Roca keeps those external contracts,
answer envelopes included, and routes each new write to the operational database
instead, carrying the same [authorship
stamp](operations.md#memory-authorship) core records. Core keeps the history it
already holds and is read from without being written to.

Reads are the union of the two halves. Resident attachment puts
`plugin_roca_ops.memories` in front of the SQL model on every query, and the
deterministic keyword rescue asks both databases, orders the merged rows by
recency before it applies the shared limit, and declares the statement for both
halves rather than the core one alone. Each row still names its origin in
`database`. A half that cannot be read degrades to a warning instead of
discarding the half that answered.

A memory supersedes only what its own database holds. With the switch on, a
write that names a core memory in `--supersedes` is refused: the exclusion is
computed inside the database that stores the replacement, so the retirement
would be reported without happening.

Nothing expires by itself and there is no default lifetime. A write may declare
an RFC3339 `expires_at` in its `--metadata`, and only an explicit drain removes
the rows whose declared expiry is due:

```sh
roca ops drain                                 # what has already expired
roca ops drain --before 2026-01-01T00:00:00Z   # what had expired by that instant
```

A row with no `expires_at` is never drained, and `ROCA_READ_ONLY` refuses the
drain like any other write.
