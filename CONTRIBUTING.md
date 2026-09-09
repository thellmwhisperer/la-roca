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

Adjacent features already found are recorded in the
[dragon registry](.slop/dragons/README.md), which owns removal rules and gate
scope. See [Build and test](#build-and-test) for cost-check mechanics.

## Public text

Issues, PR titles and bodies, commit messages, changed filenames, and added diff
lines must omit personal home, temporary and mounted-volume paths, local
hostnames, private LAN addresses, and session UUIDs. Use `~`, `$TMPDIR`, `<workspace>`, `<host>`,
`<lan-ip>`, and rounded counts. The UUID check detects version 4; it exempts
files beneath a `testdata/` directory.
Existing history is outside this gate's scope.

Use the [bug report](.github/ISSUE_TEMPLATE/bug_report.md) or
[feature request](.github/ISSUE_TEMPLATE/feature_request.md) template when opening
an issue; both include the public-text reminder.

`scripts/public-text.py` owns the shared check. The `public-text` CI job fails
with the offending line; opening or editing an issue flags the pattern without
repeating the value. Before publishing a PR, pipe its final title and body
(including generated evidence) to `python3 scripts/public-text.py --text` and
replace findings until it passes.
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
make playground-test
make dist
```

`make check` runs formatting, vet, unit tests, the acceptance tests, and the
slop gate (duplication, orphans, claims, and removed adjacent-feature
forbids). Godog acceptance contracts live directly under
`features/{store,ingest,provider,distribution}/`; every feature there is
discovered automatically, and `make accept-index` rejects any other layout. The
acceptance harnesses are compiled only with the `acceptance` build tag.

`make accept` (also part of `make check`) and `make split-oracle` run without
an external playground checkout or network service. Core exercises the optional
plugin's argv, errors, audit and diagnostic contracts with small local fake
executables. `make playground-test` runs these local fake-executable and
plugin-absence contracts directly, including custody and diagnostics, without
building the native vector payload or requiring a published binary.
Human answering scenarios belong to the playground repository.
`make playground-integration` separately downloads the pinned `v0.1.1` release
through the real `roca plugin install` flow and verifies a synthetic SQL result.

`make playground-evidence ROCA_PUBLISHED_BIN=<pinned-v1.82.6-binary>` retains the
published-versus-branch S1 extraction evidence under `.tmp/playground-evidence`.
It is a separate opt-in comparison on synthetic homes, never a mutable
`make check` dependency. It requires an explicit executable, validates its
v1.82.6 version, and fails if the published comparison does not execute.

`make migrate-test ROCA_PUBLISHED_BIN=<published-binary>` runs `TestCostMigrate`
against two synthetic verified homes, including legacy DATA-4 rows, and requires
an explicit published executable. It records published and branch measurements
under `.tmp/migrate-evidence` (override with `ROCA_MIGRATE_EVIDENCE_DIR`), checks
that the published baseline reproduces size-dependent reads, and requires branch
logical reads (including cache hits) within five percent across sizes and opens
under 100 ms. It then takes frozen snapshots offline and repeats SELECT and
migrate, checking that migrated exchanges remain readable. Linux uses process
read accounting; macOS requires `clang` for read instrumentation and measures
time separately without instrumentation. It never measures the live federation.
The ordinary acceptance suite runs the branch regression without requiring a
published binary; the paired target is also required by the delivery test command.

The D1 acceptance denies temporary copies throughout the read-only operation
and checks durable database digests, allowing SQLite SHM. Synthetic vector
latency tests live in `plugins/vector/internal/vector/status_issue336_test.go`:
they require a completed nonempty indexing pass and an actual query result
before judging latency in the process doing the work. They do not claim to
measure native model residency or bytes read. There are no expected-fail costs.
See [D7 batch indexing evidence](docs/d7-batch-evidence.md) for the isolated
published-versus-branch transaction counts and timings.
See [D8/D9 source generation evidence](docs/d8-d9-evidence.md) for unchanged-pass
source reads and stored-count status latency in the isolated lab.

`make e2e-smoke` isolates the real-binary operator path in a disposable `HOME`
and covers init, ingest, query, plugin install, and plugin update. It is also
part of `make check` and must never mutate an operator's live La Roca home.

`make upgrade-gauntlet` is the second gate every pull request has to pass: it
upgrades the committed homes of older releases through the binary you just
built. [Releases](docs/releases.md#schema-migration-definition-of-done) explains
when a change owes the gauntlet a new frozen home.

`make split-oracle` replays the core DATA SPLIT compatibility cases on their own,
the executable definition of zero behavior change for core CLI and MCP users that
`make check` already runs with the rest of the acceptance suite. It drives the
binary you just built against a fully synthetic fixture, normalizes away run
noise (timestamps, durations, correlation ids, home paths, and the build's own
version and source sha), and compares the recording against the goldens in
`testdata/data-split-oracle/`. The full digest-pinned archive is retained;
core excludes the extracted inference cases from comparison. Human answering
scenarios belong to the playground repository, as described above.
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
