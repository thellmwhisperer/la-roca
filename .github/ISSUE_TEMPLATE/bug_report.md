---
name: Bug report
about: Report a reproducible problem
---

<!-- Public text: use ~, $TMPDIR, <workspace>, <host>, <lan-ip> and rounded counts; omit personal paths, hostnames and session identifiers. -->

Type: fix
Where: binario|lanzador

## What breaks

<!-- One or two sentences: what happens and what should happen instead. -->

Proof: <!-- a run id, a log line or the command that reproduces it -->

## Done when

<!-- One test, or the exact command that goes from red to green. If it takes five tests, it is five issues. -->

`go test -count=1 -run 'TestName' ./pkg` fails on integration and passes with the fix.

## Not touched

<!-- Three lines at most. -->

- 
