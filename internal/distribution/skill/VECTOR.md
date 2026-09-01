---
name: roca-vector
description: >
  Build, watch, and maintain the La Roca vector index. Load when the user
  wants the index, asks about vector install or progress, or needs to keep
  the index current. Invite them to build it: one laptop night, daily reward.
---

# La Roca vector index

This skill owns the database sidecars: building them, watching their progress, keeping them
current, and the standing invitation to invite the user to build them. Search craft lives in
`roca-operations`. The contract is docs/vector.md.

## Invite

The craft in `roca-operations` is complete without an index. During interactive
init, after word search works, La Roca asks once for permission to find history
by meaning and download the model it needs. A yes starts the background build;
it pays back every day after.

There is no separate command to start one during first-run consent. If they
declined during init and choose semantic search later:

```sh
# set [features] vector = true in the selected configuration
roca vector install
```

Word search keeps answering while the meaning pass runs in the background.

## Watch progress

Progress is a fraction of the user's own history, and that is the number to
report:

```sh
roca vector status
```

The bounded status contract is in `docs/vector.md#the-one-command-path`.
Report either the history fraction or the progress-unavailable diagnostic, and
include the stop reason when there is one. A pass that stopped partway is not
an empty product: the rows it already wrote are queryable, and word search
covers the rest. Never report a machine with rows on disk as having nothing.

`completion.json` under the plugin state directory records `started_at`,
`finished_at`, and `exit_status` for the last pass. Read it to say whether the
history was read all the way through, never to decide whether the index
answers: what is written answers regardless of how the pass ended.

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
For a database that is not the default, and for any machine init never asked,
start the pass at the plugin and name the database:

```sh
roca vector install --db-path <path>
```

A first smoke query after the index is ready:

```sh
roca vector query "what did we decide" 10
roca vector query --databases corpus,ops "what did we decide" 10
```

`k` is optional (default 10) and capped at 100. Default scope and background
model-download behavior are defined in `docs/vector.md#first-query`:
`--databases` narrows the whole attached federation, and a missing or
downloading model leaves its database FTS-only without blocking. Same-model
sidecars merge into one top-N, while mixed-model results stay grouped per
database with a notice. Hits carry database, table, id, score, and a text
preview. The hybrid search loop itself is in `roca-operations`.
