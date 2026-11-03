## 1. Views

- [ ] 1.1 In `installHubCompatibility` (`internal/provider/service/hub.go`), make the `memories`, `exchanges` and `thinking_blocks` views select `id AS rowid` and the `sessions` view select its base `rowid AS rowid`, matching what each `*_fts` compatibility view exposes as `rowid`.

## 2. Surfaces

- [ ] 2.1 When the service runs on the hub (cutover), leave the core `sessions`, `exchanges` and `thinking_blocks` surfaces out of the search plan (`collectSurfaces` or `Search` in `internal/provider/service/hybrid.go`); keep the core `memories` surface.

## 3. Acceptance

- [ ] 3.1 Write the two scenarios of `specs/cutover-search/spec.md` as acceptance scenarios run by `make accept` against the real `roca`, on a fixture home made by `roca init --db-path`, a fixture agent session, `roca store`, `roca migrate` and `serving = "cutover"`.
- [ ] 3.2 `make e2e-cover` passes: every changed line is executed by those scenarios.
