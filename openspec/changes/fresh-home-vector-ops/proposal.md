## Why

On a new home whose `config.toml` says only `[features] vector = true` before
`roca init`, the first `roca vector install` never finishes. The command exits
0, but its background worker exits 1 on the ops source, every time, until the
operator runs `roca migrate` and switches serving to cutover. `roca vector
status` and `roca doctor` then tell the operator to run `roca vector install`,
which fails the same way.

- `roca init` keeps a configuration that is already there, so `roca_ops` stays
  absent, which reads as false, and serving stays `legacy-serving`. The same
  happens with `roca_ops = false` written by hand.
- With `roca_ops` off and legacy serving, the query route does not attach the
  ops database (`rocaOpsEnabled` in `internal/distribution/cli/cli.go`, and
  `openResidents` in `internal/provider/service/plugins.go`). `roca store`
  writes the memory to core.
- The vector registry is composed from every installed plugin
  (`discoverPluginContracts` in `internal/distribution/cli/skill.go`). It
  declares the ops `memories` table whether the route serves ops or not.
- The worker reads each declared table through the core's read gate. That gate
  knows only the attached databases, so it refuses `plugin_roca_ops.memories`
  and the worker exits 1.

A home made by `roca init` without a configuration gets `roca_ops = true` and
does not fail: on the same build its worker exits 0 and `roca vector query
--databases ops` finds the memory.

Who asked: `proposal`, for both requirements: the registry follows the served
route, and the stored memory stays findable on that home.

Proof: on integration 565bf65 (darwin-arm64) and on the published v1.94.1, in
an empty `HOME` with `[features] vector = true` written before init, after
`roca init --db-path ~/.roca/roca.db`, `roca store --layer discovery --content
"orchard compass note"` and `roca vector install` (exit 0), the worker log
ends with:

```
vector worker: exit 1 · 0 added · 0 updated · 0 removed · 0 chunks
  error: index vector source roca-ops/ops: count declared chunks roca-ops/ops/memories: roca reader: no such table: "plugin_roca_ops.memories" is not a table this query can read
```

and then:

```
$ roca vector status
  roca-ops,ops,memories,0,null,65536,"<time>",empty,unheld,false
  - Run `roca vector install` to start or resume embedding
$ roca doctor
  roca-ops/ops · 0 chunks · 65536 bytes · state empty · lock unheld
      remedy: Run `roca vector install` to start or resume embedding
$ roca vector query "orchard compass" 5 --databases ops
error: roca reader: unknown database "ops"; attached databases: core, corpus
$ roca query "orchard compass"
  1,core.memories.1,fts,1,orchard compass note
```

## What Changes

- The vector registry declares a plugin database only when the query route
  attaches it. For `roca-ops` that is the rule the route already uses:
  `roca_ops = true`, or serving other than `legacy-serving`, or the DATA SPLIT
  writer fence. A home outside that rule gets no ops sidecar to build, so the
  first pass reads only sources that exist and the worker exits 0.
- On that home, `roca vector status` and `roca doctor` no longer list an ops
  sidecar and ask for nothing. The memory `roca store` wrote lives in core and
  stays findable through `roca query`, by its full-text leg. Core has no vector
  sidecar before the migration; that does not change.
- When the home later serves ops, after `roca_ops = true` or `roca migrate` and
  cutover, the next `roca vector` command regenerates the registry, as it does
  today, and the ops sidecar is declared again.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `vector-memory-ownership`: adds what a home that does not serve ops declares
  for vector search before any migration, and where its stored memories are
  found.

## Impact

- Code: the composition of the vector registry in
  `internal/distribution/cli/skill.go`, reading the same rule as the
  `rocaOpsEnabled` value in `internal/distribution/cli/cli.go`, taken out into
  one function both use. No change in `plugins/vector`.
- Acceptance scenarios to write, run by `make accept`: in
  `features/store/vector-registry-serving.feature`, "A vector command on a home
  that does not serve ops declares no ops sidecar", with the stub companion. It
  fails on 565bf65 and passes with the fix.
- End-to-end scenario to write, run by `make e2e` with the pinned embedding
  model: in `features/distribution/e2e-federation.feature`, "a new home without
  roca_ops finishes its first vector pass before any migration". It fails on
  565bf65 and v1.94.1.
- Real sizes, from the reproduction: `roca.db` 176,128 bytes holding one
  memory, `roca-ops.db` 176,128 bytes holding none, `vector-registry.json`
  4,482 bytes, the ops sidecar the failed pass left 65,536 bytes as status counts it, with 0
  chunks.
  The registry declares two databases today and one after the fix on that home.
- Not to build: no core vector sidecar for `roca.db` before the migration, no
  automatic `roca_ops = true` on a configuration init did not write, no attach
  of ops into the route when the route does not serve it, no wait in `roca
  vector install` for the first pass, no deletion of an ops sidecar already on
  disk.
- Not touched: the semantic catalog skill, which reads the same plugin list;
  the cutover declaration of `core_memories`; the `roca vector install` exit
  code and its background worker; how `roca vector status` and `roca doctor`
  report a worker run that failed; the missing state folder of #571; the
  companion in `plugins/vector`.
