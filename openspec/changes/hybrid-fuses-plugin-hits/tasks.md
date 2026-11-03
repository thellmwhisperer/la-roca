## 1. Labels

- [ ] 1.1 In the full-text branch of `internal/provider/service/hybrid.go`, keep the surface label the search plan assigned (`scopeName`) as the hit's database instead of the provenance `EnsureDatabaseColumn` writes; leave `EnsureDatabaseColumn` unchanged for `roca exec`.
- [ ] 1.2 Update the unit test that expects `plugin:roca-corpus` on a search hit (`internal/provider/service/resident_test.go`) to expect `corpus`.

## 2. Acceptance

- [ ] 2.1 Write the two scenarios of `specs/hybrid-fusion/spec.md` as acceptance scenarios run by `make accept` against the real `roca`, with a stub `roca-vector` on `PATH` that answers `query` with one fixed hit for the fixture exchange.
- [ ] 2.2 `make e2e-cover` passes: every changed line is executed by those scenarios.
