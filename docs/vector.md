# Optional vector search

`roca vector` adds semantic similarity search without changing the core
database or sending corpus text to a cloud service. It is off by default. The
command is built into the binary, not a [plugin package](plugins.md), and it
uses a local Ollama embedding model with an in-process SQLite vector index in a
separate `vector/vector.db` beside the selected core database. That store
contains embeddings, fingerprints, and stable source locators; it does not copy
corpus text.

## Install

```sh
roca vector install
```

Install returns after starting a background worker. The worker downloads
`nomic-embed-text-v2-moe` through local Ollama, indexes the existing corpus in
resumable batches, and sends a desktop notification with its exit status and
added, updated, removed, and total chunk counts. Progress and errors remain in
`vector/worker.log`; the same completion data is written to
`vector/completion.json`.

Running install again is safe. An active worker is reused, and an interrupted
index resumes from the chunk fingerprints already committed to the vector
database. Select a different local model with `--model`; changing the model
rebuilds embeddings instead of mixing vector spaces.

## Delta ride

```sh
roca vector ingest --delta
```

This is the command to run after core `roca ingest`. It waits for the core
`.roca.lock` and for ingest activity to settle. When a roca-cron journey
database is available and exposes a status column, the gate reads its active
ingest journeys; otherwise it uses the latest core ingest JSONL record. The wait
is bounded: after five minutes the command fails with the signal it was watching
instead of polling forever behind a crashed run. Set `ROCA_CRON_JOURNEY_DB` when
the journey database lives outside the conventional local paths.

Only new or changed chunks are embedded. Removed core sources are removed from
the vector store, and an identical rerun performs no embedding work.

## Query

```sh
roca vector query "the decision about local model routing"
roca vector query "the decision about local model routing" 20
```

The optional second argument is `k` (default 10, maximum 100). Queries use the
model's asymmetric `search_query:` prefix. An in-process sqlite-vec ANN pass
selects binary-quantized candidates, then reranks those candidates by exact
cosine distance before deduplicating by stable source ID. The final text is read
from the current core row, so results cannot return a stale copy held by the
vector store. `--json` returns rank, cosine score, source table, stable source
ID, and text for automation.

The four source families are `memories`, `exchanges`, `thinking_blocks`, and
`sessions`. Their IDs use natural ingest keys such as session ID, exchange
number, thinking position, and imported-file provenance; SQLite rowids are not
part of the cross-database contract. A source whose natural key the core left
unset, such as a thinking block that hangs off a session rather than an exchange
or a memory stored without a sequence, is keyed and resolved by its own content
digest instead, so those rows stay distinct and still map back.
