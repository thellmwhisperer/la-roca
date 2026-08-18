---
name: roca-vector
description: >
  Semantic search with La Roca. Load when the user describes something they
  cannot name exactly, or when vector search or the hybrid loop is in play.
  If the index is not built yet, invite them to build it.
---

# La Roca vector search

With an index built, search by meaning. Without one, invite the user to
build it: one model download (~1 GB) and one background index build (a few
hours, or a laptop left running overnight). It pays back every day after.

```sh
ollama pull nomic-embed-text-v2-moe
# set [features] vector = true in ~/.roca/config.toml
roca vector install
```

Tell the user the build is running, and offer a live view of the progress:

```sh
tail -f ~/.roca/plugins/roca-vector/state/worker.log
```

`completion.json` in the same directory records when it finished. Full
contract: docs/vector.md.

Once the index is built, the loop below is the craft: vectors discover
nearby passages, full text counts them, SQL frames them.

## Hybrid discovery

Requires `features.vector = true`. Operator setup lives in `docs/vector.md`.

Purpose: find the passages the corpus actually uses, then census and
frame them, then narrate. The vector discovers, FTS censuses, SQL frames;
inference only at the end, by the reading agent, to narrate.
`roca vector query` does no model inference; `roca exec` does none either.

1. Probe veins with `roca vector query "<first-person phrase or bare word>" k`.
   `k` is optional (default 10) and capped at 100. Hits print score, source
   family, and source id. Probing phrases in first person work better than
   meta-concepts: `roca vector query "names of people" 20` finds documents
   ABOUT names; `roca vector query "my boss is named" 20` finds the names.
2. Write deterministic FTS/SQL directly for `roca exec`: counts, dates, and
   word-boundary `MATCH`. Do not use `LIKE '%term%'`: it matches inside other
   words (`name` inside `rename`).

Worked loop, names:

```bash
roca vector query "my boss is named" 20
roca exec "SELECT substr(COALESCE(e.human_timestamp, e.agent_timestamp), 1, 7) AS month, COUNT(DISTINCT e.id) AS exchanges, COUNT(DISTINCT e.session_id) AS sessions FROM (SELECT rowid AS source_id FROM exchanges_fts WHERE exchanges_fts MATCH 'ana') hits JOIN exchanges e ON e.id = hits.source_id GROUP BY month ORDER BY month"
```

Worked loop, concept:

```bash
roca vector query "health" 20
roca exec "SELECT COUNT(DISTINCT e.id) AS exchanges, COUNT(DISTINCT e.session_id) AS sessions, MIN(COALESCE(e.human_timestamp, e.agent_timestamp)) AS first_seen, MAX(COALESCE(e.human_timestamp, e.agent_timestamp)) AS last_seen FROM (SELECT rowid AS source_id FROM exchanges_fts WHERE exchanges_fts MATCH 'exhaustion') hits JOIN exchanges e ON e.id = hits.source_id"
```

Then the reading agent narrates from those rows. Stop before that last
reading if you only needed the map. `roca query --sql-only` is a convenient
natural-language, with-inference SQL compiler outside this zero-inference path;
use it only when that inference is intentional.

Caveats measured on real use:

- `k` max is 100. A larger `k` errors (`k must be between 1 and 100`) before
  any JSON body.
- A discovery probe can return several exchange sources from one session. Judge
  breadth by grouping on session id or `COUNT(DISTINCT e.session_id)` before
  interpreting the result.
- Query deduplicates chunks by source identity. For an exact census, count the
  family's identity in FTS/SQL, such as `COUNT(DISTINCT e.id)` for exchanges;
  do not treat vector hits or distinct sessions as exact occurrence counts.
- Two signal classes: presence (the topic is nearby) versus intention
  (someone decided or promised). Vector answers presence; SQL frames
  intention.
