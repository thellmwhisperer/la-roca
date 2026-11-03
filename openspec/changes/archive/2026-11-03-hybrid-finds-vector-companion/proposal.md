## Why

`roca vector query` answers while `roca query`, on the same home and in the same
shell, prints `notice: vector plugin is not installed; continuing with FTS-only`
and runs without its vector leg. This happens whenever the directory that holds
`roca-vector` is not on the shell's `PATH`. That is common right after
`install.sh`: the installer puts both binaries in `~/.local/bin`, and a shell
that started before the rc line was added calls `~/.local/bin/roca` by its full
path.

The two commands find the companion differently. `roca vector` resolves it with
`resolveCompanion` (`internal/distribution/cli/cli.go`), which looks in the
managed install directory first (`$ROCA_PREFIX`, else `~/.local/bin`) and then
in the directories on `PATH`. The hybrid leg of `roca query` calls
`PluginVectorQuery` (`internal/provider/service/hybrid.go`), which only tries
`exec.LookPath("roca-vector")` and gives up when `PATH` lacks it.

Who asked: proposal.

Proof: on v1.94.0 (81721cf), in a disposable `HOME` with `vector = true` and a
finished `roca vector install`, which left `roca-vector` in that home's
`~/.local/bin`, and with `PATH=/usr/bin:/bin`, calling `roca` by its absolute
path:

```
$ roca query "ponytail review skill"
search fts · engines fts
notice: vector plugin is not installed; continuing with FTS-only
...
$ roca vector query "ponytail review skill" 3 --databases corpus,ops
1. 0.534 · database=corpus · table=thinking_blocks · id=1
...
```

With `~/.local/bin` on `PATH`, the same `roca query` prints
`search hybrid · engines fts,vector`.

## What Changes

- The vector leg of `roca query` finds `roca-vector` the way `roca vector`
  does: the managed install directory first, then `PATH`.
- The `vector plugin is not installed` notice stays for a home where none of
  those places holds the companion.

## Capabilities

### New Capabilities

- `hybrid-vector-companion`: where `roca query` looks for the vector companion.

### Modified Capabilities

None.

## Impact

- Code: `PluginVectorQuery` and `PluginVectorSearch` in
  `internal/provider/service/hybrid.go`, and the place in
  `internal/distribution/cli` that builds the service options, so the companion
  path `resolveCompanion` finds reaches the hybrid leg.
- Acceptance scenarios to write, run by `make accept` against the real `roca`:
  the two scenarios of `specs/hybrid-vector-companion/spec.md`, with a stub
  `roca-vector` that answers `query` with one fixed hit, so no test loads the
  embedding model.
- Not touched: `roca vector` and `resolveCompanion` itself; `install.sh` and its
  `PATH` warning; the hit labels and fusion; the vector companion; the notices
  for databases without a vector declaration.
