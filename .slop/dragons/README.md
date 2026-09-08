# Adjacent-feature dragons

One YAML record per recorded adjacent feature from the 2026-09-08 dive.
The shape matches [slopslint#4](https://github.com/thellmwhisperer/slopslint/issues/4):
`category: adjacent_feature`, `status: removed|accepted`, and the incident
fields (`intent`, `what_was_built`, `justifying_sentence`, `source`, `cost`),
plus `rule` and `forbid`.

Until slopslint lands that category, `make slop` runs
`scripts/check-dragons.sh`. A `removed` record whose `forbid` path, symbol or
string is present in the tree fails the gate. `accepted` records are
documented and still in the product; later wave tickets flip them to
`removed` only when the forbid is actually gone.

The gate uses the existing Go YAML decoder, including quoted escapes and inline
lists. Every removed record must have a non-empty forbid. It scans Git-tracked
files in the source/fixture roots declared once in `check_test.go`, including
Makefile and extensionless sources. The register and dive evidence documents
are outside that product scope; a source directory named `dragons` is still
checked. There is no scan-root argument. The synthetic test executes the same
gate on versioned clean and reintroduced fixtures.

Mark `removed` only when the forbid is absent from the tree being checked.
[CONTRIBUTING.md](../../CONTRIBUTING.md#build-and-test) owns cost-check
mechanics, including the separate synthetic vector tests.

Each record cites `INFORME-SLOP-ADYACENTE.md` (dive of 2026-09-08) by
section or suspicion. For D-series records, section 3 describes the dragon;
section 6 is the dismantling order.
