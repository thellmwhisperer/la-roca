## 1. Residents

- [ ] 1.1 Pass the starting `roca`'s path (`HostBinary`) on every start of the vector resident: the MCP server's start in `internal/distribution/mcpplug/resident.go` and the companion client's start in `plugins/vector/cmd/roca-vector/resident_query.go`.
- [ ] 1.2 Make the core and vector resident handshakes carry the build, and have a client that meets another build replace that resident instead of using it (`pkg/resident`, `pkg/vectorresident`).

## 2. End to end

- [ ] 2.1 Write the two scenarios of `specs/resident-binaries/spec.md` in `features/distribution/e2e-federation.feature`, run by `make e2e` against the installed candidate with the pinned embedding model, installing with `install.sh` from the suite's local release channel; record that both fail on v1.94.1.
- [ ] 2.2 `make e2e-cover` passes: every changed line is executed by an end-to-end run.
