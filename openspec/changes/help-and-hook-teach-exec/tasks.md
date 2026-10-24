## 1. Help

- [ ] 1.1 Make `exec` and `vector` appear in `roca --help` and `roca help` with their one-line descriptions.

## 2. Session hook

- [ ] 2.1 In `internal/distribution/cli/hooks/context.go`, replace the "returns 0 rows without a warning" clause of `sessionCraft` with: an unqualified table name is refused with the qualified candidates.
- [ ] 2.2 In `SessionHookBody`, when the database opens, add one line with each attached alias and its table names, read from the catalog the skill generator uses; no line when it does not open.

## 3. Acceptance

- [ ] 3.1 Write the four scenarios of `specs/agent-onboarding/spec.md` as acceptance scenarios run by `make accept` against the real `roca`.
- [ ] 3.2 `make e2e-cover` passes: every changed line is executed by those scenarios.
