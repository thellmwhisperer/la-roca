# The acceptance suite

`features/*.feature` is the suite. There is no second copy of it.

Ten Gherkin files, 102 consecrated and frozen scenarios, black box over the real
`roca` binary. Which scenarios a wave claims is declared in
`test/acceptance/acceptance_test.go`, never by editing the suite; changing a
consecrated question is the suite owner's decision, never a wave's. `make accept`
runs it against the built binary in a sandbox HOME.

| Contract | File |
| --- | --- |
| F01 install cycle | `features/01_install_cycle.feature` |
| F02 operator flow | `features/02_operator_flow.feature` |
| F03 defect regression (D-1..D-8) | `features/03_defect_regression.feature` |
| F04 query cascade | `features/04_query_cascade.feature` |
| F05 golden bench | `features/05_golden_bench.feature` |
| F06 teach | `features/06_teach.feature` |
| F07 model adapters | `features/07_model_adapters.feature` |
| F08 MCP plug | `features/08_mcp_plug.feature` |
| F09 concurrency | `features/09_concurrency.feature` |
| F10 surface | `features/10_surface.feature` |

## What used to be here

`docs/SUITE-ACEPTACION.md` was the QA author's dated proposal of 2026-08-05: the
same 102 scenarios written out in Spanish prose, plus the evidence base they came
from and the traceability map back to the D-1..D-8 defects.

It was removed rather than translated, because a suite transcribed in prose next
to the executable suite is two sources of truth, and the prose is the one that
goes stale silently: the `.feature` files are run by `make accept` and the
document was not. The record itself is not lost, it is in the history:

```sh
git log --follow --diff-filter=D -- docs/SUITE-ACEPTACION.md
git show <that commit>^:docs/SUITE-ACEPTACION.md
```

Read it for the archaeology (why a scenario exists, which defect it descends
from). For what the product has to do, read `features/`.
