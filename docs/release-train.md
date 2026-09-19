# Release train

The repository ships on a two-branch train: `integration` is the default
branch where work lands, and `main` is the protected release lane where only
release merges land. [Releases](releases.md) owns the release-please channel
this page feeds.

## Branch roles

- `integration` is the GitHub default branch. Every feature, fix and docs pull
  request targets it, including the pull requests release automation opens
  against the default branch. Merging into `integration` is deliberately
  cheap: pull-request CI is its gate, and it is the branch the e2e round pins.
- `main` is the release lane. Branch protection requires the CI status checks
  (strict, so a merge must be up to date), refuses force pushes and
  deletions, and binds administrators too, so nothing lands except a
  pull-request merge with green checks. By policy those merges are exactly
  three kinds: the release merge from `integration`, a hotfix pull request,
  and the release-please release pull request.

CI runs on every pull request and on pushes to both branches, so the tip of
`integration` always carries the status the e2e round pins.

## The cycle

1. **Land.** Open a pull request against `integration` and let it merge.
2. **E2E round.** Check `integration` out at the candidate SHA on the lab
   fixture and run `make e2e`. The target chains the real-binary smoke suite
   and the frozen federation suite ([runbook](e2e-federation.md),
   [aceptacion](e2e-federation-aceptacion.md)). Both targets demand an
   installed published release (`ROCA_PUBLISHED_BIN`), and the federation leg
   also demands the pinned embedding model (`ROCA_E2E_VECTOR_MODEL`), exactly
   as the targets already enforce. The SHA that passes the round is the release candidate;
   record it in the release merge's pull request.
3. **Release.** Merge `integration` into `main` — one release merge per
   cycle. The push wakes release-please, which folds the cycle's Conventional
   Commits into its single release pull request; auto-merge lands it with a
   merge commit, the `vX.Y.Z` tag triggers the release workflow, and
   operators converge with one `roca update` per cycle.
4. **Back-merge.** Merge `main` back into `integration` as soon as the
   release lands. The train then carries the version bump, and the next
   release pull request is up to date with `main`, which strict required
   checks demand.

## Hotfix

1. Branch from `main` and open the pull request against `main`; CI gates it
   exactly like any other merge into the release lane.
2. Merge it. Release Please turns the `fix:` commit into a release pull
   request right away, and auto-merge publishes it without waiting for the
   next cycle.
3. Back-merge `main` into `integration` so the fix rides the train.

## Multi-OS status

The e2e round runs on one real machine today: the release train's lab
fixture. That is single-OS evidence, and a release candidate must not claim
more. The multi-OS matrix — the same round over the real binary on every
supported host class — is done only when a second host class runs it; the CI
matrix builds every published platform, and a built artefact is not a run on
an operator machine.
