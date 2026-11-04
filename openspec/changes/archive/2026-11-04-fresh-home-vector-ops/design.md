## Context

See proposal.md, Why. Two readers decide what the ops database is, and they
disagree on a home that does not serve ops:

- The query route. `internal/distribution/cli/cli.go` computes
  `rocaOpsEnabled := file.Features.RocaOps || readLayout !=
  service.LayoutLegacyServing || writerFenced` when it opens the service.
  `openResidents` in `internal/provider/service/plugins.go` attaches
  `roca-ops` as a resident only when that value is true; otherwise ops is kept
  as the layer registry and is not attached. `memoryOwner` in
  `internal/provider/service/layer_registry.go` sends `roca store` to core.
- The vector registry. `discoverPluginContracts` in
  `internal/distribution/cli/skill.go` validates every installed plugin and
  applies only `ForServingLayout` (`internal/provider/plugin/plugin.go`),
  which drops `core_memories` outside cutover. It keeps the ops `memories`
  table on every home. `saveVectorRegistry` writes that list for the
  companion.

The companion reads each declared table through `roca _vector-reader`
(`internal/distribution/cli/commands.go`), which runs the same SQL gate as
`roca exec`. On that home the gate has no `plugin_roca_ops` and refuses the
count in `DeclaredCorpus.CountChunks`
(`plugins/vector/internal/vector/federation.go`). `Federation.Ingest` reports
that job's error and the worker exits 1. The corpus job of the same pass
succeeds and is sealed.

Since the change `vector-embeds-each-memory-once`, every `roca vector` command
regenerates an existing registry before the companion starts (`dispatchPlugin`
in `cli.go`), and every service open refreshes it too. So a registry composed
from the right rule is rewritten on the next command; the operator never
edits it.

The ops `memories` declaration has existed since the federated sidecars of
#220, and the `rocaOpsEnabled` rule since the federation hub of #144. The
change `vector-embeds-each-memory-once` added `core_memories` and the refresh
on every vector command; it did not create this disagreement.

## Goals / Non-Goals

**Goals:**

- One rule says whether ops is served, and both the query route and the
  vector registry read it.
- On a home that does not serve ops, the first pass reads only what exists and
  the operator sees nothing to repair.

**Non-Goals:**

- Waiting in `roca vector install` for the first pass, or giving it the
  worker's exit code. Install is documented as a background start
  (docs/vector.md, "Index declared databases"), and init's yes waits only for
  the pass to acknowledge that it started. With every declared source
  readable, this home has no failing first pass left. A failed background run
  that `roca vector status` and `roca doctor` do not report is a gap for every
  cause of failure, not for this one, and belongs in its own change.
- A vector sidecar for core before the migration. Core memories are embedded
  by the ops sidecar after `roca migrate` and cutover, through `core_memories`.
- Changing the semantic catalog skill, which reads the same plugin list and
  has the same gap. Its output is agent-facing and needs its own scenario.

## Decisions

### Filter the registry by the served route, not the route by the registry

The registry composition drops the `roca-ops` database when ops is not
served. The rule is taken out of `cli.go` into one function over the loaded
configuration and the ops database path: `roca_ops = true`, or serving other
than `legacy-serving`, or the DATA SPLIT writer fence. The service open and
the registry composition both call it. The service open computes it before
its own registry refresh, so the two cannot disagree within one command.

Alternatives considered:

- Attach ops read-only into the route whenever the plugin is installed. The
  worker would then find an empty `memories` table and exit 0, but
  `roca vector query --databases ops` would still say `unknown database`, the
  stored memory is in core and would still not be found by vector, and every
  agent query would see a database the operator switched off.
- Write `roca_ops = true` at init into a configuration init did not create.
  Init keeps an existing configuration byte for byte by contract
  (`TestTTYInitPreservesExistingConfigByteExact`), and `roca_ops = false` by
  hand must keep meaning off.
- Teach the companion to skip a declared source the reader refuses. The
  companion would hide a contract error the core can avoid, and status would
  still list a sidecar that can never fill.

### Where the stored memory is found

On this home the memory is in core, and core has no vector declaration before
the migration. `roca query` finds it by its full-text leg, and `roca vector
query` already says `database core has no vector declaration; continuing with
FTS-only`. The spec asserts both outputs, so the fix cannot leave the memory
unfindable. After `roca migrate` and cutover the scenario "589 a core memory
and a corpus memory after the migration", which starts from the same kind of
home, keeps proving that the ops sidecar embeds it.

### Acceptance in make accept, operator proof in make e2e

`make accept` runs no embedding model. The store scenario writes `roca_ops =
false` into an init-made configuration, runs `roca vector status` against the
stub companion, and reads `vector-registry.json`. It reuses the steps of
`features/store/vector-registry-serving.feature` and adds two: writing
`roca_ops = false` under `[features]`, and "the vector registry declares no
ops sidecar". On 565bf65 the registry declares `roca-ops` with `memories`, so
it fails.

The operator outcome, a worker that exits 0 and a doctor that asks nothing,
needs the real companion and the pinned model, so it runs in
`features/distribution/e2e-federation.feature` under `make e2e`, with the
existing step "a home made by roca init with vector = true, over an agent
session that carries a memory mentioning ..." and without the migration step.
In that harness `roca vector install` returns when the worker closes the
progress descriptor it inherits, so the next step reads a finished pass.

## Risks / Trade-offs

- [A home that turns `roca_ops` off after building an ops sidecar keeps that
  sidecar file on disk, undeclared] → It is left as it is, and it is declared
  and refreshed again when ops is served. No deletion is built.
- [The writer-fence check is one more read-only open of `roca-ops.db` per
  registry refresh] → The service open already reads it; the function is
  called once per open and its answer passed to the refresh.
- [A third-party plugin whose database the route does not attach has the same
  disagreement] → Only bundled `roca-ops` is self-gated today; other resident
  plugins are attached when `plugins = true`, and their gate is out of scope.

## Migration Plan

No data changes. The next `roca` command on an affected home rewrites
`vector-registry.json` without the ops database; the next `roca vector
install` then finishes. Rollback is reverting the merge; the registry is
regenerated by the older build on its next command.
