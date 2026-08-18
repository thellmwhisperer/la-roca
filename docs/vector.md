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
   permanent directory without renaming it, and add that directory to the user
   `PATH`.
2. Open a new PowerShell window and have the core extract its carried companion
   into that same directory:

   ```powershell
   $RocaDir = Split-Path (Get-Command roca-<version>-windows-x64.exe).Source
   $env:ROCA_PREFIX = $RocaDir
   roca-<version>-windows-x64.exe _install-bundled-plugins
   Get-Command roca-vector.exe
   ```

Replace `<version>` with the downloaded release version. The final command must
resolve `roca-vector.exe` from `$RocaDir`. Every native Windows command below
keeps using that versioned core filename; if it is deliberately renamed to
`roca.exe`, that shorter name can replace it throughout. WSL is an alternative:
install and operate the Linux artefact with the Unix commands inside the
distribution.

Turn it on in the configuration
`roca-<version>-windows-x64.exe doctor` names on native Windows (`roca doctor`
on macOS, Linux, or WSL)
(`~/.roca/config.toml`, or `%USERPROFILE%\.roca\config.toml` on Windows,
or `config.toml` next to a `--db-path` database):

```toml
[features]
vector = true
```

That exposes the `vector` command and lists `vector` in `plugins`. Absent or
false, the command does not exist. On Windows, keep `roca-vector.exe` beside the
versioned core executable in the directory on `PATH`.

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

## Index the corpus

Start the first build only after the model pull has finished. Command pairs
below show native Windows first and macOS, Linux, or WSL second:

```powershell
roca-<version>-windows-x64.exe vector install
```

```sh
roca vector install
```

`install` prepares the plugin-owned index under
`~/.roca/plugins/vector/state/` (`%USERPROFILE%\.roca\plugins\vector\state`
on Windows) and embeds the corpus in the background. macOS and Linux can send
a desktop notification with the exit status and counts. Windows sends no
desktop notification: inspect `completion.json` or `worker.log` in that state
directory. The worker log path is printed at launch; `completion.json` records
`started_at` and `finished_at`, which time the first pass on this machine.

Indexing is incremental after that. `vector ingest` always requires `--delta`:

```powershell
roca-<version>-windows-x64.exe vector ingest --delta
```

```sh
roca vector ingest --delta
```

A full delta embeds four families: `memories`, `exchanges`,
`thinking_blocks`, and `sessions`. Restrict a repair with
`--source memories|exchanges|thinking_blocks|sessions`. Session text is the
cleaned title plus cleaned `metadata.project_name` only.

For a non-default database:

```powershell
roca-<version>-windows-x64.exe vector --db-path C:\path\to\roca.db ingest --delta
```

```sh
roca vector --db-path /path/to/roca.db ingest --delta
```

`ROCA_READ_ONLY` refuses `install`, `ingest --delta`, and `compact`.

## Time and disk

Throughput depends on the machine. Measured Apple Silicon rates:

| Hardware | Chunks/min |
|---|---|
| M1 base | 576 |
| Pro laptop | 2.400–2.500 |

A 353k-chunk corpus is about 10 hours at the M1-base rate and about
2.5 hours at the pro-laptop rate.

Windows has no published rate here. NVIDIA GPUs accelerate; a CPU-only
box can be slower than the M1. The first full pass **is** your
measurement for this machine: time that run and scale from it. A later
daily delta against an unchanged or lightly grown corpus is minutes.

After compaction the index occupies about 1.3-1.5 GB per ~350k chunks.
Churn (many updates and deletes) leaves empty pages; reclaim them
explicitly:

```powershell
roca-<version>-windows-x64.exe vector compact
```

```sh
roca vector compact
```

Ingest does not compact on its own.

## First query

```powershell
roca-<version>-windows-x64.exe vector query "what did we decide" 10
```

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
section (`roca-<version>-windows-x64.exe skill install` on native Windows;
`roca skill install` on macOS, Linux, or WSL).
