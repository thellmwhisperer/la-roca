# Project agent memory

This file is the project's committed home for project-intrinsic agent knowledge: build, test, release, architecture, and sharp-edge notes that should travel with the code.

- Run the canonical local gate with `make check`; inspect the Gherkin catalogue with `make accept-index`.
- Per-domain acceptance lives under `features/<domain>/`; the Godog harness is enabled only by the `acceptance` build tag.
- The slop gate ratchets over test code too, so new tests alone can fail `make check`: fold paired cases into one table-driven test rather than raise the ceiling in `.slop/`.
- Public source, documentation, features, and fixtures are English-only; use unmistakably synthetic test data.
- Never add Sherpa-style navigational comment blocks or numbered section maps to any file; the owner forbids them repository-wide.
- Keep distribution ownership declarations centralized in `internal/distribution/cli/uninstall.go` (`ownedPaths` and recovery-backup handling).
- Operational JSONL lives under the selected data directory's `logs/`; the stable call contract, doctor reader, rotation, retention, and redaction are owned by `internal/distribution/logfile` and documented in `docs/operations.md`.
- MCP answers are TOON-only text: never return row envelopes in `StructuredContent`; the contract lives in `internal/distribution/mcpplug/toon_contract_test.go`.
- A query costs two inferences and only the second one sees result rows; they may run on different providers (`models.interpret_order`). Keep rows out of the SQL prompt: the guarantee is pinned in `internal/provider/service/two_inferences_test.go`.
- Keep local-binary isolation and corpus exclusion synchronized: shipped command data lives in `internal/provider/command_presets.go`, the generic adapter in `internal/provider/localbinary.go`, and the runner ingest guard is pinned in `internal/ingest/detection_test.go`.
- There is no migration runner: `data/schema.sql` is the only schema, and `internal/store/adopt.go` compares an existing database against it and adds what is missing, so a new column must be nullable with a constant default or adoption refuses it.
- Teaching a parser to read more of a source means bumping its entry in `parserVersions` (`internal/ingest/state.go`): the version rides in the watermark so a plain `roca ingest` re-reads synced files, and `internal/ingest/provenance_test.go` pins that the re-read backfills without duplicating.
- Per-exchange provenance is filled only from what a source itself recorded; a column NULL means the source said nothing, never zero. Use `parsers.UsageTally` rather than assembling `parsers.Provenance` by hand.
- Records left out are reported apart: `parsers.Discard.ByDesign` is what this build never meant to read, and everything else is what it could not read. Collapsing the two is what made a healthy ingest report thousands of failures.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
