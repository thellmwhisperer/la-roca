## 1. Gate

- [ ] 1.1 In the `roca exec` gate, detect a `LIKE` with a leading `%` on a text column of a table with an FTS twin, from the parsed statement, and refuse it before execution unless `--timeout-ms` is set above the default.
- [ ] 1.2 Build the printed `roca exec` statement from the existing table-to-twin mapping: `MATCH` on the twin with the pattern's term, joined back to the table's rows.

## 2. Acceptance

- [ ] 2.1 Write the five scenarios of `specs/exec-fts-guidance/spec.md` as acceptance scenarios run by `make accept` against the real `roca` on a fixture corpus.
- [ ] 2.2 `make e2e-cover` passes: every changed line is executed by those scenarios.
