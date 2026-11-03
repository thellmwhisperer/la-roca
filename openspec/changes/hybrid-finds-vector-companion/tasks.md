## 1. Companion path

- [ ] 1.1 Resolve the companion once with `resolveCompanion` where `internal/distribution/cli` builds the service options, and pass the path to the hybrid leg.
- [ ] 1.2 In `PluginVectorQuery` (`internal/provider/service/hybrid.go`), use that path when it is set and fall back to `exec.LookPath("roca-vector")` when it is not; keep the `vector plugin is not installed` notice for neither.

## 2. Acceptance

- [ ] 2.1 Write the two scenarios of `specs/hybrid-vector-companion/spec.md` as acceptance scenarios run by `make accept` against the real `roca`, with a stub `roca-vector` that answers `query` with one fixed hit.
- [ ] 2.2 `make e2e-cover` passes: every changed line is executed by those scenarios.
