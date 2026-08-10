# Architecture: the four domains

`internal/` is four layers, bottom up. **No domain imports the one above it.**
A package that reaches upward is a defect to fix by moving code, not by a
shortcut.

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
- `internal/provider/` — the capabilities: model providers (Codex, OpenAI,
  Ollama, API key), OAuth, the model catalog, the semantic layer (`layers`),
  configuration (`config`), the query/NL-to-SQL surface (`query` with
  `query/sqlgate`), prompts, FTS, and service orchestration (`service`).
- `internal/distribution/` — the plumbing: CLI (`cli`), MCP stdio (`mcpplug`),
  install/uninstall of the binary and of agent configs (`agentcfg`, `release`,
  `lifecycle`), and skill install (`skill`). The `human` formatting helper
  lives here because only the CLI uses it.

## Notable placements

- `search` sits under `store`: its production code imports only `store`, and
  `provider/query` and `provider/service` build on it.
- `skill` sits under `distribution`, not `provider`: its only consumer is the
  CLI and it needs `agentcfg`; placing it in provider would force a
  provider→distribution edge.
