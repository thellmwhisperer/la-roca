# roca-vector

`roca-vector` is the optional executable plugin for local semantic retrieval.
Its implementation is a separate Go module and binary: core has no import of
that module, built-in vector command, or index dependency. The plugin reads
corpus rows through `roca exec --json` and
keeps only embeddings, fingerprints, stable source locators, and aggregate
token document frequencies in its own manifest-owned `state/` directory.
Corpus text is resolved live from core when a result is returned and is never
copied into the index.

## Install from a release

The plugin lifecycle is experimental and OFF by default. Set
`features.plugins = true` in the selected La Roca `config.toml`. Releases carry
one verified archive per core platform. Download the archive for the installed
release to a stable local path, verify its release checksum, and install it:

```sh
VERSION=$(roca --version | awk 'NR == 1 {print $2}')
PLATFORM=darwin-arm64 # or linux-arm64, linux-x64, windows-x64
ASSET=roca-vector-$VERSION-$PLATFORM.tar.gz
DOWNLOAD=$(mktemp -d)
CACHE=$HOME/.cache/roca

gh release download "$VERSION" --repo thellmwhisperer/la-roca \
  --pattern "$ASSET" --pattern checksums.txt --dir "$DOWNLOAD"
grep "  $ASSET$" "$DOWNLOAD/checksums.txt" |
  (cd "$DOWNLOAD" && shasum -a 256 --check)
mkdir -p "$CACHE"
mv "$DOWNLOAD/$ASSET" "$CACHE/roca-vector.tar.gz"
roca plugin install "$CACHE/roca-vector.tar.gz"
```

Installing this archive is an explicit full-trust consent event. Set
`features.vector = true` as well: absent or false hides `roca vector` dispatch
and its `roca plugins` entry even when the binary is on `PATH`. Installation
supplies the package; the switch activates it.

For the next release, repeat the download, verification, and `mv` with the new
`VERSION`, keeping the same cache path, then run:

```sh
roca plugin update vector
```

Update reopens the recorded archive path, verifies the package's inner
`checksums.txt`, replaces the released executable and manifest, and preserves
the manifest-owned `state/` index. No Go toolchain or local build is involved.

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
roca vector query "which decision kept inference local" 5
roca vector vocab salud
```

`install` is the plugin's adopt/init command: it pulls the model, prepares the
plugin-owned index, and starts a resumable background build. `ingest
--delta` embeds only new or changed chunks and removes missing sources. Both
writing commands honor `ROCA_READ_ONLY`. `query` uses binary ANN candidates,
exact cosine reranking, stable source deduplication, and live text resolution.

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

## Vocabulary discovery

`roca vector vocab CONCEPT` reports the discriminative vocabulary around a
concept with zero inference in the discovery path: the vector index nominates
the top-100 semantic hits among `exchanges` and `thinking_blocks`, terms are
tokenized with accent folding, and JSON-key terms, hexadecimal tokens, and
opaque numeric identifiers are excluded. Each remaining term is scored by the
smoothed log-odds of its document share in the discovery set against its share
in a global census. A term must occur in at least two hit documents and have
positive log-odds to survive, so high-volume workshop vocabulary (for example
`worktree`, `exchange`, `semantic`, `projects`) is penalized by the baseline
instead of dominating. Surviving terms are grouped into research avenues by
shared hit documents, in a fixed rank order that makes the report reproducible.

The census is rebuilt from the same corpus walk that maintains the index:
`install`, a full `ingest --delta`, or a targeted `memories`, `exchanges`, or
`thinking_blocks` delta refreshes it. A `sessions`-only delta leaves it
unchanged. The census covers memories, exchanges, and thinking blocks;
sessions are deliberately excluded because they cannot be
vocabulary-discovery hits. On an index installed before the census existed,
`vocab` reports the missing census until the next census-building delta ingest.

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
