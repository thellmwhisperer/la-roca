## ADDED Requirements

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
  serve` with a `PATH` that does not contain `~/.local/bin` and called
  `roca_vector_query` once, so the residents run with that `PATH`, and the
  operator, with `~/.local/bin` on `PATH`, runs `roca query "harbor lantern"
  --json` and `roca-vector --json query "harbor lantern"`
- **THEN** `roca query` exits 0, its `engines` include `vector`, its notices
  include neither `vector plugin is not installed` nor `vector search
  unavailable`, and `roca-vector query` exits 0 with that exchange among its
  hits and does not print `find the roca core executable on PATH`

### Requirement: A resident of another build is replaced

A client that reaches a core resident or a vector resident started by a
different build SHALL NOT use it: it SHALL replace it with a resident of its
own build and get its answer from that one.

#### Scenario: An upgrade over running residents

- **WHEN** on the same kind of home, the published release before the
  candidate, installed by `install.sh` into `~/.local/bin`, started the core
  and vector residents through `roca mcp serve` with a `PATH` that does not
  contain `~/.local/bin` and one `roca_vector_query` call, the candidate was
  then installed by `install.sh` over it, and the operator, with
  `~/.local/bin` on `PATH`, runs `roca query "harbor lantern" --json`
- **THEN** the command exits 0, its `engines` include `vector`, and its notices
  include neither `vector plugin is not installed` nor `vector search
  unavailable`
