## Why

After `roca migrate` and `serving = "cutover"`, the memories a home held move
into the ops `memory_records` table, tagged with the database they came from
(`core`, `plugin:roca-corpus` or `plugin:roca-ops`; `memory_custody.go` in
`internal/distribution/rocaops`). The vector sidecars do not follow that
ownership:

- The corpus sidecar embeds the corpus `memories` table
  (`internal/distribution/rocacorpus/plugin.json`), so memories that came
  from the corpus are covered.
- The ops sidecar embeds only the ops `memories` table
  (`internal/distribution/rocaops/plugin.json`). Memories that came from
  `core`, the ones `roca store` wrote in a legacy home without `roca_ops`,
  now live only in `memory_records`, and no sidecar embeds them.
- A sidecar with nothing of its own to embed stays `outdated`, and
  `roca doctor` keeps offering `roca vector install`, which changes nothing.

Each memory must be embedded once, by the plugin that owns it after the
migration: corpus memories by the corpus sidecar, core and ops memories by the
ops sidecar. Pointing the ops sidecar at all of `memory_records` would embed
the corpus memories a second time and return them twice.

Who asked: #589.

Proof: macOS arm64, `roca v1.94.1 (97d0afe)`, a migrated home
(`data2-memory-custody state=verified destination=memory_records`) with
`serving = "cutover"`:

```
$ roca exec "SELECT count(*) AS ops_memories FROM plugin_roca_ops.memories"
  0,"plugin:roca-ops"
$ roca vector status
  roca-ops,ops,memories,0,not counted,<bytes>,"<time>",outdated,unheld,false
```

## What Changes

- In cutover, the ops sidecar also embeds the memories whose migration source
  is `core`, read from `memory_records` through the ops `core_memories` view,
  declared only while the home serves in cutover; memories whose source is the
  corpus stay with the corpus sidecar only. Each `roca vector` command
  regenerates the vector registry first, so a serving edit takes effect.
- A sidecar whose tables hold nothing to embed reports a finished state, and
  `roca doctor` offers no `roca vector install` for it.

## Capabilities

### New Capabilities

- `vector-memory-ownership`: which sidecar embeds each memory after the
  migration.

### Modified Capabilities

None.

## Impact

- Code: the ops vector declaration in `internal/distribution/rocaops/plugin.json`
  and the `core_memories` view in `internal/distribution/rocaops/schema.sql`,
  without a `SchemaVersion` bump: a bump reopens every verified custody
  migration, and upgraded homes receive the view because the bundled plugin
  re-applies `schema.sql` on every version change; the sidecar state in
  `plugins/vector` and its doctor remedy.
- End-to-end scenarios to write in `features/distribution/e2e-federation.feature`,
  run by `make e2e` against the installed candidate with the pinned embedding
  model: the scenarios of `specs/vector-memory-ownership/spec.md`, on a home
  installed with `install.sh` from the suite's local release channel into
  `~/.local/bin`. Both fail on v1.94.1.
- Acceptance scenarios run by `make accept`, as many as `make e2e-cover` needs
  so that every changed line runs end to end.
- Not touched: how `roca vector` and `roca query` find the binaries (#588);
  legacy serving; the corpus sidecar's own declaration; the hub `memories`
  compatibility view.
