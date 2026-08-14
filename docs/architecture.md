# Architecture: the four domains

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

`internal/` is four layers, bottom up. **No domain imports the one above it.**
A package that reaches upward is a defect to fix by moving code, not by a
shortcut.

## Data domains

The engine and its data domains are separate contracts. Bundled domain plugins
live under `internal/distribution/`, own one custody database and describe its
query surface in `semantic.yaml`. `roca-ops` owns operational agent writes.
`roca-corpus` declares the perennial-harvest schema for sessions, exchanges,
reasoning, tool uses, and harvested files. It is always resident in the CLI,
owns new ingest writes and its lexical index, and participates in model and
deterministic retrieval. Historical core rows remain readable without an
implicit custody migration.

## The dependency rule

```
store        — the bottom. Nothing imports it from below.
ingest       — depends on store.
provider     — depends on store + ingest.
distribution — depends on provider (and therefore everything below it).
```

Production imports obey the rule; test files may reach upward to build
fixtures (an external test seam, never a product import).

## What lives in each

- `internal/store/` — SQLite, schema, backups, adopting foreign databases,
  and the lexical FTS search engine (`store/search`: engine, index, match).
- `internal/ingest/` — source scanning, pure parsers (`parsers/`), idempotent
  writes keyed by fingerprint.
- `internal/provider/` — the capabilities: detected local agent CLI providers,
  Ollama, custom local commands, the model catalog, the semantic layer
  (`layers`), isolated plugin schemas (`plugin`), configuration (`config`), the
  query/NL-to-SQL surface (`query` with `query/sqlgate`), prompts, FTS, and
  service orchestration (`service`).
- `internal/distribution/` — the plumbing: CLI (`cli`), MCP stdio (`mcpplug`),
  install/uninstall of the binary and of agent configs (`agentcfg`, `release`,
  `lifecycle`), verified plugin packages (`plugininstall`), redacted JSONL
  traces (`logfile`), the external ride train and its journey plugin
  (`rocacron`), and skill install (`skill`).

## Notable placements

- `search` sits under `store`: its production code imports only `store`, and
  `provider/query` and `provider/service` build on it.
- `skill` sits under `distribution`, not `provider`: its only consumer is the
  CLI and it needs `agentcfg`; placing it in provider would force a
  provider→distribution edge.
- `internal/artifact` is a shared file primitive below those domains: both init
  and distribution use its zones and registry without creating a
  provider→distribution edge.
- `internal/distribution/cli/artifacts.go` orchestrates discovery, rollout
  gating, refresh reports, and the post-swap handoff to the new binary.
- `plugins/<name>/` sits outside `internal/` and outside the root Go module:
  each one is its own module and binary, installed as a verified package
  instead of linked in, so no core package imports it and the root `make check`
  does not reach it. `plugins/vector/` is the worked example, described in
  [Plugins](plugins.md#worked-executable-example-vector-search).
