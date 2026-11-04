## Why

The core resident (`roca mcp serve --resident`) opens its service once, when it
starts, and keeps that service until a signal stops it. It reads `config.toml`
only then. An operator who changes `config.toml` while an agent session keeps
the resident alive gets answers from the old configuration, with notices that
send them to repeat what they already did.

- `internal/distribution/cli/serve.go` opens the service once for the resident.
  What that open reads from `config.toml` stays fixed for the resident's life:
  `features.vector`, `roca_ops`, the serving layout and the query settings
  (`openService` in `internal/distribution/cli/cli.go`).
- The handshake between a client and the resident compares only the database
  path (`handle` in `internal/distribution/resident/server.go`). It carries no
  build and no configuration.
- The core resident has no idle exit (docs/mcp.md). The install step retires it
  since #588, but nothing retires it when `config.toml` changes, or when the
  `roca` executable is replaced on disk by any path other than `install.sh` and
  `roca update`.
- `roca doctor` prints the resident's PID, uptime and connections. It prints
  neither the build that answers nor whether that build's configuration is the
  one on disk.

Who asked: `proposal`, for the three requirements: the resident never answers
with another configuration or build, `roca doctor` names both, and an
end-to-end scenario proves the documented way to turn vectors on with an agent
session open.

Proof: on integration a531af9 (the tree of cfdaac6), darwin-arm64, in an empty
`HOME`, following AGENTS.md step two with an agent session open:

```
$ roca init --db-path ~/.roca/roca.db          # writes vector = false
$ roca store --layer discovery --content "orchard compass note about the harbor lantern"
# an MCP client starts `roca mcp serve` and calls roca_query once:
# the core resident now runs
# vector = false becomes vector = true under [features] in ~/.roca/config.toml
$ roca vector install                          # exit 0
vector worker: exit 0 · 1 added · 0 updated · 0 removed · 1 chunks
$ roca vector status
  roca-ops,ops,memories,1,1,3379200,"<time>",complete,unheld,false
$ roca query "orchard compass" --json
  engines: ["fts"]   notices: ["vector index is not installed; continuing with FTS-only"]
$ roca vector query "orchard compass" 3
error: semantic search is disabled; enable features.vector in config.toml
$ roca doctor
  resident: pid=<pid> · uptime=1.051s · attached clients=1 · open connections=5
  roca-ops/ops · 1 chunks · 3379200 bytes · state complete · lock unheld
```

`roca migrate` and `serving = "cutover"` afterwards change nothing: `roca query`
still answers with `engines: ["fts"]`. With the same configuration, after the
resident process is stopped by hand, `roca query` answers with `engines:
["fts", "vector"]`.

## What Changes

- A core resident compares the `config.toml` it was opened with, and the `roca`
  executable it was started from, with the ones on disk whenever a client
  connects and before it runs each request. When either changed, it retires:
  it stops accepting clients and frees its socket, runs nothing more, and exits
  once the calls it already started have returned. The call that found the
  change is answered with the current configuration, by the CLI's local path
  or by a resident that the existing locked start brings up.
- An agent MCP session that stays open across the change keeps working: its
  next tool call is answered by a resident opened with the current
  configuration, and no request the old resident did not run is reported
  failed.
- `roca doctor`, text and `--json`, reports the version and commit of the
  resident it reached, the `config.toml` it was opened with, and the settings
  it answers with: `vector`, whether it serves `roca_ops`, and the serving
  layout.
- An end-to-end scenario turns vectors on with the resident alive and checks
  that `roca query` uses them.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `resident-binaries`: adds that a resident answers only with the configuration
  and the `roca` executable on disk, and that `roca doctor` names the build and
  configuration of the resident it reached.

## Impact

- Code: `internal/distribution/resident/server.go` (the check at connect and
  before each request, and the retire), `pkg/resident/resident.go` (how a
  client and the MCP shim read a retiring resident),
  `internal/distribution/cli/serve.go` (what the resident records at start),
  the resident status and `roca doctor` rendering
  (`internal/provider/service/doctor.go`), and the resident paragraphs of
  docs/mcp.md.
- Acceptance scenarios to write, run by `make accept`: a resident started
  before `config.toml` changes, then a CLI call, with the stub companion; and
  the `roca doctor` resident line.
- End-to-end scenarios to write, run by `make e2e` with the pinned embedding
  model, in `features/distribution/e2e-federation.feature`: the proof above,
  for `roca query`, `roca vector query` and an open MCP session. They fail on
  cfdaac6.
- Real sizes, from the reproduction: `config.toml` under 100 bytes, one memory,
  one ops chunk, an ops sidecar of 3,379,200 bytes. The check adds, per connect and
  per request, one read of `config.toml` and one `stat` of the executable.
- Not to build: no idle exit for the core resident, no file watcher, no reload
  of the configuration inside a running service, no change to what `install.sh`
  or `roca update` run, no signal to residents of another data directory, no
  new field in `roca query --json`.
- Not touched: the vector resident (`roca-vector _resident`), its idle exit and
  its capability check; how `roca vector status` and `roca doctor` report a
  failed worker run; the CI artifact for darwin; the loop of #571; environment
  variables a resident inherited from whatever started it.
