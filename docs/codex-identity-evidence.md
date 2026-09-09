# Codex source identity repair

Issue [324](https://github.com/thellmwhisperer/la-roca/issues/324) concerns an
already-harvested thread whose exchanges and tools live under different session
keys. Its stored `codex_thread_id` and rollout path agree, but its source ID is
absent from the served sessions.

The earliest divergence demonstrated by the reproduction is the persisted
primary key, before the next ingest. Fresh published ingestion of the same
history and fossil rollout keeps the exact parser ID. Replacing only those
served rows with the inherited sibling shape, while retaining clean fingerprints
and history cursors, leaves the siblings untouched on published v1.84.8. The
historical operation that originally minted the suffixes is not established by
this experiment. Neither the current parser nor the current writer creates them.

`internal/ingest/codex_identity.go` reconciles that inherited identity before
unchanged-source skipping. It requires matching stored source identity and a
rollout path, not just a common stem. Children move within one transaction; their
row IDs, payloads, failures and session-level orphan status remain. Numbered
children keep their original exchange relationships, with collisions assigned
unused numbers. Shared source keys retain the canonical binding without deleting
other historical child rows. Conflicting rollout identities fail and roll back.
All sibling envelopes are retired before the final metadata union is written,
so an intermediate envelope cannot collide with a later sibling's exact guard.
Dry-run leaves the corpus untouched. This does not change the Codex parsers or
their reading versions, and it does not deduplicate child content.

## Reproduce the published comparison

```sh
mkdir -p .tmp/issue324-control
gh-axi release download v1.84.8 --pattern roca-v1.84.8-darwin-arm64 \
  --dir .tmp/issue324-control
chmod +x .tmp/issue324-control/roca-v1.84.8-darwin-arm64
ROCA_CODEX_PUBLISHED_BIN="$PWD/.tmp/issue324-control/roca-v1.84.8-darwin-arm64" \
  make codex-identity-test
```

The executable test creates its own isolated home. Its only incident identity is
the one already public in the issue; prompts, calls and failures come from
`internal/ingest/testdata/codex-stem-split.sql`. It first checks the healthy fresh
published path, then keeps its watermarks and installs the declared inherited
shape. Published and branch execute sequentially on that same lab corpus.

The control retains two siblings and no exact source session. The branch yields
one exact source session, retaining all six exchanges and 48 tools, including
every session-level tool and the synthetic failure. An unrelated healthy thread
remains. The repeat pass is unchanged. The test compares every child row's ID and
payload digest with only the intended parent-key substitution normalized away.
Reported ingest durations are paired with equality of logical threads, child
rows and errors in the same output; I/O bytes and memory cost are unmeasured.
No performance improvement is claimed.

`TestCodexStemSplitIdentity` also covers a pre-existing exact session, a third
sibling whose envelope would collide during a partial merge, unchanged-file
skipping and dry-run. `TestCodexIdentityReferencesAndConflict` covers numbered
orphan isolation, thinking and memory references, exact-payload guards and atomic
rollback. The existing Codex history and orphan recovery tests remain applicable.

## Historical lab copy

The implementation was also run against a pre-existing historical lab corpus
copy containing roughly 30,000 physical sessions, 800,000 exchanges and two
million tool rows. No live home or federation was queried or ingested. That older
lab retained an extra empty exact-ID parent; removing only that empty parent
recreated the public issue's absent-ID starting state. Its children were already
split between the two sibling keys. Source roots for this comparison were empty;
the declared executable fixture above separately checks history, fossil rollout
and watermarks through the published parser.

Published v1.84.8 left the two siblings unchanged. The branch produced the exact
source session with the same six exchanges and 48 session-level tools. Across
the entire copied corpus, the logical thread set, all child row counts, row IDs,
payload digests, exchange ownership relationships, tool failure count and stored
file errors were equal before and after. File-state rows were unchanged. Physical
session count decreased only through identity reunification, and the repeated
ingest had zero delta. Both comparison ingests reported zero errors and zero
failed writes. I/O bytes, memory use and initialization cost are unmeasured;
these results establish preservation and identity correction, not a speedup.
