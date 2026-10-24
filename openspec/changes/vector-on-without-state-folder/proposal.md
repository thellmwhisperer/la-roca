## Why

The documented way to turn semantic search on after `roca init` is to set
`vector = true` under `[features]` in `config.toml` and run `roca vector install`
(docs/vector.md, AGENTS.md). On a home initialized with vector search off, both
`roca vector query` and `roca vector install` fail with
`vector state moved from ~/.roca/plugins/roca-vector/state; rerun the command`,
and every rerun prints the same line. `roca vector status` meanwhile tells the
operator to run `roca vector install`, the command that fails.

Init with vector search off installs `roca-corpus` and `roca-ops` but no
`roca-vector` plugin folder, so the companion's state folder is absent. The
companion's state lock (`LockStateUsage` in
`plugins/vector/internal/vector/runtime.go`) reads a missing managed state folder
as "an update moved it while I waited", and asks for a rerun. Nothing on that
path creates the folder, so the rerun finds it missing again. `Launch` takes the
same lock before its own `MkdirAll`, so `roca vector install` loops too.

docs/vector.md already promises what should happen: a missing or unready sidecar
emits a notice and leaves that database on its FTS/SQL route, a pending model
download never blocks a query, and `roca vector install` starts the background
pass while word search keeps answering.

Who asked: proposal, from the chief of staff's report of 24-oct-2026; it
reproduced on integration 9ebda82 and again on d22ad62.

Proof: on integration d22ad62, in a disposable `HOME` with `roca` and
`roca-vector` in `~/bin`, after `roca init --json --db-path ~/.roca/roca.db`
(exit 0) and `vector = true` in `~/.roca/config.toml`:

```
$ roca vector query "what did we decide" 5
error: vector state moved from ~/.roca/plugins/roca-vector/state; rerun the command
exit=1
$ roca vector query "what did we decide" 5
error: vector state moved from ~/.roca/plugins/roca-vector/state; rerun the command
exit=1
$ roca vector status
...
  - Run `roca vector install` to start or resume embedding
exit=0
$ roca vector install
error: vector state moved from ~/.roca/plugins/roca-vector/state; rerun the command
exit=1
$ ls ~/.roca/plugins
roca-corpus  roca-ops  vector-registry.json
```

The same home after `roca _install-bundled-plugins` (which creates
`~/.roca/plugins/roca-vector/state`) answers the query with exit 0 and
`notice: database corpus has no ready vector sidecar; continuing with FTS-only`.

## What Changes

- A vector command that finds its state folder absent, after it holds the shared
  relocation lock and no relocation left the state elsewhere, creates the folder
  and goes on. `roca vector query` then answers with the documented notices and
  exit 0, and `roca vector install` starts the background pass.
- The "moved; rerun the command" error stays only for a state folder that a
  relocation really moved, where a rerun resolves the new place.

## Capabilities

### New Capabilities

- `vector-first-use`: what the vector commands do on a home where vector search
  was turned on after init and no index or state folder exists yet.

### Modified Capabilities

None.

## Impact

- Code: `LockStateUsage` and `Launch` in
  `plugins/vector/internal/vector/runtime.go` (create a missing managed state
  folder under the relocation lock instead of reporting a move); the vector
  command callers in `plugins/vector/cmd/roca-vector/main.go` only if they need the
  created path.
- Acceptance scenarios to write, in `features/distribution/vector.feature` (new)
  or `features/distribution/cli.feature`, run by `make accept` against the real
  `roca`: the three scenarios of `specs/vector-first-use/spec.md`, on a home made by
  `roca init` with vector search off, then `vector = true`, with the model download
  pointed at a local fixture so no test fetches the real model.
- Not touched: the relocation of a legacy `plugins/vector/state` folder and its
  exclusive lock; `ROCA_VECTOR_STATE_DIR` and `--state-dir` overrides; the shared
  resident; `roca vector status` output; the sidecar formats; init's vector
  question; the message for a vector call while `vector = false`.
