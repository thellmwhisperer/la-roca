# Local vector search

Vector retrieval is opt-in and off by default. The release core carries its
matching `roca-vector` companion; the installation step extracts the companion
next to `roca`, and the switch only unhides dispatch. It does not require
`features.plugins`. Windows is a first-class indexing seat: the matching
artefacts are `roca-<version>-windows-x64.exe` (core, carrying
`roca-vector.exe`) and
`roca-vector-vX.Y.Z-windows-x64.tar.gz` (standalone archive in the same
release).

## Windows install

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
later docs or skill command. WSL is an alternative: install and operate the
Linux artefact with the Unix commands inside the distribution.

Turn it on in the configuration `roca doctor` names
(`~/.roca/config.toml`, or `%USERPROFILE%\.roca\config.toml` on Windows,
or `config.toml` next to a `--db-path` database):

```toml
[features]
vector = true
```

That exposes `roca vector` and lists `vector` in `roca plugins`. Absent or
false, the command does not exist. On Windows, keep `roca-vector.exe` beside
`roca.exe` in the directory on `PATH`.

## Prerequisites

Ollama must be running locally (default `http://127.0.0.1:11434`).

The embedding model is `nomic-embed-text-v2-moe` (~957 MB). Pull it
**before** the first index build. The download happens once; later delta
ingest and queries reuse the local copy. The `vector install` command will
pull the model if it is missing — do the explicit pull first so the size
is not a surprise inside a background worker:

```
ollama pull nomic-embed-text-v2-moe
```

### Ollama on Windows

1. Install Ollama from https://ollama.com/download (Windows installer
   `OllamaSetup.exe`). Official notes: https://docs.ollama.com/windows.
   The installer does not require Administrator rights. After it
   finishes, Ollama stays in the system tray and serves
   `http://127.0.0.1:11434`.
2. Open a **new** terminal (`cmd` or PowerShell) and confirm with
   `ollama --version`.
3. Pull the embedding model above (~957 MB, one-time) before
   the `vector install` step below.

NVIDIA GPUs accelerate this model. On a machine with no GPU, Ollama
runs on CPU.

### Ollama on macOS and Linux

Install Ollama from https://ollama.com/download, start the daemon, then
pull the same model before the first index build.

## Index declared databases

Start the first build only after the model pull has finished:

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
successfully.

Worker coordination remains under `~/.roca/plugins/roca-vector/state/`
(`%USERPROFILE%\.roca\plugins\roca-vector\state` on Windows). macOS and Linux
can send a desktop notification with the exit status and aggregate counts.
Windows sends no desktop notification: inspect `completion.json` or
`worker.log` in that state directory. The worker log path is printed at launch;
`completion.json` records `started_at`, `finished_at`, and `exit_status`. The
completion record describes that worker run; query readiness is checked from
each selected sidecar's owner, model, and dimensions metadata. Sidecars that
are not ready leave their databases on deterministic FTS and SQL. The
timestamps time the first pass on this machine.

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
duplicate chunks. Progress prints counts, rate, and ETA, newest rows first.

The worker fingerprints each database (including its SQLite WAL) through
`pkg/incrementality` and skips the row sweep when both source and declaration
are unchanged. When a sweep is needed, existing chunk fingerprints decide
added, updated, and unchanged work; a desired-versus-stored fingerprint diff
garbage-collects chunks and embeddings whose source disappeared. Optional
manifest chunking hints override the kernel defaults without giving plugins
executable generation code.

For a non-default database:

```sh
roca vector --db-path /path/to/roca.db ingest --delta
```

`ROCA_READ_ONLY` refuses `install`, `ingest --delta`, and `compact`.

## Time and disk

Throughput depends on the machine. Measured Apple Silicon rates:

| Hardware | Chunks/min |
|---|---|
| M1 base | 576 |
| Pro laptop | 2,400–2,500 |

A bounded 24-row lab rebuild with the current embedding engine measured 24
chunks before the policy change and 55 after it: 2.29x as many chunks at about
30 chunks/s. Use that 2.29x result as the measured planning sample, not a
universal multiplier; source shape and declared columns determine the actual
growth.

A 353k-chunk corpus is about 10 hours at the M1-base rate and about
2.5 hours at the pro-laptop rate.

Windows has no published rate here. NVIDIA GPUs accelerate; a CPU-only
box can be slower than the M1. The first full pass **is** your
measurement for this machine: time that run and scale from it. A later
daily delta against an unchanged or lightly grown corpus is minutes.

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

The default route searches the corpus sidecar. `--databases` has the same
explicit comma-list and `all` selection rules as `roca query`; the command fans
out only to selected databases with vector declarations and ready sidecars.
Same-model scores merge into one top-N. If selected sidecars use different
models, results stay grouped per database with a notice because their scores
are not comparable. Every hit carries database, table, and source id. `k` is
optional (default 10) and capped at 100.

A missing sidecar or unavailable embedding model emits a notice and leaves that
database on its deterministic FTS/SQL route. Databases without a vector
declaration are still recognized routing targets and behave the same way.
Vector serving never invokes the answering model, and core search never
depends on a model or sidecar.

## Verify the index

Re-run a full delta with no source database change. Healthy sidecars report a
null aggregate delta — `0 added · 0 updated · 0 removed` — and an unchanged
count equal to the live chunk count across declared databases. That is the
operator's own confidence probe; it needs no golden file.

Search craft for agents lives in the `roca-operations` skill. The
`roca-vector` skill owns index installation, progress, and maintenance.
