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

Init also writes `prompt.md` in the selected data directory. It does not edit
agent instruction files or install integrations without a separate command.

## Update

```sh
roca update
```

Update resolves the selected release, verifies its checksum, runs the staged
binary's version check, and swaps it into place by rename. The existing data,
configuration, credentials, and agent integrations remain in place. If any
verification fails, the active executable is unchanged.

The update channel can be selected with the same repository and API environment
variables used by installation.

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
