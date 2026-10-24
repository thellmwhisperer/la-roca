## Why

After a search, the next thing an agent needs is almost always the whole conversation a
hit belongs to. `roca vector query` offers only "read a hit in full", which prints one
exchange, and `roca query` offers no per-hit line at all. The agent has to find the
session id from the hit and write a SELECT by session ordered by exchange number; new
agents miss that step.

Who asked: proposal, from issue #547; decision 8151 orders release tickets rewritten as
OpenSpec changes when they carry the same format problem.

Proof: on 24-oct-2026, `roca vector query "<phrase>" 3 --databases corpus` prints two help
lines: "read a hit in full" (one exchange by id) and "narrow or widen"; `roca query
"<phrase>"` prints three help lines, none per hit. The read-hit line comes from
`pkg/vectorhelp/vectorhelp.go:38`.

## What Changes

- `roca vector query` and `roca query` print one more help line when a hit is an exchange
  or a thinking block: a complete `roca exec` statement that returns that hit's whole
  session ordered by exchange number, with `--max-chars 2000`.
- For a thinking hit the inner lookup reads `thinking_blocks`; memory hits get no such
  line. The statement uses the hit's own database alias.

## Capabilities

### New Capabilities

- `search-session-help`: the whole-session help line printed after a search.

### Modified Capabilities

None.

## Impact

- Code: `pkg/vectorhelp` (the read-hit hint) and the help lines of `roca query` in core.
- Acceptance scenarios to write, run by `make accept` against the real `roca` on a fixture
  corpus: the line after an exchange hit, the line after a thinking hit, no line when the
  only hits are memories, and the printed statement returning the session in order.
- Not touched: ranking, the read-hit line itself, the `exec` gate, no new verb.
