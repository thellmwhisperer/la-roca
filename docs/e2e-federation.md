# Frozen federation end-to-end suite

The suite runs **commands** against an **installed** `roca` binary. Unit tests
do not satisfy it. The fixture is the content-addressed archive
`testdata/e2e-federation/frozen.tar.gz` (digest in
`testdata/e2e-federation/frozen.sha256`). The live operator home is never
selected.

The suite copies that archive into a disposable HOME. It does not run `init`,
`ingest`, or `store` to build the lab. Those verbs appear only as the command
under test.

The executable contract is [Aceptacion](e2e-federation-aceptacion.md). The same
commands are the Gherkin scenarios in
`features/distribution/e2e-federation.feature`, run by
`TestJourneyAcceptanceSuite`.

## Run

From the repository root:

```sh
make e2e-federation
```

That builds this tree's binary, copies it into `.local/bin` under a disposable
HOME, extracts the frozen snapshots, and runs
`TestFrozenFederationInstalledBinary`.

`make check` includes the Go cases through `make accept` and the Gherkin cases
through `TestJourneyAcceptanceSuite`. `make e2e-smoke` remains the shorter
operator-path smoke and is the Aceptacion command for update+init on a clean
home.

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

Plus the eight real-usage paths from operator execution logs: hooks at 0 ms,
exec of an exact frozen id under 5 s, vector query under 2 s, query under 3 s
without a silent hybrid claim, one current handoff per project, MCP handoff
store refusal as contract, `make e2e-smoke`, and MCP `roca_health`.

## Fixture rule

The bytes in `testdata/e2e-federation/frozen.tar.gz` are the lab. Rebuild them
with `scripts/freeze-e2e-federation.sh` only when the seeded rows are meant to
change, then update `frozen.sha256` and the pinned ids in Aceptacion. Do not
copy a live hub database into the tree.
