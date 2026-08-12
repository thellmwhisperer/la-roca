# Plugins

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

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

## Commands and core-memory writes

An optional `roca-<name>` executable on `PATH` remains the command surface.
When `roca <name>` is not built in, La Roca hands that executable the remaining
arguments, standard streams, and exit status unchanged. Built-ins win. The
current directory is never searched. `roca plugins` lists these executables;
data-plugin discovery does not change dispatch.

A plugin that intentionally writes a core memory uses `roca store` or MCP with
`--origin plugin:<name>`. Direct writes to `roca.db` are outside the plugin
contract. Executables run with the user's permissions and are not a sandbox.
