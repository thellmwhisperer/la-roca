# roca-vector

`roca-vector` is the optional executable plugin for local semantic retrieval.
It is a separate Go module and binary: core has no import, built-in command, or
index dependency. The plugin reads corpus rows through `roca exec --json` and
keeps only embeddings, fingerprints, and stable source locators in its own
manifest-owned `state/` directory. Corpus text is resolved live from core when
a result is returned and is never copied into the index.

## Build and install

The plugin lifecycle is experimental and OFF by default. Set
`features.plugins = true` in the selected La Roca `config.toml`, then:

```sh
make -C plugins/vector check
make -C plugins/vector package
roca plugin install .tmp/vector-package
```

This package is intentionally installable rather than bundled. Installation is
an explicit full-trust consent event and an ordinary La Roca install or update
does not place the binary. Set `features.vector = true` as well: absent or false
hides `roca vector` dispatch and its `roca plugins` entry even when the binary
is on `PATH`. Installation supplies the package; the switch activates it.

## Use

Ollama must be running locally. The default model is
`nomic-embed-text-v2-moe`; select another installed model with `--model`.

```sh
roca vector install
roca vector ingest --delta
roca vector ingest --delta --source sessions
roca vector query "which decision kept inference local" 5
roca vector vocab salud
```

`install` is the plugin's adopt/init command: it pulls the model, prepares the
plugin-owned index, and starts a resumable background build. `ingest
--delta` embeds only new or changed chunks and removes missing sources. Both
writing commands honor `ROCA_READ_ONLY`. `query` uses binary ANN candidates,
exact cosine reranking, stable source deduplication, and live text resolution.

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

## Vocabulary discovery

`roca vector vocab CONCEPT` reports the discriminative vocabulary around a
concept with zero inference in the discovery path: the vector index nominates
the top-100 semantic hits among `exchanges` and `thinking_blocks`, terms are
tokenized with accent folding, and each term is scored by the smoothed
log-odds of its document share in the discovery set against its share in a
global census. Only terms the discovery set concentrates survive, so
high-volume workshop vocabulary (for example `worktree`,
`exchange`, `semantic`, `projects`) is penalized by the baseline instead of
dominating. Surviving terms are grouped into research avenues by shared hit
documents, in a fixed rank order that makes the report reproducible.

The census is rebuilt from the same corpus walk that maintains the index:
any `install` or `ingest --delta` refreshes it, and it covers the content
kinds with first-class text (memories, exchanges, thinking blocks; the session
projection carries serialized metadata and is deliberately not a baseline).
On an index installed before the census existed, `vocab` reports the missing
census until the next delta ingest builds it.

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
