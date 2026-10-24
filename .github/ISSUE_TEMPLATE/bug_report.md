---
name: Bug report
about: Report a reproducible problem, written from an OpenSpec change
---

<!-- Public text: use ~, $TMPDIR, <workspace>, <host>, <lan-ip> and rounded counts; omit personal paths, hostnames and session identifiers. -->

Type: bug
Where: binario|lanzador

Written from the OpenSpec change `openspec/changes/<change-name>`. Write the change first (`openspec validate <change-name> --strict`); this issue copies it and never grows.

## Why

<!-- From the proposal's Why: what happens, what should happen instead, and who asked for it. -->

Proof: <!-- the command that reproduces the problem, a run id or a log line -->

## Scenarios

<!-- Copied from the spec's WHEN/THEN, one bullet each. The first one fails today. -->

- WHEN ..., THEN ...

## Done when

- The scenarios above are acceptance scenarios in `features/<domain>/*.feature`, with steps in `test/acceptance`, run by `make accept` against the real `roca`. They fail on integration and pass with the fix.
- `make e2e-cover` passes: every changed non-test line is executed by those scenarios.

## Not touched

<!-- From the proposal's Impact. -->
