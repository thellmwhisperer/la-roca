## Why

On v1.94.1, after `install.sh` put `roca` and `roca-vector` in `~/.local/bin`,
with `~/.local/bin` on the operator's `PATH`, `roca query` still answers from
full-text search alone and `roca-vector --json query` fails with
`find the roca core executable on PATH`. The operator's own environment never
reaches the code that looks the binaries up: both commands are answered by a
resident process that is already running, and that process keeps the
environment of whatever started it.

- `roca query` hands the query to the core resident when one is alive
  (`tryResident` in `internal/distribution/cli/resident.go`). The resident was
  started as `roca mcp serve --resident` with its starter's environment
  (`DialOrSpawn` in `pkg/resident/resident.go`), and the handshake checks only
  the database path, so a resident started by an agent's MCP session, with
  that session's `PATH`, or by an older `roca` build, keeps answering.
- `roca-vector query` hands the query to the shared vector resident, which
  looks the core up on every query with its own `PATH`
  (`env.core()` in `plugins/vector/cmd/roca-vector/main.go`). The MCP server
  starts that resident without the core path (`residentOptions` in
  `internal/distribution/mcpplug/resident.go`), and so does the companion's
  own client start (`plugins/vector/cmd/roca-vector/resident_query.go`), while
  `roca vector` passes it (`HostBinary`), which is why `roca vector query`
  works on the same home.

Who asked: #588.

Proof: macOS arm64, `roca v1.94.1 (97d0afe)` and `roca-vector v1.94.1
(97d0afe)` installed by `install.sh` into `~/.local/bin`, a home in
`serving = "cutover"` with `vector = true` and a finished `roca vector install`:

```
$ env PATH="$HOME/.local/bin:/usr/bin:/bin" roca query "<term>"
search fts · engines fts · 8 ms
notice: vector plugin is not installed; continuing with FTS-only
$ env PATH="$HOME/.local/bin:/usr/bin:/bin" roca-vector --json query "<term>"
semantic search: preparing
error: find the roca core executable on PATH: exec: "roca": executable file not found in $PATH
```

## What Changes

- Every start of the shared vector resident passes the path of the `roca`
  that starts it, so the resident finds the core whatever its `PATH` holds.
- Installing or updating `roca` retires the core and vector residents the
  previous binaries left running, before the command returns, so the next
  client starts residents of the installed build through the existing locked
  start. A first attempt had each client stop residents of another build
  during a query; review showed it races (two queries start two residents)
  and fights MCP sessions opened before the upgrade, so no client stops a
  resident any more. The install step is the one place that does.

## Capabilities

### New Capabilities

- `resident-binaries`: which `roca` and `roca-vector` a resident answers with.

### Modified Capabilities

None.

## Impact

- Code: the starters of the vector resident in
  `internal/distribution/mcpplug/resident.go` and
  `plugins/vector/cmd/roca-vector/resident_query.go`, `pkg/vectorresident`
  where the core path becomes `ROCA_VECTOR_ROCA_BINARY`, and the install step
  that `install.sh` and `roca update` run (`roca _install-bundled-plugins`),
  which stops the home's residents after the new binaries are in place.
- End-to-end scenarios to write in `features/distribution/e2e-federation.feature`,
  run by `make e2e` against the installed candidate with the pinned embedding
  model: the four scenarios of `specs/resident-binaries/spec.md`. They install
  with `install.sh` from the suite's local release channel into
  `~/.local/bin`, and each fails on v1.94.1.
- Acceptance scenarios run by `make accept`, as many as `make e2e-cover` needs
  so that every changed line runs end to end.
- Not touched: the #583 companion lookup when `~/.local/bin` is not on `PATH`;
  the hit labels, fusion and duplicate hits; what the ops sidecar embeds
  (#589); `install.sh` itself.
