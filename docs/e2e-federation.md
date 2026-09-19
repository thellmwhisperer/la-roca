# Frozen federation end-to-end suite

The suite runs **commands** against an **installed** `roca` binary. Unit tests
do not satisfy it. The fixture is the synthetic tree under
`testdata/e2e-federation/`. The live operator home is never selected.

The executable contract is [Aceptacion](e2e-federation-aceptacion.md). The same
commands are the Gherkin scenarios in
`features/distribution/e2e-federation.feature`, run by
`TestJourneyAcceptanceSuite`.

## Run

From the repository root:

```sh
make e2e-federation
```

That builds this tree's binary, installs a copy into a disposable home prefix
(`.local/bin` under the lab `HOME`), materializes the frozen sources, and runs
`TestFrozenFederationInstalledBinary`.

`make check` includes the Go cases through `make accept` and the Gherkin cases
through `TestJourneyAcceptanceSuite`. `make e2e-smoke` remains the shorter
operator-path smoke.

## Coverage

One case per bug shipped around 2026-09-07, using the evidence commands from
those pull request and issue bodies:

- pull 321: Codex `history.jsonl` exact-payload collision, `roca ingest --json`
- pull 325: `roca pill delete <slug>`
- pull 326: `roca exec ... --max-chars 900` on TOON and JSON
- issue 315: three `roca mcp serve` processes share one vector resident
- issue 317: unqualified `FROM memories` is refused with qualified candidates
- issue 318: `roca handoff latest --limit 1` and `--all-projects`
- issue 319: ops memory ids are JSON strings
- issue 324: Codex source thread keeps its exact session id

Plus one command case per uso-de-la-roca correction source (23 exchange ids),
expressed as the binary command the agent should have run.

## Fixture rule

Edit files under `testdata/e2e-federation/` when the lab needs new invented
rows. Do not copy a live hub database into the tree.
