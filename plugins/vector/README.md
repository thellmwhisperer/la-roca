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
does not place the binary.

## Use

Ollama must be running locally. The default model is
`nomic-embed-text-v2-moe`; select another installed model with `--model`.

```sh
roca vector install
roca vector ingest --delta
roca vector query "which decision kept inference local" 5
```

`install` pulls the model and starts a resumable background build. `ingest
--delta` embeds only new or changed chunks and removes missing sources. Both
writing commands honor `ROCA_READ_ONLY`. `query` uses binary ANN candidates,
exact cosine reranking, stable source deduplication, and live text resolution.

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
