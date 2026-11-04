# resident-binaries Specification

## Purpose
`roca query` and `roca-vector query` are often answered by a core or vector
resident that is already running. This capability says which `roca` and
`roca-vector` those residents answer with: the vector resident always gets the
path of the `roca` that starts it, and installing `roca` retires the residents
the previous build left running.

## Requirements

### Requirement: The vector resident always knows its core

Every start of the shared vector resident SHALL pass it the path of the `roca`
that starts it, so that `roca-vector query` and the vector leg of `roca query`
find the core whatever `PATH` the resident was started with.

#### Scenario: A resident started by an MCP session with another PATH

- **WHEN** `roca` and `roca-vector` were installed by `install.sh` into
  `~/.local/bin`, a home was made by `roca init` with `vector = true`, it
  ingested an agent session whose exchange mentions `harbor lantern`, `roca
  vector install` finished, `roca migrate` is verified and `serving =
  "cutover"` is set under `[layout]`, an MCP client then started `roca mcp
  serve` with a `PATH` without `~/.local/bin` and called
  `roca_vector_query` once, so the residents run with that `PATH`, and the
  operator, with `~/.local/bin` on `PATH`, runs `roca query "harbor lantern"
  --json` and `roca-vector --json query "harbor lantern"`
- **THEN** `roca query` exits 0, its `engines` include `vector`, its notices
  include neither `vector plugin is not installed` nor `vector search
  unavailable`, and `roca-vector query` exits 0 with that exchange among its
  hits and prints no `find the roca core executable on PATH`

### Requirement: Installing roca retires the residents of the previous build

When `install.sh` or `roca update` replaces `roca`, the core and vector
residents of the home that the previous binaries left running SHALL be
stopped before the command returns, so the next client starts residents of
the installed build through the existing locked start. No client SHALL stop a
resident while answering a query or an MCP request.

#### Scenario: An upgrade over running residents

- **WHEN** on the same kind of home, the published release before the
  candidate, installed by `install.sh` into `~/.local/bin`, started the core
  and vector residents through `roca mcp serve` with a `PATH` without
  `~/.local/bin` and one `roca_vector_query` call, the candidate was
  then installed by `install.sh` over it, and the operator, with
  `~/.local/bin` on `PATH`, runs `roca query "harbor lantern" --json`
- **THEN** the command exits 0, its `engines` include `vector`, and its notices
  include neither `vector plugin is not installed` nor `vector search
  unavailable`

#### Scenario: An MCP session that stays open across the upgrade

- **WHEN** on the same kind of home, an MCP session started by the published
  release through `roca mcp serve` called `roca_vector_query` once, the
  candidate was installed by `install.sh` over it while that session stayed
  open, and the session then calls `roca_vector_query "harbor lantern"` again
- **THEN** that call returns that exchange among its hits, and a `roca query
  "harbor lantern" --json` run afterwards still has `vector` among its
  `engines`

#### Scenario: Two queries right after the upgrade

- **WHEN** on the same kind of home, the candidate was installed by
  `install.sh` over the published release while its residents ran, and the
  operator starts two `roca query "harbor lantern" --json` at the same time
- **THEN** both exit 0 with `vector` among their `engines`, and once both have
  returned exactly one vector resident process runs for the home
