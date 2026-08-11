# Install, update, and uninstall

La Roca ships as one static executable. Installation copies it onto the PATH,
update replaces it after verification, and uninstall removes the executable and
integrations. Data is deleted only with explicit purge consent.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/thellmwhisperer/la-roca/main/install.sh | sh
```

The installer resolves a release artefact, downloads `checksums.txt`, verifies
the SHA-256 digest, and only then replaces the target by rename. It refuses to
overwrite an unrelated executable and converges safely after an interrupted
run.

| Flag | Default |
|---|---|
| `--repo owner/name` | `thellmwhisperer/la-roca`, or `ROCA_REPO` |
| `--version vX.Y.Z` | Latest published release |
| `--prefix DIR` | `~/.local/bin`, or `ROCA_PREFIX` |
| `--api URL` | `https://api.github.com`, or `ROCA_GITHUB_API` |
| `--force` | Reinstall even when that version is already active |

Windows is not installed by the shell script. Download the `.exe` release
artefact and place it on the PATH. Release artefacts are:

- `roca-<version>-darwin-arm64`
- `roca-<version>-linux-x64`
- `roca-<version>-linux-arm64`
- `roca-<version>-windows-x64.exe`

## Initialize

Run `roca init` after installation. With no home database it asks for one of
two explicit choices:

- `new` creates an empty database and ingests detected local sources.
- `adopt` asks for an existing database path, copies it into the home data
  directory, and leaves the source untouched.

If the home database already exists, init asks whether to keep or reinitialize
it. Non-interactive automation must select a location with `--db-path`; init
never guesses. Existing tables outside the current schema are reported and left
intact.

In a terminal, database selection flows directly into a model-first chooser:

1. Init lists the default model for every detected supported agent CLI and the
   models returned by Ollama's local catalogue, then asks which model should
   answer. A CLI without an enumerable catalogue contributes its shipped
   default and accepts a free-text model ID. Plain Enter keeps the provider and
   model the existing selection rules would use.
2. Init resolves the harnesses that can serve that model. One candidate is
   selected and named automatically; several candidates produce one short
   harness question.
3. Init confirms the provider/model pair, probes a changed choice, and writes
   the provider entry, model, and order to the configuration with a surgical
   edit. An existing configuration file gets a named `.roca.bak` recovery copy
   when the edit changes it.

A normal fresh init asks for the database, model, and confirmation. An ambiguous
harness adds one question; adoption separately asks for its source path. It uses
the agent CLI's existing session and does not add a login step.

Init also writes `prompt.md` in the selected data directory. If that optional
write fails, init reports a warning and leaves the prepared database usable. It
does not edit agent instruction files or install integrations without a
separate command.

A successful human-readable init reports the corpus floor: the oldest ingested
moment, the bedrock your memory reaches back to. An empty database says so
plainly instead of printing a zero date. It ends with an `answering:` line that
names the active provider/model, the exact configuration path, and the command
or key that changes it. `roca doctor` reports the same floor as part of
installation health, and `--json` carries the machine fields in both commands.

Before asking for any provider setup, init detects supported agent CLIs already
on `PATH` and uses their existing signed-in sessions. Its summary names the
selected factory-default provider and says that no La Roca login is required;
the next step is simply `roca query "<question>"`. Ollama and then keyword
search remain the honest local fallbacks when no detected CLI can serve.
HTTP/OAuth or key login is optional setup only for users without a usable local
CLI who choose a configured remote provider.

With `--db-path` on non-terminal input, init keeps that zero-login factory
selection without opening the chooser or writing model configuration. Human
output emits one `answering:` notice with the chosen provider/model and
configuration path; scripts receive no prompts. `--json` remains one JSON
document.

## Update

```sh
roca update
```

Update resolves the selected release, verifies its checksum, runs the staged
binary's version check, and swaps it into place by rename. The existing data,
configuration, credentials, and agent integrations remain in place. If any
verification fails, the active executable is unchanged.

After the swap, update reports how many new capability proposals are open. On
the first eligible command run with each new version, La Roca offers every open
proposal once for that version. Init reserves its short question budget for the
database and model chooser, so proposals wait for the next command. In a
terminal La Roca asks before each proposal change; an accepted change edits only
the declared TOML values, preserves unrelated content, and creates the same
named recovery backup as `roca login`. A rejection changes no configuration.
Without a terminal, each proposal is one plain alert: La Roca does not prompt
or edit the configuration.

`roca doctor` always lists proposals that remain open, even after they were
already offered for the current version. An interactive doctor run offers them
again; `--json` reports them under `capability_proposals` without prompting.
The current proposals are:

- When an explicit provider order excludes a detected Claude Code binary,
  offer the shipped local-CLI preset at the front of that configured order;
  its default model is `sonnet`. An absent order already detects it as the
  factory default and needs no proposal.
- When Codex is using La Roca's OAuth/HTTP session and the Codex CLI is on
  `PATH`, replace that provider's HTTP settings with the isolated `codex exec`
  local-binary transport while preserving the selected model.
- When Anthropic export ingest is available but
  `defaults.anthropic_export_paths` is empty, ask for an extracted export
  directory and add that typed path. See [Ingest sources](ingest.md#declare-an-anthropic-data-export).

The update channel can be selected with the same repository and API environment
variables used by installation.

`--api` and `ROCA_GITHUB_API` exist only for tests and trusted GitHub-compatible
mirrors. A custom base must be HTTPS, contain no credentials, query or fragment,
and is combined only with an `owner/name` repository path. Setting either
redirects release metadata and downloads, so do not point it at an origin you
do not trust. A trusted mirror with a private CA can use the standard
`SSL_CERT_FILE` environment variable.

## Uninstall

```sh
roca uninstall
roca uninstall --keep-data
roca uninstall --purge
```

Without an explicit data flag, uninstall asks for consent in an interactive
terminal. `--keep-data` removes the executable and integrations while retaining
the data directory. `--purge` removes every artefact La Roca created, including
the database, configuration, credentials, indexes, logs, generated prompt,
backups, skills, and integration recovery copies.

Uninstall edits each supported agent configuration surgically, preserving all
unrelated bytes. It refuses to delete files it cannot identify as product-owned
and reports them with a reason. Re-running uninstall is safe.

## Release ownership

Release artefacts are built by `.github/workflows/release.yml`; `make dist`
produces the same platform names locally. The release publishes binaries and
uploads `checksums.txt` after all targets have completed.
