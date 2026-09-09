# D8/D9 source generation evidence

Synthetic macOS ARM lab, eight declared text rows containing leading whitespace
and one word each, about 64 MiB total. Both native executables completed actual
indexing with the pinned model and produced eight embedded chunks. No operator
federation or installed SSH wrapper was invoked. Commands ran sequentially with
GOMAXPROCS=2; the native branch build reused the pinned llama.cpp libraries.

Published: v1.83.1, SHA-256
`d0a43815ecf83caf9320d75d5cd290eb1aacde10d046455baaea25f3084e31f9`.
Branch executable: SHA-256
`23919c1f8e783cd194a6b259f93f15ff281894bc694ba3860efa25834c672eb9`.

| Measurement | Published | Branch |
|---|---:|---:|
| Source bytes per unchanged pass, three runs | 100% | 0% |
| Source bytes per status, three runs | about 100% | 0% |
| Explicit `ingest --delta --verify` source bytes | flag unavailable | 100% |
| Uninstrumented status latency, three runs | 891 / 271 / 267 ms | 47 / 12 / 12 ms |
| Exact candidate / embedded counts | 8 / 8 | 8 / 8 |

Source reads were counted with a task-private Darwin interposer around `read`
and `pread`, filtering descriptors to the synthetic source database and WAL.
It records returned bytes, including cached reads, rather than physical disk
traffic. Published reads and branch explicit verification both consumed the
entire source, providing positive controls for the zero-byte measurements.
Timing uses a separate three-pair run without interposition: the counter's
per-read logging overhead is deliberately excluded from latency evidence.
These are full `status --json` process wall times, including startup and exit.
All branch status calls meet the 300 ms acceptance bound.

Initial indexing took about 3 seconds published and 4 seconds on the final
branch executable. An earlier branch setup took about 15 seconds; all
setup observations remain in the retained transcript; this comparison makes
no initial-indexing throughput claim. An initial fixture missing owner metadata
and a recursive interposer probe failed before measured runs; both were fixed
in the private harness and retained separately.

The task data directory retains the native binaries, synthetic source and
sidecar databases, fixture runner, counter source/library, CLI outputs,
individual byte logs and JSON timing records. The harness creates isolated
HOME, state and model paths and never discovers a real federation. Reproduce
by running the retained `measure.py` with a fresh label and each executable,
then `status-latency.py` for the uninstrumented comparison.

`TestCostUnchangedSourceAndStoredStatus` enforces the byte budget on a 32 MiB
synthetic database: full hash inputs are counted and source SQL is forbidden;
on Linux `/proc/self/io` additionally bounds all logical process read bytes,
including sidecar overhead. It requires successful initial indexing, exact
stored status counts, status below 300 ms, and a full hash on verification.
`TestCompletedGenerationInvalidation` covers WAL commits, replacement,
timestamp-preserving restores, partial/interrupted passes, concurrent source
changes, contract changes and legacy count metadata. `TestStatusDoesNotReadDeclaredText` rejects a
return to status-driven source evaluation. Existing batch/cancellation tests
remain part of the vector plugin check.
