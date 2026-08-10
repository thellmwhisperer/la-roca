# Distribution delta inventory

This inventory lists behavior present in distribution code that none of the 13 approved distribution scenarios claims. It is a pruning input, not a deletion proposal.

## CLI surface

- Hidden root commands remain callable: `version`, `exec`, `schema`, `index`, `health`, `mcp`, `skill`, `logout`, `model`, and `models`.
- Nested command surfaces remain unclaimed: schema inspection, model selection/catalogue, MCP install/uninstall/status, and skill destination listing.
- The scenarios do not claim most flags: database path selection; query layer, SQL-only, and custom budgets beyond the cross-surface property; store provenance/status/metadata/supersession; update version/repository/binary/check selection; or MCP configuration/executable overrides.
- Interactive init adoption/reinitialization, provider login/logout flows, read-only refusals, exit-code distinctions, spinner behavior, and contextual help rows are not claimed.

## MCP plug

- The exact five-tool catalogue, JSON schemas, descriptions, handshake metadata, and pipe-close behavior are exercised elsewhere but not claimed by these 13 scenarios.
- `roca_sql`, arbitrary gated `roca_exec`, store deduplication/audit metadata, health sampling, malformed-call recovery, read-only behavior, and database-path scrubbing are unclaimed here.
- Structured-content envelopes beside readable AXI/TOON text remain broader than the row-parity scenario.

## Agent configuration and skill plumbing

- `agentcfg` supports TOML, YAML, JSON, and JSONC surgical edits for Claude, Codex, Hermes, OpenCode, and Pi, including environment path overrides, status reports, idempotency, concurrent-change refusal, and recovery backups.
- Skill path overrides, repeated-install idempotency, canonical-content protection during uninstall, and runtime discovery listing are unclaimed.

## Human rendering

- Duration formatting, route narration, contextual help, quoting/escaping, stable column ordering, search-centered excerpts, and the fixed 160-rune terminal cell ceiling are not claimed beyond the explicit operator budget.

## Lifecycle and release

- Checksum verification, archive extraction, download limits, token privacy, API/direct-download fallback, interrupted-install convergence, foreign-binary refusal, symlink resolution, atomic swap, version health checks, and rollback are unclaimed.
- Purge convergence after partial failure, refusal to delete foreign files, bounded survivor reporting, SQLite journal cleanup, and JSON path privacy are broader than the zero-residue synthetic-home scenario.
- Release discovery by latest or tag, private repository authentication, platform rejection, and update rollback after an unhealthy replacement remain unclaimed.

Authoritative code inspected: `internal/distribution/axi`, `internal/distribution/cli`, `internal/distribution/mcpplug`, `internal/distribution/agentcfg`, `internal/distribution/lifecycle`, `internal/distribution/release`, and `install.sh`.
