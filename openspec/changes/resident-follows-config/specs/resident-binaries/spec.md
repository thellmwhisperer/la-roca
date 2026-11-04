## ADDED Requirements

### Requirement: A resident answers only with the configuration and build on disk

The core resident SHALL answer a CLI call or an MCP request only with the
`config.toml` and the `roca` executable on disk when that call reaches it. A
resident that finds either changed since it started SHALL retire, and that call
and every later one SHALL be answered with the configuration on disk, with no
error for a request the retiring resident never ran.

#### Scenario: Turning vectors on while an agent session keeps the resident alive

- **WHEN** `roca` and `roca-vector` were installed by `install.sh` into
  `~/.local/bin`, a home was made by `roca init` with `vector = false`, it
  holds a memory mentioning `harbor lantern`, an MCP client started `roca mcp
  serve` and called `roca_query` once, so the core resident runs, the operator
  then sets `vector = true` under `[features]` in `config.toml`, runs `roca
  vector install` and waits for its worker to exit 0, and, with
  `~/.local/bin` on `PATH`, runs `roca query "harbor lantern" --json` and
  `roca vector query "harbor lantern" 3`
- **THEN** `roca query` exits 0, its `engines` include `vector` and its notices
  include no `vector index is not installed`, and `roca vector query` exits 0
  with that memory among its hits and prints no `semantic search is disabled`

#### Scenario: The open agent session after the change

- **WHEN** on the same home, the MCP session that started the resident stays
  open while the operator sets `vector = true` and `roca vector install`
  finishes, and that session then calls `roca_query "harbor lantern"`
- **THEN** that call returns the memory with `vector` among its legs, the
  session received no error for any call it made, and a `roca query "harbor
  lantern" --json` run afterwards still has `vector` among its `engines`

#### Scenario: The roca executable replaced by hand

- **WHEN** on a home whose core resident was started by an MCP session, the
  operator copies another build of `roca` over `~/.local/bin/roca` without
  running `install.sh`, `roca update` or `roca _install-bundled-plugins`, and
  then runs `roca doctor`
- **THEN** `roca doctor` exits 0 and reports a resident whose version is the
  one `roca --version` prints, or reports no resident running

### Requirement: roca doctor names the build and configuration of the resident

When `roca doctor` reaches a core resident, its resident line and its `--json`
resident object SHALL include the version and commit of that resident, the
path of the `config.toml` it was opened with, and the settings it answers
with: `vector`, whether it serves `roca_ops`, and the serving layout.

#### Scenario: Doctor with a resident alive

- **WHEN** on a home made by `roca init` with `vector = true`, whose core
  resident was started by an MCP session, the operator runs `roca doctor` and
  `roca doctor --json`
- **THEN** both exit 0, the resident line includes the version that `roca
  --version` prints, `vector true` and `serving legacy-serving`, and the JSON
  resident object holds that version, its commit, the path of `config.toml`,
  `vector: true`, `roca_ops: true` and `serving: "legacy-serving"`

#### Scenario: Doctor after the configuration changed

- **WHEN** on the same home, the operator sets `vector = false` in
  `config.toml` and runs `roca doctor --json`
- **THEN** it exits 0, and its JSON either has no resident object or a
  resident object with `vector: false`
