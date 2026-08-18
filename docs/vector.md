# Local vector search

Vector retrieval is opt-in and off by default. The release already places
the `roca-vector` companion next to `roca`; the switch only unhides
dispatch. It does not require `features.plugins`.

Turn it on in the configuration `roca doctor` names (`~/.roca/config.toml`,
or `config.toml` next to a `--db-path` database):

```toml
[features]
vector = true
```

That exposes `roca vector` and lists `vector` in `roca plugins`. Absent or
false, the command does not exist.

## Prerequisites

Ollama must be running locally (default `http://127.0.0.1:11434`).

The first index build downloads the embedding model
`nomic-embed-text-v2-moe` (~957 MB) from Ollama. That pull happens once, on
first use, before any chunks are embedded. Later delta ingest and queries
reuse the local copy.

## Index the corpus

The download above starts when you run the first-build command. Start that
build only after you have read the size:

```sh
roca vector install
```

`install` pulls the model if needed, prepares the plugin-owned index under
`~/.roca/plugins/vector/state/`, and embeds the corpus in the background. A
desktop notification reports exit status and counts; the worker log path is
printed at launch.

Indexing is incremental after that. `roca vector ingest` always requires
`--delta`:

```sh
roca vector ingest --delta
```

A full delta embeds four families: `memories`, `exchanges`,
`thinking_blocks`, and `sessions`. Restrict a repair with
`--source memories|exchanges|thinking_blocks|sessions`. Session text is the
cleaned title plus cleaned `metadata.project_name` only.

For a non-default database:

```sh
roca vector --db-path /path/to/roca.db ingest --delta
```

`ROCA_READ_ONLY` refuses `install`, `ingest --delta`, and `compact`.

## Time and disk

Measured on Apple Silicon, expect about 2.400-2.500 chunks/min. A
full-corpus example of 353k chunks is about 2.5 hours. A later delta
against an unchanged or lightly grown corpus is minutes.

After compaction the index occupies about 1.3-1.5 GB per ~350k chunks.
Churn (many updates and deletes) leaves empty pages; reclaim them
explicitly:

```sh
roca vector compact
```

Ingest does not compact on its own.

## First query

```sh
roca vector query "what did we decide" 10
```

Each hit prints rank, cosine score, source family, source id, and a text
preview. `k` is optional (default 10) and capped at 100.

## Verify the index

Re-run a full delta with no corpus change. A healthy index reports a null
delta — `0 added · 0 updated · 0 removed` — and an unchanged count equal to
the live chunk count. That is the operator's own confidence probe; it needs
no golden file.

Search craft for agents lives in the shipped skill's Hybrid discovery
section (`roca skill install`).
