# Plugins

A plugin is a small package that gives La Roca new data to answer questions
from. It owns one or more SQLite databases, describes them in a `plugin.json`
manifest, and La Roca attaches those databases read-only so their tables can
appear in query answers. A plugin may also ship an executable that adds
commands, but it does not have to: a plugin with only data is complete.

You can build your first one in about five minutes with no Go code at all.
[Your first plugin](#your-first-plugin) below is the whole walk: three files,
one install command, one query. Everything after that is the reference: the
full manifest schema, how installs are verified and preserved, and scheduled
rides.

One thing to know before you start: the third-party plugin surface is
**experimental** and gated by `features.plugins`. A fresh `roca init` enables
it; older or operator-managed configurations may leave it disabled. Install
and update commands exist only while the switch is true, and the surface may
still change between releases.

## Your first plugin

This section is complete: copy each block in order and you end with an
installed, working plugin. You need a terminal, La Roca itself
(`roca init` if you have never used it), and the `sqlite3` command line tool,
which ships with macOS and most Linux distributions.

The example plugin is called `first-receipts` and serves one table of purchase
receipts. It ships data only, no executable, so it is classified DATA-ONLY
(near-harmless) at install time.

### 1. Confirm plugins are on

Fresh init already writes this switch. If you use an older or operator-managed
configuration, open the file —
`~/.roca/config.toml` by default, or `config.toml` next to the database when
you use `--db-path` — and make sure it contains:

```toml
[features]
plugins = true
```

### 2. Create three files

Make a folder for the plugin. The folder's name does not matter; the `name`
inside `plugin.json` decides where it installs.

```sh
mkdir first-receipts
cd first-receipts
```

**File 1: the database.** A plugin database is an ordinary SQLite file you
create however you like. Here `sqlite3` builds one with a single table and one
row, so there is something to find:

```sh
sqlite3 receipts.db <<'SQL'
CREATE TABLE receipts (id INTEGER PRIMARY KEY, title TEXT, amount_cents INTEGER);
INSERT INTO receipts (title, amount_cents) VALUES ('coffee', 420);
SQL
```

**File 2: the manifest.** Save this as `plugin.json`:

```json
{
  "schema": 1,
  "name": "first-receipts",
  "version": "1.0.0",
  "binary": "roca",
  "databases": [
    {
      "name": "records",
      "path": "receipts.db",
      "alias": "first_receipts",
      "attachment": "on-demand",
      "retention": "Keep receipts until the operator removes them."
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
            "columns": ["id", "title", "amount_cents"]
          }
        ]
      }
    ]
  }
}
```

What each part says:

- `name` is the plugin's identity. It becomes the install directory
  `~/.roca/plugins/first-receipts`, so it is restricted to letters, digits,
  `-`, and `_`.
- `binary` is `"roca"` because this plugin ships no executable of its own: it
  adds data, not code, which is what keeps it DATA-ONLY.
- `databases` lists every SQLite file in the package. `path` is the file name;
  `alias` is the schema name SQL uses (`first_receipts.receipts`); `attachment`
  `"on-demand"` means the database is attached only when a question or explicit
  SQL asks for it; `retention` is a required, plain-language description of
  your pruning policy — you own it and you enforce it.
- `semantic` describes the data in words. The description and questions are
  what lets the model pick these tables for the right questions, and the
  ordered `columns` list must match the real table exactly or the plugin is
  skipped at query time.

**File 3: the checksums.** The installer verifies every payload before it
copies anything, using `checksums.txt` — one SHA-256 per line. Generate it
after the other two files are final (on Linux, `sha256sum` writes the same
format):

```sh
shasum -a 256 plugin.json receipts.db > checksums.txt
```

Your folder now holds exactly three files:

```text
first-receipts/
├── plugin.json
├── receipts.db
└── checksums.txt
```

### 3. Install it

From the folder's parent directory:

```sh
roca plugin install ./first-receipts
```

The installer verifies the checksums, shows a consent screen naming the
source, version, package checksum, and risk level, and waits for `y`:

```text
Plugin install consent
source: /path/to/first-receipts
version: 1.0.0
checksum: sha256:…
risk: DATA-ONLY: near-harmless; its worst case is lying content returned from its database.
Proceed with plugin install? [y/N]
```

Answer `y`, and the plugin installs under `~/.roca/plugins/first-receipts`.
Plugins are installed for you as the operator, not per database: they land
under `~/.roca/plugins/` even when your database lives elsewhere through
`--db-path`.

### 4. Prove it answers

The guaranteed check is direct SQL through the read-only gate, naming the
alias you declared:

```sh
roca exec 'SELECT title, amount_cents FROM first_receipts.receipts'
```

You should see something like (the exact `databases:` line depends on what
else is installed):

```text
SELECT title, amount_cents FROM first_receipts.receipts LIMIT 1000
databases: core, plugin:roca-corpus, plugin:first-receipts
rows[1]{title,amount_cents,database}:
  coffee,420,"plugin:first-receipts"
```

The model-backed natural-language SQL surface works too. Select this on-demand
database explicitly; its semantic fragment then gives the model the declared
tables and columns:

```sh
roca playground --databases first-receipts "which receipts were recorded"
```

Every answer says which databases it consulted, and every row carries its
origin — here `plugin:first-receipts`, which is how you know the rows came
from your plugin and not from core memory.

### 5. Remove it when done

```sh
roca plugin uninstall first-receipts
```

Because this example declares no `custody`, uninstall deletes the folder. A
database holding operator data declares `custody: true` instead, and
uninstall then archives the directory rather than deleting it — see
[Database declarations](#database-declarations).

### If something goes wrong

- `the experimental plugin system is disabled; set features.plugins = true in
  <path>` — the configuration checked in step 1 does not enable it. The error
  names the exact file to edit.
- `checksum mismatch for …` — you changed a file after generating
  `checksums.txt`. Regenerate it (step 2, file 3) and install again.
- ``plugin first-receipts is already installed; run `roca plugin update
  first-receipts``` — one install per name. Uninstall first, or update.
- Your table does not appear and the `databases:` line omits the plugin — the
  semantic declaration disagrees with the real database (a wrong or reordered
  column list is the usual cause). The plugin is skipped with a warning.
  Fix `plugin.json`, regenerate the checksums, and run
  `roca plugin update first-receipts`.
- `plugin.json` with a misspelled key is rejected outright: the engine refuses
  unknown fields rather than guessing what you meant.

## How a plugin works

La Roca is a federating kernel. A plugin owns its durable databases and
describes them in its manifest; the kernel validates those declarations,
attaches the databases read-only, and composes their table descriptions into
the catalog used by natural-language queries.

The kernel does not own a domain database. Its own durable state is
configuration and installed manifests. Database retention, pruning,
compaction, and scale are plugin policy, and one plugin cannot prune another
plugin's history.

The kernel's own attach point is an empty in-memory SQLite database. During
the reversible cutover, temporary compatibility views reproduce the former
core tables from plugin custody memberships, and temporary FTS indexes
preserve the legacy ranking surface. The single `layout.serving` marker and
its rollback states are documented in the
[runtime map](architecture.md#runtime-map). This adapter is not a plugin API.
A plugin database is opened read-only and reached only through its declared
alias.

`roca-corpus` and `roca-ops`, the engine's manifest-backed bundled consumers,
prove the model without changing the product contract: both are resident,
ingest and operational writes still land in the same data, query results are
unchanged, and readable query output still begins with the same consulted
database list.

## The manifest

`plugin.json` schema 1 has five required parts and one optional retrieval
contract:

- identity: `name`, `version`, and `binary`;
- databases: every SQLite file, its declared attach alias, attachment mode,
  custody, and plugin-owned retention policy;
- semantic fragment: the tables and questions served by each database;
- optional vector fragment: the stable row id, prose columns, and chronology a
  database opts into later semantic indexing;
- verbs: one canonical command name and description, projected to CLI and MCP;
- capabilities: named executable calls used when SQL cannot perform the work.

The canonical verb `inspect`, for example, names CLI command `inspect` and MCP
tool `roca_inspect`. Both resolve to the same capability. A manifest cannot
give the two surfaces different implementations. Registration lands in steps:
this build seats a declared verb as a CLI command for bundled manifest
plugins only.

Here is a complete two-database plugin. The second database demonstrates the
same shape used by a scheduler that keeps run history and errors apart from
its domain records. (The quickstart above is the smallest useful subset of
it.)

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
            "columns": ["id", "title", "amount_cents", "created_at"]
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
  "vector": {
    "databases": [
      {
        "database": "records",
        "tables": [
          {
            "name": "receipts",
            "id_column": "id",
            "text_columns": ["title"],
            "time_columns": ["created_at"],
            "chunking": {"max_chars": 4000, "overlap_chars": 400}
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
aliases, repeated names, a semantic or vector fragment that names an
undeclared database or table, a vector id or text column missing from the
semantic declaration, a table declaration that disagrees with the real SQLite
schema, and a verb that names a missing capability. Discovery reports malformed
manifests as actionable errors; it never silently ignores them. Attach aliases
are explicit, and collisions are errors rather than names the kernel rewrites:
two packages that declare the same alias both lose it, and the warning names
them. The aliases the bundled packages declare are the kernel's own seats, so a
later package that claims one of them makes only itself unavailable.

`name` travels through the install directory, executable, and every lifecycle
argument, so an installable package restricts it to ASCII letters, digits, `-`,
and `_`. The executable is `roca-<name>` unless `name` already starts with
`roca-`, in which case it is exactly `name`. A manifest the engine would read
but the installer could not manage is refused at install time, not after.

`binary` names the executable that runs the package's capabilities. A package
that ships one declares the derived executable described above. The installer
refuses the package when the declared name and the shipped file disagree. A
package that ships no executable declares `roca`, the host binary: its
capabilities are commands of La Roca itself, and it stays **DATA-ONLY** because
it adds no code of its own. There is no third value; `binary` is never empty.

### Database declarations

`path` is one regular file shipped in the package. It must be a safe filename
ending in `.db`, `.sqlite`, or `.sqlite3`, not a path outside the package.
`alias` is the exact SQLite schema used in qualified SQL, such as
`receipts_records.receipts`.

`attachment` is `resident` or `on-demand`. A resident database is available to
every query connection. An on-demand database is selected when its semantic
fragment matches the question, or explicit SQL names its alias. Both modes use
SQLite read-only URI mode, the same SQL gate and timeout, and the same
attachment limit: at most ten plugin databases attached to one query.

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
catalog: declaring one is allowed and omitting it is allowed, and either way it
never reaches the SQL model. No plugin table may declare a column named
`database`; that name is reserved for row provenance.

### Vector fragments

The optional `vector` fragment is opt-in at column granularity. Each entry
names one declared database and table, one stable `id_column`, and the ordered
`text_columns` whose prose is worth recalling by meaning. Columns omitted from
`text_columns` are not embeddable. Keep telemetry, raw tool output, machine
noise, and other deterministic-only fields out unless their meaning is itself
the retrieval surface.

Inside `vector.databases[].tables[]`, the author contract names the table,
stable id column, and opt-in prose columns. Chronology is optional: use
`time_columns` when the row carries its own timestamps, or `time_join` when it
comes from a related declared table. A chronological join names that table, the
local and foreign join columns, and the related table's ordered `time_columns`.
Every named chronological or join column must appear in the relevant semantic
table's ordered `columns` list. When neither form is declared, newest-first
ingest falls back to deterministic descending order on `id_column`, including
for `WITHOUT ROWID` tables.

```json
{"name": "receipts",
 "id_column": "id",
 "text_columns": ["title"],
 "time_columns": ["created_at"]}
```

That is declaration only. The kernel owns embedding generation, fingerprints,
sidecars, and query fan-out; the plugin owns no embedding code. Add `chunking`
only when the default boundaries are unsuitable.

`chunking` is optional. `max_chars` must be positive, `overlap_chars` cannot be
negative, and when both are present the overlap must be smaller than the
maximum. These are hints to the kernel vector worker, not instructions for code
the plugin runs. Absent hints, the worker uses about 250-token windows with
about 100-token overlap and embeds each declared text column separately. A
plugin never supplies embedding generation code, and a
database with no `vector` declaration continues to serve through FTS and SQL
exactly as before.

Install, update, and uninstall regenerate
`~/.roca/plugins/vector-registry.json` in the same pass that refreshes the
semantic catalog. Bundled manifest upgrades refresh the same registry when the
kernel opens them. The registry contains plugin-relative database filenames,
not local home paths, plus a content-free routing inventory so FTS-only
databases remain valid `--databases` selections. It is derived state: edit
`plugin.json`, never the registry. The kernel worker turns each declaration
into a database-owned adjacent sidecar with owner/model/dimensions/version
metadata and incremental fingerprint GC. `roca vector query --databases ...`
routes over those sidecars, merges only same-model scores, and leaves missing
or undeclared vector coverage on the database's existing FTS/SQL path.

### Verbs and capabilities

A verb is the public name. A capability is the executable call behind it. The
engine derives both public names from that one record, so a verb cannot reach
the two surfaces as two different contracts. The MCP tool is always
`roca_<verb>`; the CLI name is the capability's own call, so a verb that rides
an existing command names that command with its flags instead of advertising
one the binary does not have. The current build registers that CLI command from
the record for bundled manifest plugins. A third-party verb is validated and
reserved, and until its own registration step lands it reaches the CLI only
when a `roca-<verb>` executable sits on `PATH`; the MCP server likewise still
declares its own tool list. A capability's `command` is prepended to the
arguments the caller supplies and executed through the declared `binary`.

SQL remains the preferred path for reads. Capabilities are for work SQL cannot
perform, such as importing a source, contacting a device, or producing a
derived artifact. Executable capabilities run with the user's permissions and
are not a sandbox.

## Build your own plugin

1. Create the SQLite files, and the executable when the manifest names one of
   its own rather than the host binary.
2. Write `plugin.json`, including a semantic entry for every database and an
   optional vector entry for prose columns that should be semantically indexed.
3. Publish a `checksums.txt` containing one SHA-256 for every immutable payload
   and every initial database file.
4. Test that each declared table and ordered column list matches the database.
5. Install from a local directory while developing, then publish the same
   directory in a Git repository.

A source package for the two-database example above contains:

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

Its `checksums.txt` lists exactly `plugin.json` and the derived executable
described in [the manifest](#the-manifest). The package may declare one
`state_directory`, a safe single-component name for state the
command derives and rewrites. The installer creates it after verification,
preserves it across updates, refuses a rename, and records the namespace in the
installed manifest, so uninstall and purge own its contents. It is derived
rather than published, so it carries no checksum; a package whose derived state
cannot be regenerated sets `custody: true` alongside the kind.

Such a package is always classified **EXECUTABLE**. It never enters data-plugin
discovery, attachment, or the semantic catalog: it is reached only by running
its command.

## Verified packages and lifecycle

An installable source is a local directory, a local or HTTP(S) `.tar.gz` or
`.tgz` release archive, a Git URL, or `owner/repo`. Git sources are cloned with
the user's existing credentials, including for a private repository. Archives
must contain the package files at their root; nested paths and non-regular
entries are refused before package verification.

`checksums.txt` publishes one SHA-256 for every payload file: `plugin.json`,
each declared database, the optional `rides.toml`, and the optional derived
executable. The installer rejects missing, extra, changed,
symlinked, or non-regular payloads before it writes anything, and it installs
each payload from the same open file it verifies, so a source swapped for a
symlink or another file between the consent screen and the copy is refused
rather than installed. The displayed package checksum is the deterministic
SHA-256 fingerprint of those verified source checksums.

```sh
roca plugin install <path|archive|url|owner/repo>
```

The consent screen always names the source, version, checksum, and one of two
risk levels:

- **DATA-ONLY** has databases and their semantic fragments, no executable, and
  no ride manifest. It is near-harmless; its worst case is lying content
  entering model context.
- **EXECUTABLE** is full trust. It runs code with the user's privileges, either
  from its derived executable or from the ride commands the [cron
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
from a process that holds it open. An update applies the plugin's own schema to
that database before the manifest records the new version, so an interrupted
upgrade is retried by the next run instead of being reported as done; every
declaration it replays is additive and leaves the existing rows in place.

Each bundled database also describes itself. It carries its own schema and index
version, the state of every named custody migration it hosts, the source batches
already committed into it, and the source identity behind each row it holds.
Those `plugin_schema`, `plugin_migrations`, `migration_batches`, and
`custody_memberships` tables are custody bookkeeping rather than fleet memory,
so they stay hidden from every attached schema. A batch is recorded only once it
is fully committed: an interrupted one leaves nothing half-migrated behind and
resumes from where it stopped, and only a verified migration becomes eligible
for cutover. A bundled database may also declare `legacy_*` quarantine tables,
which keep owner-specific records verbatim beside their canonical digest instead
of reshaping them into an active surface. Bumping one of those versions is a
schema change a released database has to adopt, so it owes what
[releases](releases.md#schema-migration-definition-of-done) requires of one.

The DATA SPLIT orphan import has no command, MCP tool, or make target. Only the
internal DATA-6 cutover coordinator in
`internal/distribution/datasplit/cutover.go` runs it, so it remains an internal
stage of the split rather than an operator surface. What follows is what it
does.

The import reads a verified core snapshot in read-only mode. It keeps old
`runs` and `run_logs` as legacy cron payloads; the garden coordination tables,
proposals with their annotations, and the query-plan teaching examples as typed
ops legacy records; and `flow_patterns` in corpus quarantine. Their original
columns, nulls, source keys, and table names stay reproducible through the
payloads and custody memberships; they are not reshaped into `journeys` or
another current surface. Values JSON cannot carry are kept losslessly as hex:
`{"$blob": "…"}` for a stored blob and `{"$text_blob": "…"}` for text whose
bytes are not valid UTF-8, so a byte the source recorded is never dropped or
replaced. The empty withdrawn `messages` table creates no destination object,
and the derived `search_state` is rebuilt by its owner rather than copied. The
import refuses to start at all while the snapshot still holds a table nobody
disposed of, and it refuses before writing anything when a table it would
quarantine still holds a row whose identity columns are NULL or blank, because a
row it cannot address is a row it cannot prove it carried over. Each
checksummed batch is replay-safe, and the plugin databases deliberately remain
in shadow migration state until the whole split is independently verified for
cutover.

Removing La Roca itself removes the installed packages and asks separately
before it touches those archives: see [Uninstall](lifecycle.md#uninstall).

## Stable integration surfaces

Plugins should treat the `roca` process as the public API:

- use `roca query`, `roca playground --sql-only`, and `roca exec` for gated reads;
- use `roca store --origin plugin:<name>` for attributed operational writes;
- use `--json` for machine-shaped CLI output;
- use `roca mcp serve` when MCP is the natural transport.

Every query and explicit SQL answer declares consulted databases, omitted
databases, warnings, and row provenance. Direct writes to another plugin's
database are outside the contract.

## The bundled roca-corpus plugin

`roca-corpus` is a resident manifest plugin. It owns the perennial archive:
sessions, exchanges, reasoning blocks, tool uses, and files harvested as
memories. Installation places its manifest and database
under `~/.roca/plugins/roca-corpus/`; no feature flag is required.

Its manifest declares the `ingest` verb, the `plugin_roca_corpus` attach alias,
the semantic fragment used by NL-to-SQL, custody, and plugin-owned archive
retention. Existing ingest and query behavior is unchanged by the migration.

Its schema also declares the shadow archive the retired core history is copied
into: one digest-only version table per family, and the evidence tying each
version back to the source row it came from. Version rows store the payload
hash, source coordinates, and observed time. They never store a second copy of
`human_text`, `agent_text`, or `full_text`. There is no full-text index over
versions: search uses the current harvest tables. Those tables are migration
machinery rather than fleet memory, so they stay hidden from every query
surface, and the served tables above keep answering exactly as before until the
atomic cutover. Each family is a named custody migration of its own,
`corpus-archive-<family>`, because a migration owns exactly one destination.
The five family migrations retain their table-level archive seal, and cutover
additionally requires the versioned DATA-3 reconciliation seal.

`roca compact` rewrites an existing corpus database onto that one-row law and
VACUUMs. Current session, exchange, thinking, and tool rows stay. Backup copies
belong outside the database.

That reconciliation rereads the same frozen sources and compares every source
database and table by occurrence count and canonical payload hash. It also
compares each source session by record count, exchange count, payload hash, and
an exchange-provenance hash covering model, provider, token counts, and cost.
The archived session payload includes `source_surface`, so OpenCode harness
attribution survives beside its exchange model and provider after cutover.
Missing or duplicate memberships, duplicate physical payload rows, changed
payloads, or changed provenance make the report red; merge and the re-runnable
verifier return an error unless global coverage is exactly 100%.

An exact payload shared by several legacy rows may occupy one immutable version
row, but every occurrence keeps its own custody membership. That preserves the
legacy multiset when a current parser normalizes several assistant fragments
into one turn. Replaying the same frozen snapshots is idempotent and must
reproduce the sealed digest before DATA-3 is cutover-eligible.

## The bundled roca-ops plugin

`roca-ops` owns operational agent writes, durable redacted CLI/MCP call history,
and their query surfaces. It has a separate custodial database so its retention
policy can differ from the corpus and cron. The package and its database are
always installed for call-history dual-write, on every run that is not
read-only; only the staged agent-memory write and query routes remain behind
the existing rollout switch:

```toml
[features]
roca_ops = true
```

Its resident manifest declares the `plugin_roca_ops` attach alias, the
operational memory semantic fragment, custody and retention, and the `store`,
`query`, `sql`, and `exec` verbs. Those declarations grant the existing CLI
commands and MCP tools their seats without changing their handlers or output.
`sql` is the exception to a verb naming a command of its own: its capability
names `roca playground --sql-only`, which stays its only CLI surface, while
MCP keeps the `roca_sql` tool. Historical operational rows in the compatibility
database remain readable; the manifest migration does not move data. It owns an
accent-insensitive `memories_fts` index over its own memories, rebuilt on every
schema apply so a database that predates the index answers for the rows it
already held. Its `call_history` table retains every current `CallRecord` field
plus the redacted surface record. Checksummed segment and parity tables are
internal migration bookkeeping; they do not enter generated SQL or the
read-only query surface.

DATA-2 also prepares a second, hidden memory route in that same custodial
database. `memory_records` holds the multiset union of ops, core, and harvested
corpus payloads: byte-equivalent identities from different sources share one
record, while duplicates within a source and divergent payloads remain physical
versions. Plugin-local custody memberships and `memory_compatibility` retain
every legacy database label and ID. Its derived `memory_records_fts` is rebuilt
and checked before the ledger becomes `verified`. These names stay outside
prompts and the SQL gate during shadow mode, so the served `memories`/
`memories_fts` route and source databases remain untouched until the atomic
cutover.

The DATA-6 cutover coordinator in `internal/distribution/datasplit/cutover.go`
is DATA-2's sole runtime caller, the way DATA-1 shipped the ledger with only
`Prepare` wired: no installer and no command invokes the copy directly. Sources
are free to move between an interrupted run and its resume. A row whose payload
changed is carried forward as a further version of the same legacy ID rather
than refused, a row that disappeared keeps the membership its batch truthfully
recorded, and both are reported as drift events; membership counts are verified
against what the committed batches recorded, not against the live source. A
home whose three sources are all empty verifies as `verified-empty` rather than
`verified`: nothing was carried, so the migration stays open and a later run
still carries whatever the sources hold by then, while the home counts as
cutover-ready because there is nothing left to carry. Each source's frozen copy
is named once per migration generation and published by renaming a validated
sibling copy over it, so retries replace their own snapshot instead of
accumulating a full database per attempt, a failed replacement leaves the
previously verified copy intact, and no reader sees a half-written database.

A plugin database hosts as many custody migrations as it needs. `plugin_schema`
keeps the plugin's schema and index versions and nothing else that is current:
its `migration_state` and verification columns are the DATA-1 shape, kept only
for the databases that already carry them. `plugin_migrations` keys every
lifecycle transition and verification outcome by a stable migration name, and
both batches and memberships are keyed by that name beside the destination the
migration owns. Batch identity is local to its migration, so two migrations may
number their batches however they like and still commit the same id. Two
migrations in one database therefore prepare, batch, resume, and verify
independently: neither counts the other's batches, reuses its destination, nor
overwrites its state, and each reaches `verified` or `verified-empty` on its
own. DATA-2 owns `data2-memory-custody` over `memory_records`, and the corpus
shadow archive owns one `corpus-archive-<family>` name per version table. A
DATA-1 database adopts this in place on the next `Prepare` — the rows it
already held stay under an unclaimed empty name, so they can never stand in for
a migration that has not run. A plugin schema or index bump reopens every named
migration, because the destination those migrations fill may have moved under
them.

## Scheduled rides

`roca cron` is a lightweight train: an external observer that invokes work
already owned by the kernel or a plugin. It does not ingest, embed, or keep a
daemon alive. System cron calls `roca cron run`; omitting the train selects
`nightly`. The command is gated by a feature switch that fresh init writes as
true:

```toml
[features]
cron = true
```

With `features.cron` absent or false, the command is not registered and behaves
as though it does not exist. The kernel registers its own `roca ingest` command
as the first nightly ride, so direct ingest remains available unchanged, and
the plugin name `core` is reserved for that built-in ride namespace.

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
