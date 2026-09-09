# D7 batch indexing evidence

Revised acceptance passes on this lab: **six embedding transactions**, below the limit of **seven**, and **no time regression** across three alternating warm pairs (median **8.8s published → 7.5s branch**). The owner withdrew the original 3x requirement as incorrectly set; the measured speedup is **1.17x**.

Isolated macOS ARM lab: two synthetic SQLite databases, 130 and 131 one-word sources, one chunk per source, no existing embeddings. Both executables used the same pinned model and Metal backend, with GOMAXPROCS=2; memory peaked around 1.2 GB. No operator federation or installed SSH wrapper was used. The source SQL seat is the same fixture-only read-only Python runner for both executables; embeddings run in the actual native binaries.

Published baseline: release v1.83.1, executable SHA-256 `d0a43815ecf83caf9320d75d5cd290eb1aacde10d046455baaea25f3084e31f9`.
Branch executable SHA-256: `7cf1e555592e1f0f0d706dd4cdf5d3c220866c234909caae181f5fc3317725b5`.

| Run | Wall seconds | Embedding transactions | Native load seconds | Native embed seconds | Peak GB |
|---|---:|---:|---:|---:|---:|
| published-3 | 10.3 | 261 | 0.5 | 6.5 | 1.2 |
| branch-1 | 19.3 | 6 | 11.3 | 6.3 | 1.2 |
| published-warm | 9.1 | 261 | 0.6 | 6.2 | 1.1 |
| branch-warm | 7.6 | 6 | 0.4 | 6.2 | 1.2 |

Each embedding INSERT/UPDATE fires a fixture-only trigger updating a single audit page. A pinned read transaction prevents WAL recycling; the harness counts WAL commit markers whose transaction includes that page. These are counted commits, not embedding-call estimates. Published: 130 + 131 transactions. Branch: 3 + 3, below ceil(261/64) + 2 = 7. Native telemetry independently records 261 single calls versus six batches (64, 64, 64, 64, 3, 2).

The first branch native load took around 11 seconds; its complete timing is retained above. The warm comparison includes ordinary model load and CLI startup. Native inference still takes most of the warm run; the observed improvement is 1.17x, and the revised acceptance requires no time regression. A short interrupted CPU smoke run during compilation was excluded from timing evidence. The warm published run overlapped briefly with focused test compilation; this does not establish an isolated timing acceptance pass either.

Reproduce, with each label unused and no concurrent builds or tests:

```sh
gh-axi release download v1.83.1 --pattern 'roca-vector-*-darwin-arm64.tar.gz' --dir .tmp/d7-lab
# Extract the executable into .tmp/d7-lab/published.
gh-axi release download models-v1 --pattern 'nomic-embed-text-v2-moe.f16.gguf' --dir .tmp/d7-lab/model
GOMAXPROCS=2 GOFLAGS=-p=2 make -C plugins/vector build-native LLAMA_JOBS=1 BIN=../../.tmp/d7-lab/branch
python3 plugins/vector/scripts/measure-batches.py .tmp/d7-lab published-check .tmp/d7-lab/published/roca-vector .tmp/d7-lab/model/nomic-embed-text-v2-moe.f16.gguf
python3 plugins/vector/scripts/measure-batches.py .tmp/d7-lab branch-check .tmp/d7-lab/branch .tmp/d7-lab/model/nomic-embed-text-v2-moe.f16.gguf
```

Each label directory retains `evidence.json`, CLI stdout/stderr and native telemetry. Rerun with fresh labels for warm timing; compare total wall seconds, not just selected phases. Lab-local Go and CMake tools were used because neither was on PATH; the native build used the Makefile-pinned llama.cpp commit, Release mode, Metal and Accelerate.

Validation passed: `make -C plugins/vector check`; focused CGO-enabled batch cost, progress rollback, scheduler ordering, native abort/trap and worker recovery tests; dragon forbid gate; public-text check. The cost/resume test counted six transactions across a canceled pass and its restart, retained exactly 64 committed chunks, then embedded only the remaining 197. A source-progress insertion failure rolls back chunks, both embedding indexes and source records together. The scheduler ordering test now sends 64-item batches.

## Sequential warm follow-up

All owned builds/tests had completed before these fresh-fixture runs. The fixture, binaries, model, backend and GOMAXPROCS remained unchanged. Order: published then branch; branch then published; published then branch. Foreign host workloads (including the already loaded speech and model services) were left unchanged by this worker; no foreign process was stopped or profiled. This is a shared host, not proof of constant foreign resource usage.

| Run | Wall seconds | Embedding transactions | Native load seconds | Native embed seconds | Peak GB |
|---|---:|---:|---:|---:|---:|
| published-isolated | 9.0 | 261 | 0.5 | 6.0 | 1.1 |
| branch-isolated | 7.7 | 6 | 0.5 | 6.3 | 1.2 |
| pair-1-published | 9.1 | 261 | 0.5 | 6.1 | 1.1 |
| pair-1-branch | 7.5 | 6 | 0.4 | 6.1 | 1.2 |
| pair-2-published | 8.8 | 261 | 0.4 | 6.2 | 1.2 |
| pair-2-branch | 7.5 | 6 | 0.5 | 6.0 | 1.2 |
| pair-3-published | 8.7 | 261 | 0.4 | 6.1 | 1.2 |
| pair-3-branch | 7.5 | 6 | 0.5 | 6.1 | 1.2 |

Medians use only the three requested alternating pairs:

- published: wall 8.8s; native load 0.4s; native embedding 6.1s.
- branch: wall 7.5s; native load 0.5s; native embedding 6.1s.

Measured median speedup: **1.17x**, satisfying the revised no-time-regression requirement. Every published run counted 261 embedding transactions; every branch run counted six, below seven.

Inference from this fixture: holding the observed native embedding duration (about 6.1s) fixed, eliminating all remaining storage/scheduling overhead would still miss the roughly 2.9s target implied by the withdrawn 3x requirement. This is an inference about these observations, not a universal speedup ceiling across hardware or workloads. The initial pair above is not a controlled cold-cache comparison; its slow first branch load remains visible rather than being silently discarded.

Owner decision: accept D7 against the transaction limit and no time regression, and withdraw the 3x gate. The [D7 record](../.slop/dragons/D7.yaml) owns the removal rule and forbid; [Index declared databases](vector.md#index-declared-databases) owns the operator contract. No native-throughput investigation or other wave work is authorized. All measurements remain unchanged; this evidence accompanies the scoped patch into validation.
