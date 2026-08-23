---
name: roca-vector
description: >
  Build, watch, and maintain the La Roca vector index. Load when the user
  wants the index, asks about vector install or progress, or needs to keep
  the index current. Invite them to build it: one laptop night, daily reward.
---

# La Roca vector index

This skill owns the database sidecars: building them, watching their progress, keeping them
current, and the standing invitation. Search craft lives in
`roca-operations`. The contract is docs/vector.md.

## Invite

The craft in `roca-operations` is complete without an index. During interactive
init, invite the user to say yes to semantic search. That remembered answer
starts the one model download (~1 GB) and newest-first background build; there
is no second setup command. It pays back every day after.

If they declined during init and choose semantic search later:

```sh
# set [features] vector = true in the selected configuration
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
roca vector ingest --delta --reembed
```

A full delta embeds every table and prose column declared in the generated
vector registry. The bundled corpus declares sessions, memories, exchanges,
and thinking blocks; ops declares operational memories. Restrict a repair with
`--source <declared-table>`. Use `--reembed` for the resumable rebuild flow;
generation policy, progress, and sizing details live in
`docs/vector.md#index-declared-databases`.

Churn leaves empty pages. Reclaim them explicitly; ingest does not
compact on its own:

```sh
roca vector compact
```

Verify healthy sidecars by re-running a full delta with no database change. It
reports a null aggregate delta (`0 added · 0 updated · 0 removed`) and an
unchanged count equal to the live chunk count across declared databases.

`ROCA_READ_ONLY` refuses `install`, `ingest --delta`, and `compact`.
For a non-default database, pass `--db-path` on the vector command.

A first smoke query after the index is ready:

```sh
roca vector query "what did we decide" 10
roca vector query --databases corpus,ops "what did we decide" 10
```

`k` is optional (default 10) and capped at 100. The default route searches the
corpus sidecar; `--databases` follows `roca query` selection. Same-model
sidecars merge into one top-N, while mixed-model results stay grouped per
database with a notice. Hits carry database, table, id, score, and a text
preview. A missing sidecar or unavailable model leaves that database FTS-only.
The hybrid search loop itself is in `roca-operations`.
