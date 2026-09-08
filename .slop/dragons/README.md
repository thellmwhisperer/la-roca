# Adjacent-feature dragons

One YAML record per adjacent feature the 2026-09-08 dive named D1–D12.
The shape matches [slopslint#4](https://github.com/thellmwhisperer/slopslint/issues/4):
`category: adjacent_feature`, `status: removed|accepted`, and the incident
fields (`intent`, `what_was_built`, `justifying_sentence`, `source`, `cost`),
plus `rule` and `forbid`.

Until slopslint lands that category, `make slop` runs
`scripts/check-dragons.sh`. A `removed` record whose `forbid` path, symbol or
string is present in the tree fails the gate. `accepted` records are
documented and still in the product; later wave tickets flip them to
`removed` only when the forbid is actually gone.

The temporary gate accepts block lists and empty `[]` under `forbid`;
other forms, including nonempty inline lists, fail with a format error.

Mark `removed` only when the forbid is absent from `main`. Cost assertions
for this registry live in the acceptance `cost` group and run against a lab
fixture, never the operator's live federation.

Each record cites `INFORME-SLOP-ADYACENTE.md` (dive of 2026-09-08) by
section. Section 3 describes the dragon; section 6 is the dismantling order.
