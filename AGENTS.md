# Project agent memory

This file is the project's committed home for project-intrinsic agent knowledge: build, test, release, architecture, and sharp-edge notes that should travel with the code.

- Run the canonical local gate with `make check`; inspect the Gherkin catalogue with `make accept-index`. That gate stops at the root module, so a change under `plugins/<name>/` also owes `make -C plugins/<name> check`.
- Per-domain acceptance lives under `features/<domain>/`; the Godog harness is enabled only by the `acceptance` build tag.
- The slop gate ratchets over test code too, so new tests alone can fail `make check`: fold paired cases into one table-driven test rather than raise the ceiling in `.slop/`.
- Public source, documentation, features, and fixtures are English-only; use unmistakably synthetic test data.
- Never add Sherpa-style navigational comment blocks or numbered section maps to any file; the owner forbids them repository-wide.
- Keep distribution ownership declarations centralized in `internal/distribution/cli/uninstall.go` (`ownedPaths`, the `~/.roca` plugin trees, and recovery-backup handling); archived custodial plugin data is the one tree a purge owns only after its own consent.
- Managed agent artifacts use the shared zones and schema-versioned registry in `internal/artifact`; installers register skill, prompt, and hook ownership, and `ownedPaths` consumes that registry rather than growing a second install inventory.
- Verified executable-only plugin packages declare their kind and optional writable state directory in `plugin.json`; `internal/distribution/plugininstall` preserves that manifest-owned namespace across updates and supplies purge ownership. Keep them out of data-plugin discovery; the contract and worked example live in `docs/plugins.md`.
- Operational JSONL lives under the selected data directory's `logs/`; the stable call contract, doctor reader, rotation, retention, and redaction are owned by `internal/distribution/logfile` and documented in `docs/operations.md`.
- MCP answers are TOON-only text: never return row envelopes in `StructuredContent`; the contract lives in `internal/distribution/mcpplug/toon_contract_test.go`.
- A prose query or explore costs two inferences and only the second one sees result rows; `models.interpret_order` routes that seat, while deep explore may try `models.explore_order` first. Keep rows out of the SQL prompt and terrain facts derived from the run's returned rows; the contracts live in `internal/provider/service/two_inferences_test.go` and `explore_test.go`.
- Keep local-binary isolation and corpus exclusion synchronized: shipped command data lives in `internal/provider/command_presets.go`, the generic adapter in `internal/provider/localbinary.go`, and the runner ingest guard is pinned in `internal/ingest/detection_test.go`.
- Account exports are invocation-scoped: `roca ingest <path>` imports one extracted ChatGPT or Claude snapshot, while plain/nightly ingest resolves only live agent roots; the contract lives in `internal/distribution/cli/ingest.go` and `features/ingest/`. `ingest.WithExportPath` decides the vendor from the folder shape and refuses a directory that is neither, so no snapshot is diagnosed as the other vendor's.
- There is no migration runner: `data/schema.sql` is core's only schema, each bundled plugin owns its own outside adoption (`internal/distribution/{rocaops,rocacron,rocacorpus}/schema.sql`, applied idempotently on install by the shared `internal/distribution/bundledplugin` installer), and `internal/store/adopt.go` compares an existing core database against it and adds what is missing, so a new core column must be nullable with a constant default or adoption refuses it. Every pull request and release replays the frozen old-version homes through the current binary (`make upgrade-gauntlet`), and a release whose schema change an existing database must adopt freezes one more of them; `docs/releases.md` owns that definition of done.
- Teaching a parser to read more of a source means bumping its entry in `parserVersions` (`internal/ingest/state.go`): the version rides in the watermark so a plain `roca ingest` re-reads synced files, and `internal/ingest/provenance_test.go` pins that the re-read backfills without duplicating.
- Per-exchange provenance is filled only from what a source itself recorded; a column NULL means the source said nothing, never zero. Use `parsers.UsageTally` rather than assembling `parsers.Provenance` by hand.
- Records left out are reported apart: `parsers.Discard.ByDesign` is what this build never meant to read, and everything else is what it could not read. Collapsing the two is what made a healthy ingest report thousands of failures.
- Cron ride manifests are optional `rides.toml` plugin payloads; `internal/provider/plugin/rides.go` owns discovery, but only installer-manifested, checksum-verified payloads whose recorded consent is executable may contribute rides, because a declared ride is an execution surface and never data-only. The observer and canonical custodial journey database live in `internal/distribution/rocacron`, and the train probes but never holds the core log lock.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
