# Releases

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

`.github/workflows/release-please.yml` maintains one release pull request from
the Conventional Commits merged into `main`; it does not build artefacts.
The workflow arms auto-merge only on a release pull request returned by
Release Please after the protected adoption boundary; the pre-adoption release
pull request keeps its existing merge policy. GitHub merges later release pull
requests once their required checks pass. That merge creates the `vX.Y.Z` tag.
The tag then triggers
`.github/workflows/release.yml`, which is the only workflow that builds and
uploads release artefacts.

Each `vX.Y.Z` binary release carries four `roca` binaries, each with its
matching vector executable embedded for installation, plus the four existing
`roca-vector-vX.Y.Z-<platform>.tar.gz` standalone archives. The embedded
semantic-search engine is available in the macOS ARM64 and Linux releases.
Windows keeps its existing compatibility payload until it passes the same
equivalence lane. `make dist`
on one machine builds the matching host payload plus Windows. The binary lane
stamps the core, embedded vector,
standalone vector, and vector manifests with one tag, checks the runnable Linux
artefacts, and publishes one top-level `checksums.txt` covering every asset. A
vector archive also contains its package-level `checksums.txt`, which is
verified during archive installation and update. The standalone archive flow is
documented in
[`plugins/vector/README.md`](../plugins/vector/README.md#install-from-a-release).

The workflow uses the `RELEASE_PLEASE_TOKEN` repository secret instead of the
automatic `GITHUB_TOKEN`. GitHub does not start a second workflow from a tag
created with `GITHUB_TOKEN`; the repository token is what lets the new tag start
the existing release channel. Scope that fine-grained token only to this
repository, with write access to contents and pull requests. The workflow's own
permissions match that contract, and the privileged token is available only on
a push to `main`: there is no branch-selectable manual or pull-request trigger
and no checkout of repository code. Also allow GitHub Actions to create pull
requests and enable auto-merge in the repository settings. A missing secret is
a release-channel failure, not a green skip: the workflow stops with the exact
fine-grained-token setup required to restore it.

The same release workflow owns the separately versioned embedding payload.
Tagging the exact model contract `models-v1` stages the upstream GGUF through
`plugins/vector/cmd/model-release`, which verifies its pinned byte count and
SHA-256 before publishing the model asset, its Apache-2.0 license, and then
`checksums.txt`. Runtime downloads use that GitHub release asset; the upstream
URL is consulted only by the release lane. Model releases are always marked
non-latest, including on reruns, so `releases/latest` remains the binary channel
used by the installer and `roca update`. Any other `models-v*` tag is refused by
the staging command, so changing model bytes requires an explicit new contract
in the tree.

## Version policy

The release manifest and the repository-root `plugin.json` both record the
current published version. Release Please updates the plugin version through
`extra-files`, so the plugin manifest has no independent version lifecycle and
no permanently pinned release override. A bundled plugin's federation manifest,
such as
`internal/distribution/rocacorpus/plugin.json`, is not an `extra-files` target:
the installer stamps it with the running build's version, so its checked-in
placeholder never ships.

Normal Conventional Commits choose the next version:

- `fix:` produces a patch release.
- `feat:` produces a minor release.
- A commit with `!` after its type, such as `feat!:`, or a
  `BREAKING CHANGE:` footer produces a major release.

## Schema migration definition of done

A release that changes a shipped schema in a way an existing database must adopt
also adds one new frozen upgrade home. That covers core's `data/schema.sql` and
each bundled plugin's own
`internal/distribution/{rocaops,rocacron,rocacorpus}/schema.sql`: a plugin
schema change comes with a bump of that plugin's `SchemaVersion` or
`IndexVersion` constant, which the next update adopts through
`bundledplugin.ApplySchema`, reopening every named custody migration the
installed database hosts to the `prepared` state and clearing its verification.
The steps are the same either way:

1. Add the newest published pre-migration version to
   `internal/distribution/release/testdata/upgrade/versions.txt`. That file is
   the only list of frozen homes, and the helper refuses a version it does not
   already name.
2. Run `scripts/freeze-upgrade-home.sh vX.Y.Z` and commit its single `tar.gz`
   fixture, which holds the byte-exact synthetic database, config, prompt, and
   `origin.json`.
3. Run `make upgrade-gauntlet` for every committed home, or
   `make upgrade-gauntlet UPGRADE_HOME=vX.Y.Z` for the new one alone.

The helper downloads and verifies the real release binary, runs `init` plus a
small synthetic ingest in an isolated HOME, and records no operator data. Both
workflows read `versions.txt` and give every committed home a job of its own,
which upgrades it with the current binary, performs another synthetic ingest,
executes deterministic SQL reads, and runs doctor and data health checks. The
binary release lane publishes only once all of those jobs are green, so a
failed compound migration blocks the binary release.
