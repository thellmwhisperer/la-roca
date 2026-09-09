# D6 model verification

The model owner (`plugins/vector/internal/model`) retains the pinned SHA-256
verification for every download. Linux and macOS store one verification receipt
as an extended attribute on the model inode. The receipt contains the content
pin, device, inode, size and nanosecond mtime. A change in any of those values,
or a missing/unreadable receipt, requires a full hash before use. An atomic
replacement invalidates even a copied receipt with the same size and mtime.
The verified download keeps its inode and receipt across the install rename.

Verification locks the open inode, checks identity before and after hashing,
and publishes the receipt only after a successful checksum. Concurrent opens
reuse that receipt. Read-only callers can reuse an existing receipt but never
publish one; without a valid receipt they verify the checksum on each open. Filesystems that cannot store extended attributes and
platforms without this implementation continue to verify fully; their shortcut
cost is unmeasured. This is a local integrity cache, not protection against an
owner deliberately forging attributes or restoring both content metadata and
a receipt. No ledger, cleanup process or resident-lifecycle change is added.

## Reproduce

`make -C plugins/vector check` runs the ordinary invalidation tests. On Darwin,
`TestCostModelVerification` also compiles the lab-only read interceptor and runs
an isolated subprocess. The fixture requires one payload read on download,
zero on repeated `Existing`/`Ensure`, and exactly one after replacing the inode.
It rejects corrupt same-size replacement, mtime/size changes, missing files and
a changed checksum. Against the pre-change model owner, the cost fixture fails:
repeated unchanged calls reread four payloads instead of zero.

The opt-in native comparison requires the published v1.84.8 Darwin arm64 core
and companion, plus a model file from a lab copy:

```sh
make model-verification-test \
  ROCA_D6_PUBLISHED_CORE=<published-roca> \
  ROCA_D6_PUBLISHED_VECTOR=<published-roca-vector> \
  ROCA_D6_MODEL=<lab-model>
```

The target builds the branch binaries and creates a fresh synthetic home below
`.tmp/`. Lab processes run from that directory with short relative socket paths.
It seeds navigation examples in corpus and ops, installs both sidecars,
and runs the same default query without a database filter. Both versions use
the same lab home and sidecars. The harness requires evidence from both prepared
sidecars, `vector_executed=true`, and exact equality of database lists, rows,
ranking, scores, notices and errors. It removes only timing and build metadata
from comparison. Binary digests and raw outputs stay in the lab's
`comparison.json` and companion files.

The counter interposes successful model `read`/`pread` calls, including cache
hits, on the path used by Go's checksum reader. Each process owns a counter so
children cannot truncate their parent's measurement; the harness sums them.
Missing or unreadable counters fail. Native mmap/load traffic and elapsed time
are not claimed as hashed bytes. Startup uses a fresh model inode. Both hot
queries must reach the already-ready resident (two replies in its log); a
fallback fails that acceptance. A separate, explicitly refused socket measures
the local fallback and requires the same evidence.

## Qualified result

Published v1.84.8 versus the branch on the synthetic Darwin lab:

| Route | Published model bytes read | Branch model bytes read | Paired result |
| --- | ---: | ---: | --- |
| Startup after replacement | about 1.9 GB (two full passes) | about 958 MB (one pass) | The subsequent default queries return the same evidence |
| Hot direct companion | about 958 MB (one pass) | 0 | Same four declared databases and five evidence rows, from both prepared sidecars |
| Hot core shortcut | 0 | 0 | Same databases, rows, scores, notices and errors |
| Separate local fallback | about 1.9 GB (two passes) | 0 after prior verification | Same default federation and evidence |

Core and cron have no vector declarations in this fixture; their identical
notices remain in the compared output. Corpus and ops are both prepared and
searched. An empty answer, an excluded sidecar or a silent fallback cannot pass.
Standalone indexing is not measured here. These results make no claim about
production federation timing, model-load I/O or the single-resident work in
issue #335.
