# Retrieval evaluation

The retrieval evaluation is a ruler for prompt and query changes. Its fixture
is synthetic and embedded in the binary, so the default run is independent of
the operator's corpus, model configuration, and network access.

```sh
make eval
make eval EVAL_FORMAT=json
make eval EVAL_FORMAT=markdown
make eval EVAL_MODE=live EVAL_PROVIDER=codex EVAL_MODEL=gpt-5.6-sol EVAL_FORMAT=markdown
make eval EVAL_CASES=/private/owner-cases.json EVAL_DB=/private/roca.db EVAL_FORMAT=json
```

Replay is the deterministic default. It executes the fixed SQL in
`internal/evaluation/testdata/recorded_plans.json` against the synthetic rows
in `internal/evaluation/testdata/fixture.sql`. Live mode requires an explicit
provider and accepts an optional model; it does not read operator configuration.
The selected transport generates every plan against that same fixture. Every
live case and the report summary name the provider and model that produced its
plan, so runs from different model configurations remain attributable.

A live case is measuring a model, so a plan the provider fails to produce and a
plan that passes the SELECT gate but cannot execute are both that case's miss:
the error is kept in the case and attempt data, the attempt counts as an empty
query, the human report marks the case `ERROR`, and the run continues to the
last case. Replay has no model to measure, so a recorded plan that cannot run is
a harness failure and stops the run. A live provider that resolves to nobody is
a configuration failure and is refused before the run starts.

Every successful replay and live run automatically writes a credential-safe
JSONL record under `<work-dir>/logs/eval-YYYY-MM-DD.jsonl` (the default is
`.tmp/eval/logs/`) and prints that path at the end. The record carries the UTC
timestamp, mode, producer labels, complete case and attempt data including the
returned columns and rows, and the exact human, Markdown, and JSON renderings.
Multiple runs on the same day append separate records.

The golden set is `internal/evaluation/testdata/golden.json`. The file is an
envelope of `schema_version` (always `1`), `fixture` naming the corpus, and a
`cases` array:

```json
{ "schema_version": 1, "fixture": "synthetic-v1", "cases": [] }
```

Every case has a unique `id` shared with its recorded plan and a reporting
`category`; its stable retrieval contract is `question`, `expected_kind`, and
the ID-independent `expected_marker`:

```json
{
  "id": "person-approval",
  "category": "person",
  "question": "Who approved the Aurora launch?",
  "expected_kind": "row_contains",
  "expected_marker": "Nora Vale approved the Aurora launch"
}
```

Cases may also declare `rescue_path`, an ordered list of follow-up questions.
The harness stops at the first attempt whose expected marker appears in the
top five and reports how many queries it took. Markers are independent of
database IDs:

- `row_contains` finds a content fingerprint in any returned field.
- `field_equals` checks a named field such as `source_agent=codex`.
- `count_gt` and `count_equals` check a returned count.

## Personal corpus

`--cases <path>` and `--db <path>` are an explicit pair for running the same
contract against an operator-owned corpus. The case file uses exactly the
schema above and remains outside the repository. The named La Roca database is
opened with SQLite `mode=ro`, is never created, adopted, chmodded, or journaled,
and every generated or replayed statement still passes through the SELECT
gate. Logs and any live-provider runtime files go only to `--work-dir`, never
beside the personal database.

Live mode needs only the case file and its explicit provider/model. Replay mode
also reads fixed SQL from a private sidecar beside the cases. For
`/private/owner-cases.json`, name it `/private/owner-cases.plans.json` and use
the committed `recorded_plans.json` as the schema example. The sidecar must
contain one SQL statement for the main question and each declared rescue path.

The Makefile passes these flags through as `EVAL_CASES` and `EVAL_DB`:

```sh
make eval EVAL_CASES=/private/owner-cases.json EVAL_DB=/private/roca.db
make eval EVAL_MODE=live EVAL_PROVIDER=ollama EVAL_MODEL=qwen3.5:4b \
  EVAL_CASES=/private/owner-cases.json EVAL_DB=/private/roca.db
```

The embedded synthetic fixture and golden set remain the default used by CI
and public baseline numbers. Personal case files, plan sidecars, databases, and
their report logs must not be committed.

The report includes hit@1 and hit@5, the share of executed queries that
returned zero rows, mean queries-to-answer for successful rescue cases,
answered/declared rescue coverage, and wall time for every case and the full
run. hit@1 is the first question's top row only: a rescue attempt that puts
the marker first never counts towards it, so the number stays comparable when
rescue paths change. hit@5 is the marker in the top five of any attempt, and
rescue success is reported by queries-to-answer.

Three cases intentionally miss the recorded baseline: an unknown-word
paraphrase, a typo, and a term present only in a thinking block. They keep
measurable headroom in the suite without making `make eval` fail.

`EVAL_FORMAT=json` is the stable machine envelope. The human and Markdown
formats carry the same summary; the Markdown block is ready to paste into a
README or release note. Returned values and logs use the product's standard
credential redaction, so the saved record is the same safe source rendered to
the terminal.
