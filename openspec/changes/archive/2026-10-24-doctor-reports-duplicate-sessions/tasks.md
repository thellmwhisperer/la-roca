## 1. Doctor warning

- [x] 1.1 In `Service.Doctor` (`internal/provider/service/doctor.go`), next to `bedrock`, count duplicate groups and extra rows with one read-only query over `plugin_roca_corpus.sessions` grouped by `substr(session_id,1,35)`, `started_at` and title, restricted to 36-character ids; append one warning to `report.Warnings` when the count is above zero, with the counts and the listing SELECT.

## 2. Acceptance

- [x] 2.1 Add a fixture export with two sibling sessions (same stem, `started_at` and title) and write the three scenarios of `specs/corpus-duplicates/spec.md`, run against the real `roca` by `make accept`.
- [x] 2.2 `make e2e-cover` passes: every changed line is executed by those scenarios.
