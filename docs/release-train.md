# Release train

The repository uses a two-branch train: `integration` is the default
branch where work lands, and `main` is the release lane. The PR-only
protection prerequisite below must be completed before the train ships.
[Releases](releases.md) owns the release-please channel this page feeds.

## Branch roles

- `integration` is the GitHub default branch. Normal feature, fix and docs pull
  requests target it. Merging into `integration` is deliberately
  cheap: pull-request CI is its gate, and it is the branch the e2e round pins.
- `main` is the release lane. Branch protection requires the CI status checks
  (strict, so a merge must be up to date), refuses force pushes and
  deletions, and binds administrators too. Separately, it must require a pull
  request before merging. By policy those merges are exactly
  three kinds: the release merge from `integration`, a hotfix pull request,
  and the release-please release pull request.

**Rollout prerequisite, pending as of 2026-09-20:** GitHub has required
checks and admin enforcement on `main`, but no required pull requests and
no applicable rulesets. A repository administrator must enable **Require a
pull request before merging** on `main`, with zero required approvals to
preserve auto-merge, no PR bypass actors, and the existing strict checks,
admin enforcement, force-push and deletion restrictions retained. Confirm
that `required_pull_request_reviews` is present in
`gh api repos/thellmwhisperer/la-roca/branches/main/protection` before releasing.
Checks and admin enforcement alone still allow a direct push of a green SHA.

Release automation explicitly targets `main` via `target-branch: main` in
the release-please workflow, independently of the GitHub default branch.

CI runs on every pull request and on pushes to both branches, so the tip of
`integration` always carries the status the e2e round pins.

## The cycle

1. **Land.** Open a pull request against `integration` and let it merge.
2. **Freeze and e2e round.** The release owner pauses merges into
   `integration`, including already armed auto-merges, before selecting the
   candidate. Normal PR auto-merge resumes after the cycle. Back-merge any
   outstanding `main` changes first, then record the full `integration` head
   SHA as `RELEASE_CANDIDATE` in the release PR targeting `main`. Check that
   exact SHA out in a clean lab fixture checkout and run `make e2e`.
   Keep the freeze through the release merge and back-merge. If either branch
   changes before the release merge, including a hotfix or an update to the
   release PR, stop, synchronize `main` into `integration`, pin the new SHA
   and repeat the entire round. The target chains the real-binary smoke suite
   and the frozen federation suite ([runbook](e2e-federation.md),
   [aceptacion](e2e-federation-aceptacion.md)). Both targets demand an
   installed published release (`ROCA_PUBLISHED_BIN`), and the federation leg
   also demands the pinned embedding model (`ROCA_E2E_VECTOR_MODEL`), exactly
   as the targets already enforce. Record the passing evidence beside the
   candidate SHA in the release PR.
3. **Release.** Once required CI checks pass, confirm the release PR's base
   is `main`, its head branch is `integration`, and both its head SHA and the
   remote `integration` tip still equal `RELEASE_CANDIDATE`. Merge once with
   `gh pr merge "$RELEASE_PR" --merge --match-head-commit "$RELEASE_CANDIDATE"`.
   Do not arm auto-merge on this release merge PR: keep the freeze until the
   merge completes; a mismatch requires a new pin and e2e round. The push
   wakes release-please, which folds the cycle's Conventional Commits into
   its single release pull request against `main`; auto-merge lands it with a
   merge commit, the `vX.Y.Z` tag triggers the release workflow, and
   operators converge with one `roca update` per cycle.
4. **Back-merge.** Merge `main` back into `integration` as soon as the
   release lands. The train then carries the version bump, and the next
   release pull request is up to date with `main`, which strict required
   checks demand. Resume normal merges and auto-merges into `integration`.

## Hotfix

1. If a release candidate is frozen, cancel that round and re-pin and retest
   after the hotfix back-merge. Branch from `main` and open the pull request against `main`; CI gates it
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
