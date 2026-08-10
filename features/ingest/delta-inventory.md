# Ingest capability delta inventory

Capabilities below exist in ingest code but no approved scenario in this wave
claims them. This is an inventory for later pruning or contract work, not a
request to expand this wave.

| Unclaimed capability | Where it lives |
|---|---|
| Config, environment and platform precedence for every source root, including XDG, Windows and `~` expansion | `internal/ingest/roots.go` |
| Windows/WSL path equivalence, longest-root selection and encoded-path ambiguity handling beyond the global-project example | `internal/ingest/project.go` |
| Claude compaction placement, failed tool-result backfill, latency calculation and partial-line tolerance | `internal/ingest/parsers/claude.go` |
| Cowork sidecar merging and audit-only turn boundaries | `internal/ingest/parsers/claude.go`, `internal/ingest/parsers/metadata.go` |
| Subagent layout variants, parent identity and compact session-level thinking | `internal/ingest/scan.go`, `internal/ingest/parsers/subagent.go` |
| Pi active-branch selection, incomplete-tool deferral and source exchange fingerprints | `internal/ingest/parsers/pi.go` |
| OpenCode graph traversal, live-tool deferral, compactions, stable source IDs and malformed-row isolation | `internal/ingest/opencode.go` |
| Hermes live-session deferral, cost/token metadata, finish reasons and embedded tool results | `internal/ingest/hermes.go` |
| Codex state-database enrichment for thread metadata and parent/child spawn edges | `internal/ingest/codex_state.go` |
| Foreign-database schema validation and read-only snapshot handling | `internal/ingest/foreign.go` |
| Memory update-in-place, unchanged-memory accounting and immutable landed exchanges whose source later edits them | `internal/ingest/write.go` |
| Configurable subagent roots, symlink de-duplication and non-recursive Pi scanning | `internal/ingest/roots.go`, `internal/ingest/scan.go` |
| Human progress narration, deferred-exchange summary and root-path diagnostics | `internal/ingest/ingest.go`, `internal/distribution/cli/ingest.go` |
