# roca-vector

`roca-vector` is the optional executable plugin for local semantic retrieval.
Its implementation is a separate Go module and binary: core has no import of
that module, built-in vector command, or index dependency. The plugin reads corpus rows through `roca exec --json` and keeps only
embeddings, fingerprints, and stable source locators in its own
manifest-owned `state/` directory. Corpus text is resolved live from core
when a result is returned and is never copied into the index.

## Install from a release

Every release core carries its matching companion and the standard installer
materializes it beside `roca`; `features.plugins` is not required. Vector
retrieval remains off until `features.vector = true`. Follow [Local vector
search](../../docs/vector.md) for native Windows and Unix setup, the Ollama
prerequisite, indexing, and verification. Standalone verified archives remain
release artefacts for explicit plugin-package workflows.

## Build from source

Contributors can still exercise the package locally:

```sh
make -C plugins/vector check
make -C plugins/vector package
roca plugin install .tmp/vector-package
```

## Use

Ollama must be running locally. The default model is
`nomic-embed-text-v2-moe`; select another installed model with `--model`.

```sh
roca vector install
roca vector ingest --delta
roca vector ingest --delta --source sessions
roca vector compact
roca vector query "which decision kept inference local" 5
```

`install` is the plugin's adopt/init command: it pulls the model, prepares the
plugin-owned index, and starts a resumable background build. `ingest
--delta` embeds only new or changed chunks and removes missing sources. Both
writing commands and `compact` honor `ROCA_READ_ONLY`. `query` uses binary ANN
candidates, exact cosine reranking, stable source deduplication, and live text
resolution.

`compact` copies the existing float embeddings into a fresh paged store,
rebuilds their binary ANN representation without calling the model, verifies
chunk and source-kind counts plus database integrity, and atomically replaces
the old store. It reports embedding pages before and after, bytes reclaimed,
and the unchanged live chunk count. It refuses to run while a delta ingest
holds the index lock. Ingest does not compact automatically: reclaiming space
remains an explicit operator action, so no background maintenance policy is
enabled by default.

A full delta covers federated memories, exchanges, thinking blocks, and
sessions. Content-qualified source identities keep divergent rows that share a
natural locator separate, while repeated walks of the same row converge on one
indexed copy of each chunk. Use `--source` to restrict a repair to one of those
four source kinds (`memories`, `exchanges`, `thinking_blocks`, or `sessions`)
without removing indexed chunks from the others.

Session embedding input is built only from cleaned `sessions.title` and the
cleaned, string-valued `sessions.metadata.project_name`; it never reads
`sessions.project`. It excludes serialized metadata, JSON keys or fragments,
fingerprints, hashes, UUIDs, opaque identifiers, and paths while preserving
ordinary slash-bearing language such as `CI/CD` and `HTTP/2`. The session text
contract is fingerprint-versioned, so
`ingest --delta --source sessions` re-embeds the affected session chunks once,
reports added, updated, removed, and unchanged counts, and is a zero-write
delta when repeated against the same corpus.

For a non-default core database, export `ROCA_DB_PATH` or pass the plugin flag
after dispatch: `roca vector --db-path /path/to/roca.db query "..."`.

## Retrieval gate

The withdrawn laboratory implementation established the minimum useful bar
over five paired runs with `qwen3-embedding:4b`: hit@5 rose from 50.0% to 87.5%
and zero-result rate fell from 33.3% to 0.0%. The opt-in live test repeats a
synthetic vocabulary-gap suite against the configured local model:

```sh
ROCA_VECTOR_LIVE_EVAL=1 go test ./internal/vector -run TestLiveRetrievalQuality -v
```

On 2026-08-14 the isolated plugin scored hit@5 100.0% (8/8) with 0.0%
zero-result queries (0/8) using its default `nomic-embed-text-v2-moe` model,
above the laboratory bar.
