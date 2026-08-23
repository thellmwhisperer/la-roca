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
DATA-3 refuses cutover unless its reproducible custody reconciliation is exactly
100% green; the detailed count, hash, provenance, and occurrence contract is
owned by the [bundled `roca-corpus` plugin](plugins.md#the-bundled-roca-corpus-plugin).
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
attachment, semantic catalog composition, vector registry projection, and the
canonical registry for verbs and executable capabilities. Removing a plugin
removes its databases and surface declarations without requiring edits to the
kernel.

Retention and scale follow the same boundary. Corpus may preserve a perennial
archive, ops may keep a shorter operational window, and cron may prune
successful journeys while retaining failures. No global kernel policy silently
applies one domain's diet to another.

## The internal dependency rule

`internal/` has four layers, bottom up. No domain imports the one above it.

```text
store        - SQLite primitives and lexical indexing
ingest       - source scanning, parsers, and idempotent writes
provider     - models, manifests, semantic catalog, vector registry, gate, and services
distribution - CLI, MCP, installers, lifecycle, and release plumbing
```

Production imports obey the rule; test files may reach upward to build
fixtures.

## What lives where

- `internal/store/` owns SQLite access, schema adoption, backups, and FTS.
- `pkg/incrementality/` owns the public fingerprint and persisted unchanged-pass
  primitives that scanners can reuse.
- `internal/ingest/` owns source detection, pure parsers, source-specific
  provenance extraction, and idempotent corpus writes built on those primitives.
- `pkg/ingestprovenance/` exposes the canonical source-to-harness mapping and
  historical provenance backfill to external Go modules.
- `pkg/corpuswriter/` is the public normalized-conversation write facade. It
  delegates to the ingest session writer so deduplication and FTS behavior have
  one implementation.
- `internal/provider/plugin/` is the manifest engine: declarations, discovery,
  schema truth checks, semantic and vector projection, verb and capability
  registration, and the in-memory hub.
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
are corpus custody. In normal runtime operation they enter through the session
writer in `internal/ingest/`, either from ingest itself or through the public
`pkg/corpuswriter` facade, and federation directs them to the `roca-corpus`
database. Memory store calls—including CLI, MCP, core, and plugin-origin
calls—cannot cross that boundary, and explicit SQL is always read-only. The
owner-gated exact-dedup maintenance command is the sole
offline maintenance exception: it may remap and remove certified duplicate
custody rows in the federated `roca-corpus` and `roca-ops` databases, but it
cannot modify the pre-federation `roca.db`, create source observations, or
collapse divergent payloads.

`internal/artifact` remains a shared file primitive below init and
distribution. External plugin source trees may live under `plugins/<name>/` as
independent modules; the root Go module does not import them.

## Read paths

`roca query` resolves the requested database scope, discovers full-text and
vector surfaces from the validated manifests, selects rare terms from the live
FTS indexes, and fuses full-text and template-expanded vector candidates by
stable `database.table.id` identity. It invokes no answering model. Missing or
incompatible vector sidecars leave the same federated FTS path running, and
every hit carries its source and per-leg evidence. Snippet resolution uses each
manifest's declared ID and text columns, so every vector-declared table follows
the same path without a hardcoded source-family map.

`roca playground`, `roca explore`, and `roca_sql` retain the model-written SQL
path. Discovery validates semantic declarations against each real SQLite
schema, the selected fragments form the NL-to-SQL catalog, and every generated
`SELECT` passes the same read-only gate as explicit SQL. The first inference
sees schema, never result rows; optional interpretation sees only returned
rows. Results declare consulted and omitted databases and carry row provenance.

See [Plugins](plugins.md) for the manifest schema and a complete build-your-own
package.
