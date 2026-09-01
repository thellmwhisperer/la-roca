# Local vector search

Vector retrieval is opt-in and off by default. The release core carries its
matching `roca-vector` companion; the installation step extracts the companion
next to `roca`, and the switch enables semantic answering. It does not require
`features.plugins`. Windows remains an indexing seat through its compatibility
payload: the matching artefacts are `roca-<version>-windows-x64.exe` (core, carrying
`roca-vector.exe`) and
`roca-vector-vX.Y.Z-windows-x64.tar.gz` (standalone archive in the same
release).

## The one-command path

There is one command and one question. `roca init` stops at working full-text
search, and then, in that same run, asks whether to read the history for
meaning as well, saying plainly that word search already answers, that it
answers partially, and that a yes downloads what it needs.

A yes does everything the sections below describe by hand: it gets the local
embedding runtime going if this machine allows it, fetches the model if it is
missing, turns `features.vector` on in the configuration `roca doctor` names,
and starts the background pass. Word search keeps answering the whole time. If
the machine cannot embed, init reports that and prints one next step instead of
failing, and leaves `features.vector` off so nothing promises an index that is
not being built.

The question is asked only where an answer means something: a terminal with a
person reading it, a word search that has just hit, and a machine that is not
already indexing. A non-interactive run, a run with `--db-path`, and CI are
never asked; they stop at working full-text search, download nothing, and do
not rewrite an existing configuration. On those machines the pass starts at the
plugin instead, with `roca vector install`. There is no `--vectors` flag: a run
nobody answered has not consented to a download.

`roca vector status` reports one row per database declared in
`vector-registry.json`: plugin, database, declared tables, embedded chunks,
candidate chunks, sidecar size, last write, and an honest state (`building`,
`complete`, `empty`, `outdated`, or `unknown`). A number that cannot be read is
`unknown`, never `0`. One worker line says whether a pass is running, its pid,
backend (`cpu` or `metal`), and current database. Status does not wait for the
model and does not block on a large COUNT. Default output is bounded AXI;
`--json` is the complete envelope; `help[]` names the next command.

```sh
roca vector status
```

A pass that stopped partway leaves the rows it already wrote queryable; those
rows answer, and full text answers for the rest.

The rest of this document is the contract underneath, for operators who want
each step separately or who are on a machine the one command cannot serve.

## Windows install

[Install](lifecycle.md#install) owns the recommended Windows path and its
detection limitation. The native `.exe` procedure below is a manual, untested
fallback.

1. Download `roca-<version>-windows-x64.exe` from the release, put it in a
   permanent directory as `roca.exe`, and add that directory to the user
   `PATH`. This stable name is what the shipped skills and the commands below
   assume.
2. Open a new PowerShell window and have the core extract its carried companion
   into that same directory:

   ```powershell
   $RocaDir = Split-Path (Get-Command roca.exe).Source
   $env:ROCA_PREFIX = $RocaDir
   roca.exe _install-bundled-plugins
   Get-Command roca-vector.exe
   ```

The final command must resolve `roca-vector.exe` from `$RocaDir`.

No-rename PATH alternative: replace `<version>` below with the downloaded
release version and extract through that filename:

```powershell
$RocaDir = Split-Path (Get-Command roca-<version>-windows-x64.exe).Source
$env:ROCA_PREFIX = $RocaDir
roca-<version>-windows-x64.exe _install-bundled-plugins
```

The docs and shipped skills assume the stable `roca` / `roca.exe` name. A user
who keeps the versioned filename must substitute it for bare `roca` in every
later docs or skill command.

Turn it on in the configuration `roca doctor` names
(`~/.roca/config.toml`, or `%USERPROFILE%\.roca\config.toml` on Windows,
or `config.toml` next to a `--db-path` database):

```toml
[features]
vector = true
```

That exposes the semantic answering verbs and lists `vector` in `roca plugins`.
When the switch is absent or false, answering stays unavailable, but `roca
vector install` and `roca vector status` remain reachable whenever the
companion is installed so an operator can start, resume, or inspect the pass.
On Windows, keep `roca-vector.exe` beside `roca.exe` in the directory on `PATH`.

## The one download

On macOS and Linux, semantic search downloads one embedding model (~1 GB) into
the selected Roca data directory. That is the only extra download. There is no
second runtime, no daemon, and no extra command after you consent. The download
is size- and checksum-verified before it becomes active, then reused by later
indexing and queries. The pinned bytes are served from La Roca's `models-v1`
GitHub release, whose release lane verifies the upstream source before
publication.

The [init flow](lifecycle.md#initialize) owns first-run consent and ordering. If
semantic search is enabled there, no separate vector command is needed.
`roca vector install` performs the same download and build only when you turn it
on later.

macOS and Linux run the embedding engine inside the vector companion. On
macOS, live queries use hardware acceleration when available and fall back to
CPU if it cannot start. Indexing picks its backend by occasion: a full install
and `roca vector ingest --delta --reembed` are bulk builds and take the
accelerator by default, while an ordinary delta pass stays on CPU so it cannot
contend with live search. A background worker started by a query that finds the
model missing also counts as a bulk build and therefore accelerates by default.

Both `install` and `ingest` accept a tri-state `--accelerate` lever. Omitting it
keeps the occasion's default; `--accelerate` requests the accelerator, and
`--accelerate=false` forces CPU. `ROCA_VECTOR_WRITER_GPU=1` or `0` supplies the
same choice through the environment, including to a spawned worker, and an
explicit flag wins over the variable. Set `ROCA_VECTOR_WRITER_GPU=0` if a
query-triggered background build should remain on CPU.

Engine telemetry records the selected backend and decision in
`fallback_reason`: `operator requested accelerator`, `operator requested cpu`,
`bulk build default`, or `indexing leaves the accelerator for live search`.
An engine-level reason such as `accelerator init failed` takes precedence over
the policy reason. Each native engine instance serializes model opens and
embeddings. A caller waits no longer than ten minutes (or its earlier deadline).
Once native work begins, a timeout asks llama.cpp to abort. Reaching the internal
cap marks the native engine as trapped: a one-shot command reports `semantic
search stalled`, while a resident reports the terminal error before closing its
transport. A resumable background worker instead cancels and reaps its active
`roca exec` children, then restarts once for that exact embedding element. If the
same element traps again, the worker fails with its hashed element identity in
`worker.log` and
`completion.json` rather than restarting indefinitely. Linux uses CPU for both
paths. Windows keeps the previous local runtime path until its own native build
lane ships; see the release notes.

## Index declared databases

A yes to the question `roca init` asks starts this build. To start it
separately instead, which is also how a machine init never asked gets an index:

```sh
roca vector install
```

`install` reads the kernel-generated `vector-registry.json` under the plugin
root (`~/.roca/plugins/` by default, or `%USERPROFILE%\.roca\plugins\` on
Windows) and embeds every declared database in the background. Each database
owns an adjacent sidecar: `roca-corpus.db` owns `roca-corpus.vector.db`,
`roca-ops.db` owns `roca-ops.vector.db`, and third-party declarations follow
the same rule. Plugin update preserves that sidecar; uninstall archives or
removes it under the database's custody policy. Manual filesystem operations
must treat the pair together or discard the sidecar and regenerate it. Every
sidecar records its owner, embedding model, dimensions, build version,
declaration fingerprint, and source fingerprint.

When upgrading from the former central `state/vector.db`, the first worker pass
reuses compatible corpus and ops embeddings in their owned sidecars before it
runs the normal delta, including gaps in sidecars that already contain a
partial build. Fingerprint mismatches, changed chunking, and content that was
never present in the central index fall through to embedding; an unreadable or
incompatible legacy index is ignored and the ordinary build continues. The
central index is removed only after every declared sidecar has completed
successfully. Existing compatible indexes remain valid across the engine
upgrade and do not need a full re-embedding pass. An older generated vector
registry is accepted during upgrade and refreshed by core; it needs no manual
migration.

Worker coordination remains under `~/.roca/plugins/roca-vector/state/`
(`%USERPROFILE%\.roca\plugins\roca-vector\state` on Windows). macOS and Linux
can send a desktop notification with the exit status and aggregate counts.
Windows sends no desktop notification: inspect `completion.json` or
`worker.log` in that state directory. The worker log path is printed at launch;
`completion.json` records `started_at`, `finished_at`, and `exit_status`. That
record describes the worker run, not sidecar readiness. A sidecar is ready when
its identity, model, and dimensions match the current declaration. Rows already
written remain queryable whether the pass finished, failed, or stopped early;
deterministic FTS and SQL answer for the rest. The timestamps time the first
pass on this machine. During setup, download and indexing progress stream live
with completed counts, the current time range, and an ETA. Engine timings
(load, pre-warm, per-query embedding, throughput, backend and fallback, memory
high-water, and errors) are rotated, dated JSONL files at
`<data-directory>/logs/engine-YYYY-MM-DD.jsonl`. They contain no query or
document text, never enter a database table, and never leave the machine.

Indexing is incremental after that. `vector ingest` always requires `--delta`:

```sh
roca vector ingest --delta
roca vector ingest --delta --reembed
```

A full delta sweeps every table and prose column declared by installed plugin
manifests. The bundled corpus declares session titles/projects, memory content,
human/agent exchanges, and thinking text; ops declares operational memory
content. Raw tool data, call telemetry, and other undeclared columns stay
FTS-only. Restrict a repair to a declared table with `--source <table>`.

Generation policy: each declared text column is embedded on its own, so a short
human turn is not averaged into a long agent answer. Windows are about 250
tokens with about 100 tokens of overlap, and each embedding input gets a short
deterministic header from session title or project and year-month when those
columns exist. `--reembed` rebuilds a sidecar under that policy. It is
resumable: interrupting and running it again continues, and it does not
duplicate chunks. A partially rebuilt sidecar can contain multiple chunk
generations; queries search all of them while re-embedding continues. Progress
prints counts, rate, and ETA, newest rows first.

The worker fingerprints each database (including its SQLite WAL) through
`pkg/incrementality` and skips the row sweep when both source and declaration
are unchanged. When a sweep is needed, existing chunk fingerprints decide
added, updated, and unchanged work; a desired-versus-stored fingerprint diff
garbage-collects chunks and embeddings whose source disappeared. Optional
manifest chunking hints override the kernel defaults without giving plugins
executable generation code. Source sweeps are paged. Each page keeps its SQL
statement gate unbounded for large histories, but the `roca exec` child has a
two-minute process deadline; a timed-out child is terminated and reaped so the
worker can fail cleanly and a later pass can resume. Source counts use the
bounded exec path with an explicit 30-second statement timeout, independent
of the interactive query timeout; serving lookups keep the configured
interactive query budget.

Across declared databases, the scheduler waits one second to gather the newest
ready work, then embeds what is available rather than waiting for a silent
peer. Scanning, reconciliation, and embedding all count as progress. If none of
them progresses for 30 minutes, the worker exits with `indexing stalled
waiting for embedding work`; `worker.log` and `completion.json` retain the
failure.

For a non-default database:

```sh
roca vector --db-path /path/to/roca.db ingest --delta
```

`ROCA_READ_ONLY` refuses `install`, `ingest --delta`, and `compact`.

## Disk and maintenance

As an order-of-magnitude reference, a production home with 353,663 chunks
measured 1.3 GB on disk after compaction. Expect roughly 1.3-1.5 GB per
~350k chunks per sidecar; the footprint varies with the source and embedding
model. Per-column windows and overlap can raise live chunk count by roughly 2x
on a mixed conversation corpus, so measure the rebuilt sidecars before setting
a permanent disk budget.
Churn (many updates and deletes) leaves empty pages; reclaim every
installed sidecar explicitly:

```sh
roca vector compact
```

Ingest does not compact on its own.

## First query

```sh
roca vector query "what did we decide" 10
roca vector query --databases corpus,ops "what did we decide" 10
```

The default route searches every attached sidecar. `--databases` has the same
explicit comma-list and `all` selection rules as `roca query`; the command fans
out only to selected databases with vector declarations and ready sidecars.
Same-model scores merge into one top-N. If selected sidecars use different
models, results stay grouped per database with a notice because their scores
are not comparable. Every hit carries database, table, and source id. `k` is
optional (default 10) and capped at 100.

A missing sidecar or unavailable embedding model emits a notice and leaves that
database on its deterministic FTS/SQL route. A pending model download never
blocks a query: FTS answers immediately and the download continues in the
background until a later query can join the vector leg. Databases without a
vector declaration are still recognized routing targets and behave the same way.
Vector serving never invokes the answering model, and core search never
depends on a model or sidecar.

## Verify the index

Re-run a full delta with no source database change. Healthy sidecars report a
null aggregate delta — `0 added · 0 updated · 0 removed` — and an unchanged
count equal to the live chunk count across declared databases. That is the
operator's own confidence probe; it needs no golden file.

Search craft for agents lives in the `roca-operations` skill. The
`roca-vector` skill owns index installation, progress, and maintenance.
