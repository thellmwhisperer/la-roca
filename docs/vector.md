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

`install` reads the kernel-generated `plugins/vector-registry.json` and embeds
every declared database in the background. Each database owns an adjacent
sidecar: `roca-corpus.db` owns `roca-corpus.vector.db`, `roca-ops.db` owns
`roca-ops.vector.db`, and third-party declarations follow the same rule.
Moving, copying, or uninstalling a database therefore carries or removes its
derived index at the same lifecycle boundary. Every sidecar records its owner,
embedding model, dimensions, build version, declaration fingerprint, and
source fingerprint.

Worker coordination remains under `~/.roca/plugins/roca-vector/state/`
(`%USERPROFILE%\.roca\plugins\roca-vector\state` on Windows). macOS and Linux
can send a desktop notification with the exit status and aggregate counts.
Windows sends no desktop notification: inspect `completion.json` or
`worker.log` in that state directory. The worker log path is printed at launch;
`completion.json` records `started_at`, `finished_at`, and `exit_status`. The
declared sidecars are ready only when `finished_at` is non-empty and
`exit_status` is `0`; otherwise deterministic FTS and SQL continue without
them. The timestamps time the first pass on this machine.

Indexing is incremental after that. `vector ingest` always requires `--delta`:

```sh
roca vector ingest --delta
```

A full delta sweeps every table and prose column declared by installed plugin
manifests. The bundled corpus declares session titles/projects, memory content,
human/agent exchanges, and thinking text; ops declares operational memory
content. Raw tool data, call telemetry, and other undeclared columns stay
FTS-only. Restrict a repair to a declared table with `--source <table>`.

The worker fingerprints each database (including its SQLite WAL) through
`pkg/incrementality` and skips the row sweep when both source and declaration
are unchanged. When a sweep is needed, existing chunk fingerprints decide
added, updated, and unchanged work; a desired-versus-stored fingerprint diff
garbage-collects rows and chunks whose source disappeared. Optional manifest
chunking hints override the kernel defaults without giving plugins executable
generation code.

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

A 353k-chunk corpus is about 10 hours at the M1-base rate and about
2.5 hours at the pro-laptop rate.

Windows has no published rate here. NVIDIA GPUs accelerate; a CPU-only
box can be slower than the M1. The first full pass **is** your
measurement for this machine: time that run and scale from it. A later
daily delta against an unchanged or lightly grown corpus is minutes.

As an order-of-magnitude reference, a production home with 353,663 chunks
measured 1.3 GB on disk after compaction. Expect roughly 1.3-1.5 GB per
~350k chunks per sidecar; the footprint varies with the source and embedding
model. Churn (many updates and deletes) leaves empty pages; reclaim every
installed sidecar explicitly:

```sh
roca vector compact
```

Ingest does not compact on its own.

## First query

```sh
roca vector query "what did we decide" 10
```

This command currently reads the corpus compatibility sidecar; cross-database
query fan-out is a separate serving change. Each hit prints rank, cosine score,
source table, source id, and a text preview. `k` is optional (default 10) and
capped at 100. Core FTS and SQL routing never depends on a model or sidecar.

## Verify the index

Re-run a full delta with no source database change. Healthy sidecars report a
null aggregate delta — `0 added · 0 updated · 0 removed` — and an unchanged
count equal to the live chunk count across declared databases. That is the
operator's own confidence probe; it needs no golden file.

Search craft for agents lives in the `roca-operations` skill. The
`roca-vector` skill owns index installation, progress, and maintenance.
