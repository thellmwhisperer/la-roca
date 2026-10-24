---
name: Feature request
about: Propose a bounded change, written from an OpenSpec change
---

<!-- Public text: use ~, $TMPDIR, <workspace>, <host>, <lan-ip> and rounded counts; omit personal paths, hostnames and session identifiers. -->

Type: feature
Where: binario|lanzador

Written from the OpenSpec change `openspec/changes/<change-name>`. Write the change first (`openspec validate <change-name> --strict`); this issue copies it and never grows.

## Why

<!-- From the proposal's Why: what fails today and who asked for it. -->

Proof: <!-- a run id, a log line or the command that reproduces the problem -->

## Scenarios

<!-- Copied from the spec's WHEN/THEN, one bullet each. -->

- WHEN ..., THEN ...

## Done when

- The scenarios above are acceptance scenarios in `features/<domain>/*.feature`, with steps in `test/acceptance`, run by `make accept` against the real `roca`. They fail on integration and pass with the change.
- `make e2e-cover` passes: every changed non-test line is executed by those scenarios.

## Not touched

<!-- From the proposal's Impact. -->
