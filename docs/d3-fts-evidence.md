# D3: persistent FTS routing

The paired executable test on a disposable migrated lab used 30k synthetic
exchanges, about 90 MB of repeated text. The published v1.84.0 executable took
about 420 ms for the qualified MATCH count; this branch took about 40 ms for
that same query on the same lab. Both returned all 30k matches. These are whole
CLI process times after a separate installer warmup, with setup excluded.
No live federation was measured.

Reproduce after `make build`:

```sh
ROCA_FTS_PUBLISHED_BIN=<pinned-v1.84.0-binary> go test -tags acceptance ./test/acceptance -run '^TestCostQualifiedFTSCLI$' -count=1 -v
go test ./internal/provider/service -run '^TestCostHubFTS$' -count=1 -v
```

The paired test rejects a published baseline that meets the 200 ms budget and
a branch that misses it. Its transcript is retained at
`.tmp/fts-evidence/comparison.txt`; the published binary is opt-in and pinned,
while the branch regression runs in ordinary acceptance.

The service test separately exercises qualified corpus MATCH queries on 10k
synthetic exchanges and observes the actual connection's `sqlite_temp_master`.
It measured about 5–6 ms per query with zero temporary FTS objects. It also
checks the qualified suggestion for unqualified SQL, historical memory IDs,
and that internal compatibility search creates no temporary FTS tables.
The remaining compatibility objects are views forwarding SQLite's FTS handle,
including its bm25 rank, to the persistent owning index. Rank statistics now
come from that index's population. Shadow comparison is retired as described
in [migration operations](operations.md#explicit-data-split-migration).
