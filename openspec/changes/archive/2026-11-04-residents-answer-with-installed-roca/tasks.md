## 1. Residents

- [x] 1.1 Pass the starting `roca`'s path (`HostBinary`) on every start of the vector resident: the MCP server's start in `internal/distribution/mcpplug/resident.go` and the companion client's start in `plugins/vector/cmd/roca-vector/resident_query.go`.
- [x] 1.2 In the install step that `install.sh` and `roca update` run, stop the home's running core and vector residents once the new binaries are in place, before the command returns. No client stops a resident during a query or an MCP request.

## 2. End to end

- [x] 2.1 Write the four scenarios of `specs/resident-binaries/spec.md` in `features/distribution/e2e-federation.feature`, run by `make e2e` against the installed candidate with the pinned embedding model, installing with `install.sh` from the suite's local release channel; record that each fails on v1.94.1.
  - Recorded on head c24412f: `make e2e-federation` with v1.94.1 source built as v1.94.2, so the upgrade scenarios can run, fails all four scenarios at their assertions (three on `engines = ["fts"]`, one on `find the roca core executable on PATH`). The candidate passes 46 of 46.
- [x] 2.2 `make e2e-cover` passes: every changed line is executed by an end-to-end run.
