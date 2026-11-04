## Context

See proposal.md, Why. Three facts make a resident answer with a stale view:

- `serveCommand` in `internal/distribution/cli/serve.go` calls
  `env.openService()` once and hands the service to `residentserver.Run`.
  Everything `openService` reads from `config.toml` (`features.vector`,
  `rocaOpsServed`, `layout.serving`, the query settings, the companion path)
  is fixed in that service for the resident's life. The service already holds
  the configuration path (`service.Options.ConfigPath`).
- `Server.handle` in `internal/distribution/resident/server.go` accepts a
  `connect` probe and compares only the database path. A plain CLI call
  (`resident.Call` in `pkg/resident/resident.go`) opens one connection per
  call; an MCP shim (`ProxyStdio`) keeps one connection for the whole agent
  session and, when the resident goes away, reconnects with `DialOrSpawn` and
  replays `initialize`. Requests pending at a disconnect are answered to the
  agent with `resident disconnected; request outcome is unknown`.
- The CLI's hot commands (`tryResident` in
  `internal/distribution/cli/resident.go`) fall back to the local service when
  the resident is unavailable before dispatch. The local service opens the
  configuration on disk.

The install path since #588 retires residents with SIGTERM from
`_install-bundled-plugins` (`internal/distribution/cli/memory.go`). That stays.

## Goals / Non-Goals

**Goals:**

- No CLI call and no MCP request is answered by a resident whose
  `config.toml` or `roca` executable changed since it started.
- An agent session open across the change keeps working without an error.
- `roca doctor` shows which build and which settings the resident answers with.

**Non-Goals:**

- Reloading the configuration inside a running service. Pools, the vector
  client and the MCP tool list are built from it; a fresh process is the
  existing, tested way to build them.
- An idle exit for the core resident, a file watcher, or any change to the
  vector resident.
- Reporting the answering build in `roca query --json`.

## Decisions

### What the resident records at start, and what it compares

After `openService` returns, the resident records the SHA-256 of the bytes of
`config.toml` (or that the file is absent), and the identity of
its own executable: `os.Executable()` and `os.Stat` of that path (device,
inode, size and modification time). On each `connect` probe, and before it
runs each request on any connection, it reads the file again and stats the
path again. A different digest, a different presence, or a different identity
is a change. A failed stat of the executable counts as a change.

Alternatives considered:

- Modification time of `config.toml`. One-second granularity on some file
  systems and editors that keep the time would let a change through; the file
  is under a kilobyte, so reading it is as cheap as a stat.
- Hashing the executable. Tens of megabytes per call; an install renames a new
  file over the path, which changes the inode, and a copy over it changes size
  or time.
- A watcher (fsnotify or kqueue). A new dependency and a second process model
  for something a stat per call answers.

### How the resident retires

On the first change it sees, the resident closes its listener and removes its
socket at once, so the next client can start a new resident through the
existing locked start. It then answers the call that found the change with a
refusal that says it retired and ran nothing, refuses every later request on
its open connections the same way, and exits once every client connection
still open has closed. The socket is removed only by closing the listener, at
retirement, so it never deletes the socket of the resident that replaced it.

- A CLI client reads that refusal at `connect` as unavailable, so
  `tryResident` falls back to the local service, which reads the
  configuration on disk.
- The MCP shim reads that refusal on a request as "not run": it reconnects
  through `DialOrSpawn`, replays `initialize` as it does today, and sends that
  same request again. Only a disconnect without that refusal keeps the current
  `request outcome is unknown` answer. The shim therefore keeps the line of
  each pending request, not only its id.

Alternative considered: answer the call that found the change, then retire.
That call would still be answered by the old configuration, which is the
defect.

### What doctor shows

The resident status (`transport.Status`) gains the resident's version and
commit (already in `mcpplug.Build`), the configuration path, and the three
settings that change what it serves: `vector`, whether `roca_ops` is served,
and the serving layout. `roca doctor` prints them on its `resident:` line and
in its JSON resident object. A doctor call to a resident that has changed
underneath retires it and falls back like any other call, so doctor either
shows a resident that matches the disk or no resident.

### Acceptance in make accept, operator proof in make e2e

`make accept` runs no embedding model. The store scenarios start a resident
through an MCP session with the stub companion, change `config.toml`, and read
`roca doctor --json` and the CLI's local fallback. The operator outcome,
`roca query` with `vector` among its engines after `roca vector install`, needs
the real companion and the pinned model, so it runs in
`features/distribution/e2e-federation.feature` under `make e2e`, reusing the
MCP session steps of the 588 scenarios.

## Risks / Trade-offs

- [One read of `config.toml` and one stat per request] → Under a kilobyte and
  one syscall, next to SQLite queries that take milliseconds.
- [An editor that writes `config.toml` in two steps can make one resident
  retire on a half-written file] → The next process opens the file as it is
  then; a half-written file fails that open with the same error a local
  command would give, and the following call reads the finished file.
- [Two clients race to start the new resident] → The existing startup lock in
  `DialOrSpawn` serializes them, as it does after an install.
- [A request in flight when the change lands] → It finishes with the
  configuration it started with; only requests that reach the resident after
  the change are refused and resent.

## Migration Plan

No data changes. A resident of the previous build keeps running until the
install step retires it, as since #588; the first resident of this build is
already one that retires on change. Rollback is reverting the merge.
