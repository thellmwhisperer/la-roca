## 1. Flags and filter

- [ ] 1.1 Parse `--since`, `--until` (local date, `yesterday`, `today`, `<n>h`, `<n>d`) and `--project` on `roca query` and `roca vector query`.
- [ ] 1.2 After retrieval and before printing, drop hits outside the window or project; keep and mark hits whose source row has no timestamp or project.
- [ ] 1.3 Add the three flags to the help lines printed after a search.

## 2. Acceptance

- [ ] 2.1 Write the five scenarios of `specs/search-filters/spec.md` as acceptance scenarios run by `make accept` against the real `roca` on a fixture corpus.
- [ ] 2.2 `make e2e-cover` passes for the core lines; the `roca-vector` plugin lines carry their own module tests.
