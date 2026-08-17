# Architecture

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

La Roca is a federating kernel surrounded by domain plugins. The kernel owns no
domain database. Its durable state is configuration and plugin manifests, and
the hub it attaches plugin-owned SQLite files to read-only is an empty in-memory
database.

The migration to that shape is incremental. The current release has
manifest-backed `roca-corpus` and `roca-ops` domains, plus a separate
`roca-cron` journey store that still uses its legacy descriptor. One atomic
`layout.serving` marker selects `legacy-serving`, `shadow-equal`, or `cutover`.
Before either federated route opens against an existing core database, the
internal cutover coordinator completes and verifies the ops, corpus, and cron
custody imports from frozen snapshots. That preparation has no public command.
DATA-3 reconciles every frozen source table and every source session against
its custody memberships: counts and canonical payload hashes must match, no
physical or membership duplicate may exist, and exchange model/provider/token
provenance has its own hash. Exact legacy occurrences remain separate
memberships even when the current parser represents several assistant fragments
as one human turn. The merge and its re-runnable verifier return an error unless
the global report is exactly 100% green.
Shadow mode serves the legacy answer and returns the marker to legacy on any
row difference. Cutover uses temporary compatibility views and indexes over
the read-only plugin attachments; it does not open `roca.db`. That file remains
in place as the reversible legacy route until the separate retirement step.

## Runtime map

```text
                         CLI and MCP
                              |
                              v
  +-------------------------------------------------------+
  | kernel                                                |
  | init | manifest engine | read-only gate | NL-to-SQL   |
  | config + manifests | in-memory SQLite attachment hub  |
  +---------------------------+---------------------------+
                              |
              +---------------+----------------+
              |                                |
              v                                v
  +---------------------------+    +---------------------------+
  | roca-corpus               |    | roca-ops                  |
  | ingest + perennial archive|    | operational writes/reads  |
  | corpus-owned database     |    | ops-owned database        |
  | corpus retention policy   |    | ops retention policy      |
  +---------------------------+    +---------------------------+

  roca-cron owns a third database for run history and persisted errors.
```

The manifest engine owns discovery, strict schema validation, read-only
attachment, semantic catalog composition, and the canonical registry for verbs
and executable capabilities. Removing a plugin removes its databases and
surface declarations without requiring edits to the kernel.

Retention and scale follow the same boundary. Corpus may preserve a perennial
archive, ops may keep a shorter operational window, and cron may prune
successful journeys while retaining failures. No global kernel policy silently
applies one domain's diet to another.

## The internal dependency rule

`internal/` has four layers, bottom up. No domain imports the one above it.

```text
store        - SQLite primitives and lexical indexing
ingest       - source scanning, parsers, and idempotent writes
provider     - models, manifests, semantic catalog, gate, and services
distribution - CLI, MCP, installers, lifecycle, and release plumbing
```

Production imports obey the rule; test files may reach upward to build
fixtures.

## What lives where

- `internal/store/` owns SQLite access, schema adoption, backups, and FTS.
- `internal/ingest/` owns source detection, pure parsers, provenance, and
  fingerprinted incremental writes.
- `internal/provider/plugin/` is the manifest engine: declarations, discovery,
  schema truth checks, semantic composition, verb and capability registration,
  and the in-memory hub.
- `internal/provider/query/` owns prompt construction and the SQL read gate;
  `internal/provider/service/` orchestrates the compatibility product surface.
- `internal/distribution/plugininstall/` verifies packages and preserves every
  manifest-declared database across updates.
- `internal/distribution/{rocacorpus,rocaops}/` ship resident manifests and own
  their respective schemas. `rocacron/` retains its descriptor until its
  scheduled manifest migration.
- `internal/distribution/cli/` and `mcpplug/` project the shared service and
  plugin registry onto the two agent surfaces.
- `internal/distribution/cli/artifacts.go` owns artifact discovery, rollout
  gating, refresh reports, and the post-update handoff to the new binary.

The physical `sessions`, `exchanges`, `thinking_blocks`, and `tool_uses` tables
are corpus custody. In normal runtime operation only `internal/ingest/` may
create or update those records, and it writes them in the `roca-corpus`
database when federation is enabled. Memory store calls—including CLI, MCP,
core, and plugin-origin calls—cannot cross that boundary, and explicit SQL is
always read-only. The owner-gated exact-dedup maintenance command is the sole
offline maintenance exception: it may remap and remove certified duplicate
custody rows in the federated `roca-corpus` and `roca-ops` databases, but it
cannot modify the pre-federation `roca.db`, create source observations, or
collapse divergent payloads.

`internal/artifact` remains a shared file primitive below init and
distribution. External plugin source trees may live under `plugins/<name>/` as
independent modules; the root Go module does not import them.

## Query path

1. Discovery reads installed manifests and reports malformed declarations.
2. The semantic router selects resident databases plus relevant on-demand
   databases.
3. The engine checks declared tables and ordered columns against each real
   SQLite schema.
4. The validated fragments are composed into the catalog shown to NL-to-SQL.
5. The kernel attaches selected files read-only and validates the generated
   `SELECT` under the same gate used for explicit SQL.
6. Results declare consulted and omitted databases and carry row provenance.

The first inference sees schema, never result rows. The optional interpretation
inference sees only returned rows. Moving schema ownership into manifests does
not change that privacy boundary or the behavior of `roca query`.

See [Plugins](plugins.md) for the manifest schema and a complete build-your-own
package.
