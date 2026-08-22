# roca-vector

`roca-vector` is the optional executable plugin for local semantic retrieval.
Its implementation is a separate Go module and binary: core has no import of
that module, built-in vector command, or index dependency. The plugin reads
kernel-registered database surfaces through `roca exec --json` and keeps only
embeddings, fingerprints, and stable source locators in adjacent
database-owned sidecars. Source text is resolved live from core when a result
is returned and is never copied into a sidecar. The plugin-owned `state/`
directory holds only worker coordination, logs, and completion state.

## Install from a release

Data plugins can opt in as additional live sources with the repeatable
`--plugin` flag. The plugin name is resolved through La Roca's public
`roca exec` boundary (for example, `biblioteca-conocimiento` maps to the
validated `plugin_biblioteca_conocimiento` schema); `roca-vector` never opens a
data-plugin database directly. Their locators preserve material, chunk, topic,
channel, video, publication date, and opaque source reference metadata.

## Build and install

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

This package is intentionally installable rather than bundled. Installation is
an explicit full-trust consent event and an ordinary La Roca install or update
does not place the binary. Set `features.vector = true` as well: both switches
are required, with `features.plugins` as the master gate. If either is absent
or false, `roca vector` dispatch and its `roca plugins` entry stay hidden even
when the binary is on `PATH`. Installation supplies the package; the vector
switch activates it.
The corpus walk excludes deprecated `rocodata_*` memory layers. Historical
RocoData imports therefore cannot enter a new vector index; the canonical La
Roca corpus remains the only memory source used by semantic retrieval.
## Use

Ollama must be running locally. The default model is
`nomic-embed-text-v2-moe`; select another installed model with `--model`.

```sh
roca vector install
roca vector ingest --delta
roca vector ingest --delta --source memories
roca vector compact
roca vector ingest --delta --plugin biblioteca-conocimiento
roca vector query "which decision kept inference local" 5
roca vector query --plugin biblioteca-conocimiento --topic salud \
  "hábitos saludables y salud mental" 10
```

`install` is the plugin's adopt/init command: it pulls the model, prepares one
sidecar per declared database, and starts a resumable background build.
`ingest --delta` embeds only new or changed chunks and removes missing sources.
Both writing commands and `compact` honor `ROCA_READ_ONLY`. `query` uses routed
database sidecars, binary ANN candidates, exact cosine reranking, stable source
deduplication, and live text resolution. Pass `--databases corpus,ops` or
`--databases all` with the same explicit routing semantics as `roca query`.
Same-model sidecars merge into one top-N; mixed-model sidecars stay grouped per
database with a notice. Missing sidecars or models remain FTS-only.

`compact` copies each existing sidecar's float embeddings into a fresh paged store,
rebuilds their binary ANN representation without calling the model, verifies
chunk and source-kind counts plus database integrity, and atomically replaces
the old store. It reports aggregate embedding pages before and after, bytes
reclaimed, database count, and the unchanged live chunk count. It refuses to
run while a delta ingest holds a sidecar lock. Ingest does not compact automatically: reclaiming space
remains an explicit operator action, so no background maintenance policy is
enabled by default.

A full delta covers every table and prose column in the generated vector
registry. The bundled corpus declares sessions, memories, exchanges, and
thinking blocks; ops declares operational memories. Raw telemetry and every
undeclared column stay out. Stable manifest ids keep row updates attached to
one source identity, while chunk fingerprints and `pkg/incrementality` make
unchanged passes model-free. Use `--source <declared-table>` to restrict a
repair without removing chunks from other tables. Per-table chunking hints are
part of the fingerprinted contract.

Data-plugin queries add stable material deduplication, metadata filters, and
live text resolution. Titles and topic/channel labels enrich the embedding
input while the stored fingerprint remains the exact transcript text; changed
live text is rejected as a stale candidate until the next delta ingestion. The
index also removes retired `rocodata_*` chunks from an existing index when
writing is enabled. Under `ROCA_READ_ONLY`, a query refuses an index that still has
such chunks; run `roca vector ingest --delta` with writes enabled to reconcile
it.

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
