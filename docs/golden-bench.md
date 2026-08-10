# The golden query bench

The bench is what turns "search got better" into a number. It runs a frozen set
of questions, each with its own declared relevance criterion, against every
search method, and publishes the score for each one.

Without it, changing how La Roca searches is a bet.

## Nothing of yours travels in anyone's binary

**The binary ships the format and the runner. It ships no questions.** Your bench
is a data file on your own disk, generated from your own corpus, and it stays
there. This is a product rule, not a preference: a public binary cannot carry
anybody's private vocabulary (PRD C5, the decision of 2026-08-05).

`roca calibrate` generates one from your corpus, and `roca init` calls it once
as a bootstrap step. A bench can also be written by hand, which is exactly what
the format is designed for. How the generator builds a case, and why it proves
every one of them before publishing it, is in `docs/lifecycle.md`.

`internal/bench/testdata/seed.yaml` is a contrast seed derived from
`features/05_golden_bench.feature`. It is a test fixture, not a product
artifact: its questions and sentinels belong to the synthetic seeded world of the
acceptance suite.

## Running it

```
roca bench golden <file>                      # both methods
roca bench golden <file> --method fts         # only some
roca bench golden <file> --json               # machine-readable
```

Exit code is `0` when every case passed and `2` when any case failed, so a script
can use it as a release gate. A bench with red cases is not a command failure: it
is the command's answer.

Output is compact, one key per line, tables declaring their columns:

```
bench: golden.yaml
cases: 25
scores[2]{method,passed,score,p50,p95}:
  like      12/25     48%   1.4 s   2.0 s
  fts       20/25     80%    1 ms    8 ms
failures[5]{method,case,why}:
  like     protocolo-qwen-27b  no row carries the sentinel "Qwen3.6-27B"
  ...
```

Every case runs against every method and every case gets its own verdict. A case
that blows up counts as its own failure, with its reason, and the run continues:
a bench that stops at the first red cannot tell you how many reds there are.

## The competitors

| Method | What it is |
|---|---|
| `like` | The reference floor: `LIKE '%term%'` over every text column. No index. |
| `fts` | SQLite FTS5 full-text index, ranked by bm25. |

`like` is first and not out of nostalgia: it is the baseline that says whether
the index bought anything. A score with nothing to compare against says nothing.

## File format

```yaml
version: 1                      # required; a newer version is refused, not guessed
generated_at: "2026-08-05T12:00:00Z"
generator: manual-wave3-search    # who wrote it, so two scores can be compared
notes: >
  Free text.
corpus:                         # what it was generated against
  memories: 1879
  exchanges: 254631
  db_path: .tmp/real-corpus.db
cases:
  - id: term-model-in-production          # required, unique
    question: which reasoning model does roca use in production
    expect_rows_contain: ["qwen3.5:4b-roca"]
    expect_min_rows: 1
    max_latency_ms: 1500
    source: sampled from the pattern layer
```

Every criterion is optional except `id` and `question`. A case that declares
nothing still runs and still proves the query does not blow up.

| Field | Meaning |
|---|---|
| `expect_path` | `compiler`, `refused`, `unresolved`, `llm_fallback`, `keyword_fallback` |
| `expect_template` | The template the compiler must pick |
| `expect_refusal` | `out_of_scope` or `ambiguous`, when a refusal is expected |
| `expect_rows_contain` | Sentinels: **each** must appear in **some** returned row |
| `expect_min_rows` / `expect_max_rows` | Row-count bounds |
| `expect_empty` | The correct answer is nothing at all |
| `max_latency_ms` | Latency ceiling for this case |
| `source` | Where the case came from. Nobody judges it; it tells you who to ask |

Sentinels are matched folded: lower-cased, diacritics stripped, punctuation
treated as a separator, exactly the way the index tokenizes. So `guiones largos`
matches `GUIONES LARGOS`.

A case reports **every** criterion it broke, not just the first one. Fixing a case
knowing only the first reason means fixing it blind and running the bench again
to discover the next.

## Writing cases that measure something

Two rules earned the hard way while writing the first real bench:

**Word the question differently from the text it must find.** A bench whose
questions repeat the searched text verbatim measures `LIKE`, not the index. The
cases that separate the two competitors are the ones where the question and the
answer share no words.

**Keep aggregate cases as controls.** `how many memories are there` returns the
same number down any route. Those cases prove the bench itself did not break; they
just do not rank the competitors.

## Versioned, regenerated rarely

Benches are versioned and a new one never overwrites the old one, so historical
scores stay comparable. `roca calibrate` writes `golden-0001.yaml`,
`golden-0002.yaml`, … under `~/.roca/bench`, and never reuses a name.

Regeneration is by milestone (substantial corpus growth, a new dominant project,
roughly every six months), suggested by `doctor`, never automatic. `roca init`
generates the first bench and no other: a second init reports the one it found
and leaves it alone. The one milestone that is measured rather than judged is
corpus growth: doctor asks for a new bench when the memory count has tripled
since the current one was generated, and it prints both numbers.
