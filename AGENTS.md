# Project agent memory

This file is the project's committed home for project-intrinsic agent knowledge: build, test, release, architecture, and sharp-edge notes that should travel with the code.

- Run the canonical local gate with `make check`; inspect the Gherkin catalogue with `make accept-index`.
- Per-domain acceptance lives under `features/<domain>/`, one `delta-inventory.md` per domain beside its features; the Godog harness is enabled only by the `acceptance` build tag.
- The slop gate ratchets over test code too, so new tests alone can fail `make check`: fold paired cases into one table-driven test rather than raise the ceiling in `.slop/`.
- Spanish fixtures under `features/` and `test/acceptance/` are deliberate coverage for answering in the question's language; they are not sweep leakage.
- Keep distribution ownership declarations centralized in `internal/distribution/cli/uninstall.go` (`ownedPaths` and recovery-backup handling).
- Operational JSONL lives under the selected data directory's `logs/`; retention and redaction are owned by `internal/distribution/logfile`.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
