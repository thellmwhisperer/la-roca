## Why

An agent that arrives without a loaded skill learns La Roca from two places: `roca --help`
and the session hook. Both teach the wrong thing today.

- `roca --help` prints the Cobra command list without `exec` and without `vector`, the two
  commands the lab menu and the hook tell the agent to use. It shows `query`, `playground`
  and `explore` instead.
- The session hook (`roca hooks run session`, text in
  `internal/distribution/cli/hooks/context.go:20`) says "an unqualified table returns 0 rows
  without a warning". Since #317 `roca exec` refuses an unqualified table and names the
  qualified candidates, so the sentence makes agents report a bug that is fixed.
- The attach aliases (`plugin_roca_corpus`, `plugin_roca_ops`, ...) and their tables live
  only in the generated `roca-semantica` skill. A session that skips the skill guesses
  table names.

Who asked: issue #548 (follow-up of #409).

Proof: `roca --help` on v1.93.x lists no `exec` and no `vector` line;
`roca exec "SELECT count(*) FROM sessions"` exits 1 with
`unqualified table "sessions"; candidates: plugin_roca_corpus.sessions`; the session hook
text injected into a fresh agent session on 24-oct-2026 still carries the
"returns 0 rows without a warning" sentence.

## What Changes

- `roca --help` and `roca help` list `exec` and `vector`, each with its one-line
  description.
- The session hook replaces the stale sentence with the current behaviour: an unqualified
  table name is refused with the qualified candidates.
- The session hook adds one line, built from the databases `roca exec` attaches, with each
  attached alias and its table names; columns stay in the skill.
- A `roca vector` call the companion does not take says how to turn vector search on.

## Capabilities

### New Capabilities

- `agent-onboarding`: what `roca --help` and the session hook teach an agent that has not
  loaded a skill.

### Modified Capabilities

None.

## Impact

- Code: the root command's help (where `exec` and `vector` are registered or hidden),
  `internal/distribution/cli/hooks/context.go` (`sessionCraft`, `SessionHookBody`), and
  the database inventory `roca exec` routes through (`internal/distribution/cli/skill.go`).
- Acceptance scenarios in `features/distribution/cli.feature`, run by `make accept`
  against the real `roca`:
  `roca --help` shows both commands; `roca hooks run session --runtime <r>` on a home with
  the bundled plugins prints the alias line matching the attached databases and no
  "0 rows without a warning" text.
- Not touched: the skill files and their generator output, the lab menu printed by bare
  `roca`, the `exec` gate itself, pills and handoffs in the hook, column lists.
