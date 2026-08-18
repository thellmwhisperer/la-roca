# Install, update, and uninstall

La Roca ships as a static core executable carrying its platform-matched vector
companion. Installation puts both in the selected executable directory, update
refreshes them after verification, and uninstall removes the executables and
integrations. Data is deleted only with explicit purge consent.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/thellmwhisperer/la-roca/main/install.sh | sh
```

The installer resolves a release artefact, downloads `checksums.txt`, verifies
the SHA-256 digest, and only then replaces the target by rename. It refuses to
overwrite an unrelated executable and converges safely after an interrupted
run.

The installed core materializes `roca-vector` in the same directory, including
when `--prefix` or `ROCA_PREFIX` selects a custom one. Its manifest and dormant
`state/` directory live under `~/.roca/plugins/vector/`; dispatch remains hidden
unless the existing `features.vector` switch is true. See
[Local vector search](vector.md) for the operator path from that switch to a
first query. Installation refuses to
replace an externally sourced plugin package, an unmanaged plugin directory, or
an executable it does not own, and reports the collision instead.

The core also places its bundled data plugins under `~/.roca/plugins/`. Each
starts as an empty SQLite database, is verified and recorded like any other
[installed package](plugins.md#verified-packages-and-lifecycle), and is removed
by a purge with the rest of that tree. It ships the resident
[`roca-ops`](plugins.md#the-bundled-roca-ops-plugin) store, the resident
[`roca-corpus`](plugins.md#the-bundled-roca-corpus-plugin) archive and the
custodial [`roca-cron`](plugins.md#scheduled-rides) journey store. If they cannot be
placed, the installer puts the previous binary back and reports the reason, so
no partial update is left behind; when even that restore fails it names the copy
it kept for the operator to move back by hand. A `--version` older than bundled
plugins reports that none were placed.

| Flag | Default |
|---|---|
| `--repo owner/name` | `thellmwhisperer/la-roca`, or `ROCA_REPO` |
| `--version vX.Y.Z` | Latest published release |
| `--prefix DIR` | `~/.local/bin`, or `ROCA_PREFIX` |
| `--api URL` | `https://api.github.com`, or `ROCA_GITHUB_API` |
| `--force` | Reinstall even when that version is already active |

Windows is not installed by the shell script. Download the `.exe` release
artefact, place it in a directory on `PATH`, and let that core binary extract
its carried `roca-vector.exe` beside it. The exact native sequence, Ollama
install, and one-time model pull are in
[Local vector search](vector.md#windows-install). The same release also
publishes `roca-vector-vX.Y.Z-windows-x64.tar.gz` for standalone use. Core
executable release artefacts are:

- `roca-<version>-darwin-arm64`
- `roca-<version>-linux-x64`
- `roca-<version>-linux-arm64`
- `roca-<version>-windows-x64.exe`

The commands below use the short Unix name `roca`. A native Windows installation
that retains the release filename uses `roca-<version>-windows-x64.exe` for each
of those invocations.

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

Init also writes and registers `prompt.md` in the selected data directory. Its
marked SYSTEM zone is shipped by La Roca; its marked USER zone belongs to the
operator. A file an earlier release wrote is moved into those zones once, and
init names the recovery copy holding the previous file; [Update](#update) owns
that migration's rules. If that optional write fails, init reports a warning and
leaves the prepared database usable. It does not edit agent instruction files or
install integrations without a separate command.

A successful human-readable init reports the corpus floor: the oldest ingested
moment, the bedrock your memory reaches back to. An empty database says so
plainly instead of printing a zero date. It ends with an `answering:` line that
names the active provider/model, the exact configuration path, and the setting
that changes it. `roca doctor` reports the same floor as part of installation
health, and `--json` carries the machine fields in both commands.

Before asking for any provider setup, init detects supported agent CLIs already
on `PATH` and uses their existing signed-in sessions. Its summary names the
selected factory-default provider and points at `roca model check <provider>` to
confirm that session answers; the next step is simply `roca query "<question>"`.
Ollama and then keyword search remain the honest local fallbacks when no
detected CLI can serve. Models authenticate through their own CLIs; La Roca
stores no secrets.

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
binary's version check, and swaps it into place by rename. The swapped binary
then refreshes every shipped plugin payload exactly as installation does. Data
plugins keep the databases they already own; vector is replaced from the same
core release while its manifest-owned `state/` directory is preserved byte for
byte. An unowned or externally sourced vector installation is named and left
untouched instead of being overwritten. Existing data, configuration and agent
integrations remain in place. If bundled placement or verification fails, the
previous core remains active.

The roca skill, the generated `roca-semantica` catalog skill, `prompt.md`, and
the Claude authorship hook are registered in the schema-versioned
`~/.roca/artifacts.json`. Each entry records its harness,
path, installed release, available release, format, and SYSTEM checksum. The
same registry feeds uninstall's central owned-path inventory; an artifact with
operator bytes in its USER zone is not claimed as a whole file.

Automatic artifact refresh is a default-off rollout:

```toml
[features]
artifact_refresh = true
```

With the key absent or false, `roca update` discovers legacy installs, records
their state and reports outdated artifacts, but changes none of them. Proposals
for another already-supported harness are informative only. Enabling the key
lets update replace each unchanged SYSTEM zone with the new release while
transplanting the USER zone byte for byte. A pre-zone file is recognized as this
product's own by the opening every release of that artifact has carried, so the
text an older release installed becomes SYSTEM instead of surviving as a stale
copy; any unrecognized legacy bytes become USER content on the one-time
migration. A recognized file is replaced whole, so anything appended to it goes
too, and the command names the recovery copy that holds it.

An edit inside SYSTEM is divergence. So is a zoned file no registry entry stands
behind, whose SYSTEM zone cannot be proven to be La Roca's, and so is a
registered file the operator deleted between refreshes. Update and `roca skill
install` name that file, say which of the three happened, and give the force
command for it (`roca update --force-artifacts`, or `roca skill install
<runtime> --force` for one skill), then leave it alone without prompting.
Forcing a diverged artifact replaces SYSTEM and still preserves USER.

A deleted skill is the one case an explicit install answers by itself: `roca
skill install <runtime>` writes it again without force, because the operator
asked for that file by name and a file that is gone has no bytes of theirs to
overwrite. An automatic refresh has no such instruction and leaves the deletion
alone.

An artifact whose zone markers are there but broken is the one state no zone can
be read from, so nothing can be transplanted: it is reported apart from
divergence, it never stops the other registered artifacts from being refreshed,
and forcing it rewrites the whole file rather than preserving USER. Every
changed file gets a named `.roca.bak` recovery copy before publication, and that
copy is where the replaced bytes survive.

The hook uses the same ownership split inside Claude's settings: the one entry
whose command ends in `hooks run claude` is the explicitly marked SYSTEM
fragment, while the surrounding settings and other hook entries are USER. No
new harness target is introduced by this lifecycle.

This registry is only for the artifacts La Roca itself ships. Third-party
skills, skill marketplaces, and remote artifact distribution are not part of
this feature.

If an existing database uses the legacy search tokenizer, the first writable
command after the update automatically rebuilds only the derived full-text
indexes from their source rows. La Roca prints one progress line while this
runs; source rows are never changed. An interrupted rebuild resumes safely on
the next writable command, and completed upgrades are not rebuilt again.

After the swap, update reports how many new capability proposals are open. On
the first eligible command run with each new version, La Roca offers every open
proposal once for that version. Init reserves its short question budget for the
database and model chooser, so proposals wait for the next command. In a
terminal La Roca asks before each proposal change; an accepted change edits only
the declared TOML values, preserves unrelated content, and creates the same
named recovery backup as other configuration edits. A rejection changes no
configuration. Without a terminal, each proposal is one plain alert: La Roca
does not prompt or edit the configuration.

`roca doctor` always lists proposals that remain open, even after they were
already offered for the current version. An interactive doctor run offers them
again; `--json` reports them under `capability_proposals` without prompting.
The current proposals are:

- When an explicit provider order excludes a detected Claude Code binary,
  offer the shipped local-CLI preset at the front of that configured order;
  its default model is `sonnet`. An absent order already detects it as the
  factory default and needs no proposal.
- When a configuration names a retired remote provider, offer migration to a
  detected local agent CLI. A provider that declares its own `command`, or whose
  own CLI is detected, is offered the removal of its retired authentication keys
  instead: its transport and the rest of its table stay. If no CLI is on `PATH`,
  offer to drop the retired entry. Declining leaves the document unchanged and
  queries degrade honestly.
- When only a credential file from an older release is left, offer to remove
  that file alone. It changes no model configuration and never disables a
  provider this build can still run.

The update channel can be selected with the same repository and API environment
variables used by installation.

`--api` and `ROCA_GITHUB_API` exist only for tests and trusted GitHub-compatible
mirrors. A custom base must be HTTPS, contain no credentials, query or fragment,
and is combined only with an `owner/name` repository path. Setting either
redirects release metadata and downloads, so do not point it at an origin you
do not trust. A trusted mirror with a private CA can use the standard
`SSL_CERT_FILE` environment variable. The optional `features.release_redirects`
switch makes the requests the binary itself sends to that channel follow at
most three redirects: enough for the hop an authenticated asset URL makes to
its storage host, and short of a chain that loops or drags the download
somewhere else. It is off when absent.

## Uninstall

```sh
roca uninstall
roca uninstall --keep-data
roca uninstall --purge
```

Without an explicit data flag, uninstall asks for consent in an interactive
terminal. `--keep-data` removes the executable and integrations while retaining
the data directory. `--purge` removes every artefact La Roca owns, including the
database, configuration, indexes, logs, generated prompt, backups, skills, and
integration recovery copies, plus the credential files and model catalogue cache
that older releases left behind. The recovery copies a refresh left beside a
managed artifact belong to the same family: a regular uninstall names them as
kept, and a purge takes them with the rest, so the directory holding them can be
taken back too.

A pre-zone skill an older release installed is recognized by its opening rather
than by a checksum, so withdrawing one leaves a recovery copy of the file before
removing it: anything an operator appended to it before the zones existed lives
nowhere else. A skill whose USER zone the operator wrote into leaves the same
named copy and the same way. Their lines are not left behind at `SKILL.md`,
because a skill file kept there without its frontmatter is one the runtime goes
on loading after La Roca is gone.

That one copy is the exception a purge does not take. Everything else beside the
skill goes under the same consent as the rest of the recovery family, but what
an operator wrote is never this product's to delete, so the copy holding it is
kept and named — the same rule that keeps a `prompt.md` with content in its USER
zone out of the owned-path inventory. The directory holding it stays with it.

A purge also removes the installed plugin packages under `~/.roca/plugins/` and
the `roca-<name>` executables the installer placed, so no plugin code is left on
a machine La Roca was removed from. A directory there is claimed only through the
manifest the installer generated in it, whose declared writable state directory
is a package-owned namespace claimed whole, and anything else under that path
survives untouched. An executable whose bytes changed since its install is no
longer the file La Roca placed, so it survives too.

Archived plugin data is the one thing a purge asks about separately.
`~/.roca/plugin-custody/` holds the directories a custodial [plugin
uninstall](plugins.md#verified-packages-and-lifecycle) refused to delete, so this
command names each archive with its size and removes it only after an explicit
`y`. Declining, and any run with nobody at the terminal or with `--json`, leaves
the archives untouched and names where each one remains.

Uninstall edits each supported agent configuration surgically, preserving all
unrelated bytes. It refuses to delete files it cannot identify as product-owned
and reports them with a reason. Re-running uninstall is safe.

## Release ownership

The authoritative release channel, platform matrix, checksums, and optional
plugin packages are documented in [Releases](releases.md).
