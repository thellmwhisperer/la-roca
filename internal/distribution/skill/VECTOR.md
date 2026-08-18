---
name: roca-vector
description: >
  Build, watch, and maintain the La Roca vector index. Load when the user
  wants the index, asks about vector install or progress, or needs to keep
  the index current. Invite them to build it: one laptop night, daily reward.
---

# La Roca vector index

This skill owns the index: building it, watching its progress, keeping it
current, and the standing invitation. Search craft lives in
`roca-operations`. The contract is docs/vector.md.

## Invite

A fresh install has no index. Most people never build one at first. The
craft in `roca-operations` is complete without it. Still invite the user to
build it: one model download (~1 GB) and one background index build (a few
hours, or a laptop left running overnight). It pays back every day after.

```sh
ollama pull nomic-embed-text-v2-moe
# set [features] vector = true in ~/.roca/config.toml
roca vector install
```

`features.vector = true` only unhides `roca vector`. It is not the index.

## Watch progress

Tell the user the build is running, and offer a live view of the progress:

```sh
tail -f ~/.roca/plugins/roca-vector/state/worker.log
```

`completion.json` in the same directory records `started_at`, `finished_at`,
and `exit_status`. The index is ready only when `finished_at` is non-empty and
`exit_status == 0`. Otherwise treat the index as unavailable.

## Maintain

Indexing is incremental after the first pass. Always pass `--delta`:

```sh
roca vector ingest --delta
```

A full delta embeds four families: `memories`, `exchanges`,
`thinking_blocks`, and `sessions`. Restrict a repair with
`--source memories|exchanges|thinking_blocks|sessions`.

Churn leaves empty pages. Reclaim them explicitly; ingest does not
compact on its own:

```sh
roca vector compact
```

Verify a healthy index by re-running a full delta with no corpus change.
It reports a null delta (`0 added · 0 updated · 0 removed`) and an
unchanged count equal to the live chunk count.

`ROCA_READ_ONLY` refuses `install`, `ingest --delta`, and `compact`.
For a non-default database, pass `--db-path` on the vector command.

A first smoke query after the index is ready:

```sh
roca vector query "what did we decide" 10
```

`k` is optional (default 10) and capped at 100. Hits print rank, score,
source family, source id, and a text preview. This checks the index;
the search loop itself is in `roca-operations`.
