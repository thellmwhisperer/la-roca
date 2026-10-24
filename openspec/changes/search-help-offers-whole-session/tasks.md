## 1. Help line

- [ ] 1.1 In `pkg/vectorhelp`, next to the read-hit hint, add the whole-session hint for the first exchange or thinking-block hit; none for memory hits.
- [ ] 1.2 Print the same hint from `roca query` for its first exchange or thinking-block hit.

## 2. Acceptance

- [ ] 2.1 Write the four scenarios of `specs/search-session-help/spec.md` as acceptance scenarios run by `make accept` against the real `roca` on a fixture corpus.
- [ ] 2.2 `make e2e-cover` passes: every changed line is executed by those scenarios. If a changed line in `pkg/vectorhelp` cannot be executed by an acceptance run of the real binary, stop and report which command path is missing.
