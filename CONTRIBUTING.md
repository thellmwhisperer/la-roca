# Contributing

The [docs index](docs/README.md) is the operator-facing reading order.
Contributor agent notes (the project-intrinsic memory that used to live in
`AGENTS.md`) are in [project memory](docs/project-memory.md).

## Build and test

```sh
make build
make check
make accept-index
make upgrade-gauntlet
make split-oracle
make dist
```

`make check` runs formatting, vet, unit tests, the Godog acceptance suite, and
the duplication gate. Acceptance contracts live directly under
`features/{store,ingest,provider,distribution}/`; every feature there is
discovered automatically, and `make accept-index` rejects any other layout. The
Godog harness is compiled only with the `acceptance` build tag.

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
