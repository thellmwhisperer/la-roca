# Contributing

The [docs index](docs/README.md) is the operator-facing reading order.
Contributor entry guidance lives in [AGENTS.md](AGENTS.md#contributor-notes);
build, test, release, and architecture notes are in
[project memory](docs/project-memory.md).

## Adjacent features

La Roca is a light binary that queries local SQLite, attaches other databases,
answers by FTS or by vectors with one embedding model, plus plugins. Anything
else is adjacent until proven core.

Rules every brief and review must follow:

- Every brief states what NOT to build and the real data size.
- A reviewer demand must state its cost (bytes, seconds, lines) or it cannot
  block.
- No new subsystem outside the ticket's scope.
- Read-only means SQLite `mode=ro`, never a copy.
- Reuse the OS, SQLite and the standard library before writing an equivalent.

Adjacent features already found are recorded as dragons in
[`.slop/dragons/`](.slop/dragons/README.md). `make slop` fails when a
`removed` record's `forbid` path, symbol or string is back in the tree. The
acceptance harness runs a `cost` group against a lab fixture (`make check`);
it never measures the operator's live federation.

## Public text

Issues, PR titles and bodies, commit messages, and new diff lines must omit
personal home, temporary and mounted-volume paths, local hostnames, private LAN
addresses, and session UUIDs. Use `~`, `$TMPDIR`, `<workspace>`, `<host>`,
`<lan-ip>`, and rounded counts. Only UUIDs in `testdata/` are exempt.
Existing history is outside this gate's scope.

`scripts/public-text.py` owns the shared check. The `public-text` CI job fails
with the offending line; issue events flag the pattern without repeating the
value. Before publishing a PR body (including generated evidence), pipe its
exact candidate text to `python3 scripts/public-text.py --text` and replace
findings until it passes.
Rare legitimate matches belong in `.github/public-text-allow.txt` as exact
matched strings or exact complete lines, never regexes or globs.
Run `python3 scripts/public-text-test.py` for the synthetic acceptance/cost check.

## Build and test

```sh
make build
make check
make accept-index
make e2e-smoke
make upgrade-gauntlet
make split-oracle
make dist
```

`make check` runs formatting, vet, unit tests, the acceptance tests, and the
slop gate (duplication, orphans, claims, and removed adjacent-feature
forbids). Godog acceptance contracts live directly under
`features/{store,ingest,provider,distribution}/`; every feature there is
discovered automatically, and `make accept-index` rejects any other layout. The
acceptance harnesses are compiled only with the `acceptance` build tag.

`make accept` (also part of `make check`) and `make split-oracle` build the
optional playground fixture through `make playground-fixture`. That target
clones the plugin repository into `PLAYGROUND_DIR` when absent and builds it
against this checkout; the defaults and selected ref live in `Makefile`.
`make playground-test` additionally runs the plugin's checks and paired S1 cost
measurement on synthetic fixtures. Set `ROCA_PUBLISHED_BIN` to the published
`v1.82.6` executable for the before/after comparison; the measurement writes
evidence under `.tmp/playground-evidence`.
For S1, the pipeline test step must retain both `published.json` and `branch.json`
as evidence artifacts. A transcript of only the new binary is incomplete.
The published executable can be downloaded from the `v1.82.6` GitHub release;
run it only with the synthetic home supplied by `TestCostPlayground`.

`make e2e-smoke` isolates the real-binary operator path in a disposable `HOME`
and covers init, ingest, query, plugin install, and plugin update. It is also
part of `make check` and must never mutate an operator's live La Roca home.

`make upgrade-gauntlet` is the second gate every pull request has to pass: it
upgrades the committed homes of older releases through the binary you just
built. [Releases](docs/releases.md#schema-migration-definition-of-done) explains
when a change owes the gauntlet a new frozen home.

`make split-oracle` replays the DATA SPLIT compatibility oracle on its own, the
executable definition of zero behavior change for CLI and MCP users that
`make check` already runs with the rest of the acceptance suite. It drives the
binary you just built against a fully synthetic fixture, normalizes away run
noise (timestamps, durations, correlation ids, home paths, and the build's own
version and source sha), and compares the recording against the goldens in
`testdata/data-split-oracle/`.
The oracle never reads a real `~/.roca` database and never writes user data: it
records into a temporary home and keeps the recording under the project's
`.tmp/` only when it differs from the golden. A difference is reported, never
absorbed, because an intended behavior change is an owner decision, not
something quietly edited into the golden.

The bundle's `manifest.json` pins SHA-256 digests for `fixture.json` and
`golden.json`, so accidental corruption of either file fails verification
instead of passing quietly. The digests need no key custody and judge nothing
about intent: what protects the golden contract is that any change to the
goldens or to the oracle harness is an owner decision, and it never merges
without the owner reviewing the diff.
