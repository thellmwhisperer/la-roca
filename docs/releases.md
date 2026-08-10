# Releases

`.github/workflows/release-please.yml` maintains one release pull request from
the Conventional Commits merged into `main`; it does not build artefacts.
Merging that pull request creates the `vX.Y.Z` tag. The tag then triggers
`.github/workflows/release.yml`, which is the only workflow that builds and
uploads release artefacts.

The workflow uses the `RELEASE_PLEASE_TOKEN` repository secret instead of the
automatic `GITHUB_TOKEN`. GitHub does not start a second workflow from a tag
created with `GITHUB_TOKEN`; the repository token is what lets the new tag start
the existing release channel. Scope that fine-grained token only to this
repository, with write access to contents and pull requests. The workflow's own
`GITHUB_TOKEN` is read-only, and the privileged token is available only on a
push to `main`: there is no branch-selectable manual or pull-request trigger and
no checkout of repository code. Also allow GitHub Actions to create pull
requests in the repository settings. When the secret is absent the workflow
stays green: a first step checks the token via the environment and sets an
output, and the action runs only when that output says the token is present.

## Version policy

The manifest starts at version `1.0.0-rc.9`, corresponding to the published tag
`v1.0.0-rc.9`. The initial `release-as` override makes the first proposal the
official `1.0.0` release. Release Please also updates the version in
`plugin.json` through `extra-files`, so the plugin manifest has no independent
version lifecycle. Remove `release-as` from `release-please-config.json` after
that release lands; leaving it in place would keep forcing the same version.

After `1.0.0`, normal Conventional Commits choose the next version:

- `fix:` produces a patch release.
- `feat:` produces a minor release.
- A commit with `!` after its type, such as `feat!:`, or a
  `BREAKING CHANGE:` footer produces a major release.

When an operator must select a version explicitly, temporarily set
`release-as` to that version, merge the generated release pull request, then
remove the override.
