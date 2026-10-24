## 1. Help

- [x] 1.1 Make `exec` and `vector` appear in `roca --help` and `roca help` with their one-line descriptions; a `roca vector` call the companion does not take names how to turn vector search on.

## 2. Session hook

- [x] 2.1 In `internal/distribution/cli/hooks/context.go`, replace the "returns 0 rows without a warning" clause of `sessionCraft` with: an unqualified table name is refused with the qualified candidates.
- [x] 2.2 In `SessionHookBody`, when the database opens, add one line with each alias `roca exec` attaches and its table names; no line when it does not open.

## 3. Acceptance

- [x] 3.1 Write the scenarios of `specs/agent-onboarding/spec.md` as acceptance scenarios run by `make accept` against the real `roca` (`features/distribution/cli.feature`).
- [x] 3.2 `make e2e-cover` passes: every changed line is executed by those scenarios.
