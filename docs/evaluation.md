# Retrieval evaluation

The retrieval evaluation is a ruler for prompt and query changes. Its fixture
is synthetic and embedded in the binary, so the default run is independent of
the operator's corpus, model configuration, and network access.

```sh
make eval
make eval EVAL_FORMAT=json
make eval EVAL_FORMAT=markdown
make eval EVAL_MODE=live EVAL_PROVIDER=codex EVAL_MODEL=gpt-5.6-sol EVAL_FORMAT=markdown
```

Replay is the deterministic default. It executes the fixed SQL in
`internal/evaluation/testdata/recorded_plans.json` against the synthetic rows
in `internal/evaluation/testdata/fixture.sql`. Live mode requires an explicit
provider and accepts an optional model; it does not read operator configuration.
The selected transport generates every plan against that same fixture. Every
live case and the report summary name the provider and model that produced its
plan, so runs from different model configurations remain attributable.

The golden set is `internal/evaluation/testdata/golden.json`. Every case has a
unique `id` shared with its recorded plan and a reporting `category`; its stable
retrieval contract is `question`, `expected_kind`, and the ID-independent
`expected_marker`:

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

The report includes hit@1 and hit@5, the share of executed queries that
returned zero rows, mean queries-to-answer for successful rescue cases,
answered/declared rescue coverage, and wall time for every case and the full
run. Three cases intentionally miss the recorded baseline: an unknown-word
paraphrase, a typo, and a term present only in a thinking block. They keep
measurable headroom in the suite without making `make eval` fail.

`EVAL_FORMAT=json` is the stable machine envelope. The human and Markdown
formats carry the same summary; the Markdown block is ready to paste into a
README or release note.
