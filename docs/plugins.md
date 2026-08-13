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

With `features.plugins` absent or false, La Roca does not inspect the plugins
directory, route to a semantic layer, attach a plugin database, or resolve an
installer source. Existing Git-style executable dispatch predates this standard
and continues to behave as before.

A data plugin is one directory under `~/.roca/plugins/<name>/`. It contains
exactly one plain SQLite database (`.db`, `.sqlite`, or `.sqlite3`) and a
`semantic.yaml` file. The database is the plugin's only writable store; La
Roca opens it read-only only when its semantic layer is relevant to a question.
SQLite extensions, including `sqlite-vec`, are not part of this contract.

## Semantic layer

The version 1 document describes what the database actually contains and the
questions it can answer:

```yaml
version: 1
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

`description`, at least one question, and every table's description and ordered
column list are required. A plugin that holds user data moved out of core also
declares `custody: true`; lifecycle tooling must treat that data as protected.
No table may declare a column named `database`: that name is reserved for the
row provenance every answer carries.

At query time La Roca ranks installed semantic layers against the question and
validates each selected declaration against the database's real tables and
columns. A mismatch skips that plugin and travels as a warning. Valid tables
are shown to the SQL model with a qualified schema such as
`plugin_receipts.receipts`. Punctuation in a plugin name becomes `_`; the rare
collision receives a deterministic suffix.

The same read-only gate validates core and qualified plugin SQL. Hidden table
names and forbidden functions stay forbidden in every attached schema. Plugin
databases are attached with SQLite's read-only URI mode only for the execution,
then detached. The execution timeout still applies. When more relevant plugins
exist than SQLite can attach, La Roca uses the ten highest-ranked ones and
declares the omitted databases in the answer.

Every query and explicit `roca exec` answer declares its consulted databases.
Rows returned while plugins are in scope carry a `database` value such as
`core` or `plugin:receipts`; cross-database rows use a `+`-joined label. This
provenance also reaches MCP's TOON output.

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

A plugin that intentionally writes a core memory uses `roca store` or MCP with
`--origin plugin:<name>`. Direct writes to `roca.db` are outside the plugin
contract. Executables run with the user's permissions and are not a sandbox.

## Verified packages and lifecycle

An installable source is a local directory or the root of a Git repository. A
URL is treated as a Git URL; `owner/repo` is cloned from GitHub using the user's
existing Git credentials, including for private repositories. The source adds
a `plugin.json` file:

```json
{
  "schema": 1,
  "name": "receipts",
  "version": "1.2.3"
}
```

A `checksums.txt` beside it publishes one SHA-256 for each payload file:
`plugin.json`, `semantic.yaml`, the one SQLite database, and the optional
`roca-<name>` executable. The installer rejects missing, extra, changed,
symlinked, or non-regular payloads before it writes anything. Its displayed
package checksum is the deterministic SHA-256 fingerprint of those verified
source checksums.

```text
<sha256>  plugin.json
<sha256>  semantic.yaml
<sha256>  receipts.sqlite
<sha256>  roca-receipts
```

Run `roca plugin install <path|url|owner/repo>`. The consent screen always names
the source, version, checksum, and one of two risk levels:

- **DATA-ONLY** has a database and semantic layer but no executable. It is
  near-harmless; its worst case is lying content entering model context.
- **EXECUTABLE** is full trust. It runs code with the user's privileges.

The plugin folder is installed under `~/.roca/plugins/`. An executable goes to
`$ROCA_PREFIX`, or `~/.local/bin` when that variable is absent. The generated
`.roca-plugin.json` records source, version, package checksum, payload checksums,
and installed paths.

`roca plugin update <name>` re-resolves and verifies that recorded source. It
refreshes immutable package files but preserves the installed SQLite database,
because that file is the plugin's writable, user-owned state. A database filename
change is refused instead of guessing at a migration.

`roca plugin uninstall <name>` removes an ordinary verified installation. When
`custody: true`, it never deletes the folder: it atomically moves the complete
directory to `~/.roca/plugin-custody/<name>-<UTC timestamp>` and reports that
path. A lifecycle operation also refuses to overwrite or delete an installed
executable whose checksum changed outside the installer.
