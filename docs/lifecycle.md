# The lifecycle: install, calibrate, update, uninstall

La Roca is one static file. Installing it is copying that file onto the PATH,
updating it is replacing it, and uninstalling it is deleting it and, with your
consent, its data. Everything in this document follows from that.

The four commands are `install.sh`, `roca calibrate`, `roca update` and
`roca uninstall`. `roca init` runs the whole bootstrap, generates the first
calibration when needed, and writes `prompt.md` in the configured data
directory, the presentation block a human can paste into agent instructions.
It never edits those instructions.

## Installing

```sh
# Public repository
curl -fsSL https://raw.githubusercontent.com/thellmwhisperer/la-roca/main/install.sh \
  | sh

# Private repository — authenticated route
TOKEN="<the operator's token>"; REPO="thellmwhisperer/la-roca"
curl -fsSL -H "Authorization: Bearer ${TOKEN}" \
     -H "Accept: application/vnd.github.raw" \
     "https://api.github.com/repos/${REPO}/contents/install.sh" \
  | GITHUB_TOKEN="${TOKEN}" sh -s -- --repo "${REPO}"
```

The anonymous one-liner against `raw.githubusercontent.com` answers **404** on a
private repository, and no amount of README wording fixes that. While the repo
is private, the authenticated contents API is the route, and `GITHUB_TOKEN` has
to be on both sides of the pipe: `curl` needs it to read the script, and the
shell the script runs in needs it to download the release.

After the script is loaded, it resolves the selected artefact from the release
metadata, tries the asset URL from the API response first, and falls back to
GitHub's conventional release-download URL when that route does not serve it.
`checksums.txt` follows the same fallback. The artefact is still verified
against that checksum before anything is written.

| Flag | Default |
|---|---|
| `--repo owner/name` | `thellmwhisperer/la-roca`, or `ROCA_REPO` |
| `--version vX.Y.Z` | the latest published release |
| `--prefix DIR` | `~/.local/bin`, or `ROCA_PREFIX` |
| `--api URL` | `https://api.github.com`, or `ROCA_GITHUB_API` |
| `--force` | reinstall even when the target version is already there |

Windows is not installed by the script: it prints the release URL and the
artefact name to download. The four artefacts the channel builds are
`darwin-arm64`, `linux-x64`, `linux-arm64` and `windows-x64`, and `install.sh`,
`roca update` and the Makefile all spell them the same way,
`roca-<version>-<platform>`, because a fifth spelling anywhere turns "no
artefact for your platform" into a lie. Windows is the one name with a different
shape (`roca-<version>-windows-x64.exe`), because without the extension the
operating system will not run the file at all; `release.ArtefactName` and a test
that reads the Makefile keep the two halves of that agreement together.

Three properties, each covered by an acceptance scenario:

- **The checksum is verified before anything is written** (F01-01, and `Verify`
  in `internal/distribution/release`). A binary that runs is your only way back and it is not
  risked on a download. An artefact with no line of its own in `checksums.txt`
  is refused too: passing it through would be verifying nothing while reporting
  that it verified.
- **It converges over an interrupted installation** (F01-10). The target is only
  ever replaced by `mv` of a fully downloaded, already-verified file, so a run
  killed with `-9` leaves the previous binary answering; the next run removes
  whatever the killed one staged. Measured with a real `SIGKILL`, not simulated.
- **It never overwrites a file it did not put there** (F01-12). A `roca` in the
  prefix that does not answer as a roca binary is named, and the run stops.

Reinstalling the same version prints `already installed` and does not touch the
file, inode included (F01-11).

## Calibrating

```
roca calibrate                 # generate the next version of your bench
roca calibrate --cases 40      # aim for more cases
roca calibrate --no-llm        # do not ask any model to word the questions
roca calibrate --out DIR       # somewhere other than ~/.roca/bench
```

The command narrates the classifier pass, sampling and proof counts, every
model-wording request, and the bench path and case count when it publishes.
Those lines are deliberately plain terminal output: a slow provider must never
look like a wedged command.

**The binary ships the format and the runner and no questions at all.**
A public binary carrying anybody's vocabulary is a leak, so the exam every
installation is measured against has to be written where the corpus lives. That
is what this command is for, and `docs/golden-bench.md` is the format it writes.

How a case is made:

1. **Sample.** Corpus rows are read spread over the whole memory, not the first
   N: a bench built out of the twenty oldest memories measures January.
2. **Cut in two.** Each note is cut in the middle. The question is asked with
   words from one half and the sentinel that has to come back is a phrase from
   the other, and a row whose halves share words is discarded. That is the one
   rule `docs/golden-bench.md` earned the hard way: a bench whose questions
   repeat the searched text measures `LIKE`, not search.
3. **Prove.** Every candidate runs, exactly the way `roca bench golden` will
   run it, against the reference floor. What cannot find its own memory is not
   published. **A generated bench is born green**, because a case that measures
   nothing is a red line in every future report that tells you about the
   generator instead of about your search.
4. **Word.** With a model configured, it rewrites the terms into a question a
   person would type. The wording enters only if it carries no part of the
   sentinel and only if the case still proves with it, so the worst a model can
   do here is cost a request. With no model available the terms themselves are
   the question, which is a weaker case and a legitimate one — the same shape as
   the query cascade falling to its own floor.

**Versioned, and a new bench never overwrites the old one**
(`golden-0001.yaml`, `golden-0002.yaml`, …). Comparing this month's score with
last month's is the only thing a bench is for, and a file that rewrites itself
makes every historical score incomparable.

**Regeneration is by milestone, suggested by `roca doctor`, never automatic.**
`roca init` generates the first bench when there is corpus and no bench yet, and
never again: a second init reports `present` and leaves the file where it is.
Doctor asks for a new one in exactly two cases — there is none, or the corpus
has tripled since the one there is was generated — and it reports both numbers
so the suggestion is a comparison and not a verdict you have to trust.

An empty corpus is a pending job with its remedy written down, not a failure:
`roca ingest` fills it and the next `roca init` completes the bootstrap.

## Updating

```
roca update            # replace this binary with the latest release
roca update --check    # report what is published, replace nothing
roca update --version vX.Y.Z
```

The repository comes from `--repo`, then `ROCA_REPO`, then the `release_repo`
key under `[defaults]` in `config.toml`, and last from `release.DefaultRepo`,
the channel this product publishes from. A fork, a mirror or a private rebuild
is one flag away, which is the reason the default is the last link and not the
only one.
The credential comes from `GITHUB_TOKEN` and from nowhere else, so it lives in
no output and in no file of this product's.

The order is the contract, and it is what keeps a working binary on the machine
at every instant:

1. Ask the channel, and stop if you are already on the latest version.
2. Download the artefact and `checksums.txt`, and **verify before touching
   anything**. A transfer that came down shorter than the size the channel
   declared for the asset is refused here as what it is, a cut download: without
   that check a proxy closing cleanly after half the bytes raises no error at
   all, the checksum fires instead, and the operator goes hunting for a corrupt
   release that is perfectly fine.
3. Sweep whatever a killed update left staged in the prefix, then stage the new
   binary beside the current one, so the rename that follows stays inside one
   filesystem and is therefore atomic. Nothing else ever removes those files,
   and they are chmod 755: it is the same rule `install.sh` applies to its own
   leftovers.
4. Ask the staged binary for `--version`. A wrong architecture or a truncated
   download dies here, with nothing replaced.
5. Move the current one aside, rename the staged one into place, ask again.
   **Only then** is the previous one removed; if the new one stopped answering,
   the old one comes back and the command says so.

The previous binary is never deleted on a failure, whatever happens to the
rename that should have put it back: with the new one gone and the old one
deleted there is no `roca` left on the machine and nothing on disk to copy into
place, so a bad update would have turned into a reinstall. When the rollback
itself cannot complete, the file stays and the error says where it is.

A 404 has two readings with opposite remedies, and which one you are told
depends on whether a credential travelled: with no token it is the private
repository (export `GITHUB_TOKEN`); with one it names what was asked for and
leaves both live readings standing, because from here they genuinely are both
live. Sending an operator who already exported a token to export one is how a
diagnosis costs an afternoon.

## Uninstalling

```
roca uninstall                 # asks; `n` purges, anything else keeps the data
roca uninstall --keep-data     # for scripts
roca uninstall --purge
```

The question is the operator's own and the default keeps your data,
because the other answer cannot be taken back.

What it always does: unlinks the binary, and withdraws La Roca's entry from
every runtime's MCP configuration and from the settings file that declares its
session hooks, leaving every other byte of those files exactly where it was
(F02-05). The integrations go first, so an agent is never left pointing at a
binary that has gone.

What `--purge` adds: the database and its journals, the configuration, the
backups, cache, credentials, generated prompt and dated JSONL logs.

**The D-7 contract**, and it has two halves that look contradictory and are not:

- **What La Roca owns is deleted whenever it is there.** The inventory is a
  DECLARATION (`ownedPaths` in `internal/distribution/cli/uninstall.go`), never a snapshot of
  the filesystem taken before the command creates its own artefacts.
- **What La Roca did not create is never deleted.** It is reported by name, with
  the reason, and the directory holding it survives with it. That protection is
  kept whole. What was removed is the race, not the protection.

Both halves fail in the same direction when the inventory is incomplete, and the
cache proved it: the classifier's cache is written under a directory named after
the database's fingerprint, so declaring only that deepest path left the `cache/`
directory behind on every machine that had ever trained a classifier. It survived,
it kept the whole data directory alive with it, and the second half then reported
La Roca's own directory as a file La Roca did not create. Hence `Paths.CacheRoot`,
and hence the survivors are now checked against the inventory before they are
named: a path that is on it and is still there is one the purge failed to remove
(a live process wrote its journal back, a directory stopped being writable), and
the operator is told to run the uninstall again, not to go and delete this
product by hand.

Together they make the purge converge: it runs on a machine a previous attempt
left halfway, and applying the same plan twice ends ok both times
(`internal/distribution/lifecycle`). One consequence is worth knowing: the purge deletes the
binary that is running it, so a second `roca uninstall` from the same path has
nothing to execute. That is the shape of a one-file product, and it is why the
re-runnability is measured over the plan and not over two shell invocations.

There is no process to stop and none is left behind: every command opens the
database, works and exits.
