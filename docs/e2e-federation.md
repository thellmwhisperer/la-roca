# Frozen federation end-to-end suite

The suite runs **commands** against an **installed** `roca` binary. Unit tests
do not satisfy it. The fixture is the content-addressed archive
`testdata/e2e-federation/frozen.tar.gz` (digest in
`testdata/e2e-federation/frozen.sha256`). The live operator home is never
selected.

The suite extracts that archive into a disposable HOME. It does not run `init`,
`ingest`, or `store` to build the lab. Those verbs appear only as the command
under test.

The executable contract is [Aceptacion](e2e-federation-aceptacion.md). Its
Gherkin companion is
`features/distribution/e2e-federation.feature`. Hermetic scenarios run in
`TestJourneyAcceptanceSuite`. Ready-index and published-upgrade scenarios are
tagged `@provisioned` and run only by `TestFrozenFederationProvisionedJourney`.

## Run

From the repository root:

```sh
make e2e-federation ROCA_PUBLISHED_BIN=<published-roca>
```

Both `e2e-smoke` and `e2e-federation` require an executable published release;
the federation target also requires the local embedding model used by the
ready-index case. Set `ROCA_PUBLISHED_BIN` explicitly to
the local release executable; neither target selects it from `PATH`.
A `roca` command on `PATH` may be an SSH wrapper
that escapes the disposable HOME. The model defaults to the pinned file under
`~/.roca/models/`; override it when needed:

```sh
make e2e-federation \
  ROCA_PUBLISHED_BIN=<published-roca> \
  ROCA_E2E_VECTOR_MODEL=<embedding-model.gguf>
```

That builds this tree's binary, copies it into `.local/bin` under a disposable
HOME, extracts the frozen snapshots, and runs the hermetic
`TestFrozenFederationInstalledBinary` plus the provisioned tests. Missing
`ROCA_PUBLISHED_BIN` or `ROCA_E2E_VECTOR_MODEL` is a failure, never a skip.

`make check` includes the hermetic Go cases through `make accept` and the
non-provisioned Gherkin cases through `TestJourneyAcceptanceSuite`. Those
paths do not claim a ready vector index or a published upgrade. `make e2e-smoke`
runs both the shorter branch operator path and the published-release update
followed by branch init on a clean home.

Use `e2e-smoke` in place of `e2e-federation` in the command above for the
shorter suite, or `e2e` to run both suites. The combined target uses the same
binary and model prerequisites; the [release train](release-train.md) owns
when to run it and how to pin the candidate.

## Coverage

One case per bug shipped around 2026-09-07, using the evidence commands from
those pull request and issue bodies:

- pull 321: Codex `history.jsonl` exact-payload collision, `roca ingest --json`
- pull 325: `roca pill delete <slug>`
- pull 326: `roca exec ... --max-chars 900` on TOON and JSON
- issue 315: three `roca mcp serve` processes share one vector resident
- issue 317: unqualified `FROM memories` is refused with qualified candidates
- issue 318: `roca handoff latest --limit 1` and `--all-projects`
- issue 427: ops memory ids are short JSON numbers and legacy ids still resolve
- issue 324: Codex source thread keeps its exact session id

Plus one command case per uso-de-la-roca correction source (23 exchange ids),
expressed as the binary command the agent should have run.

Plus the eight real-usage paths from operator execution logs: hooks at 0 ms,
exec of an exact frozen id under 5 s, a ready-index vector query under 2 s
without degradation, query under 3 s without a silent hybrid claim, one current
handoff per project, MCP handoff store refusal as contract, the
published-release update and clean init smoke, and MCP `roca_health`.

## Fixture rule

The bytes in `testdata/e2e-federation/frozen.tar.gz` are the lab. Rebuild them
with `scripts/freeze-e2e-federation.sh` only when the seeded rows are meant to
change, then update `frozen.sha256` and the pinned ids in Aceptacion. Do not
copy a live hub database into the tree.
