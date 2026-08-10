# Ingest acceptance bite proof

All mutations below were temporary, run against the real built binary, observed
red in the named scenario, and reverted before the final gate.

| Scenario | Temporary mutation | Observed failure |
|---|---|---|
| `Every session records which agent family wrote it, from the supported list` (`attribution.feature`) | Changed the Claude session target in `internal/ingest/scan.go` from `claude` back to `claude-code`. | `session source "claude-code" is outside the supported roster` |
| `A malformed file is skipped and counted, never fatal` (`parsing.feature`) | Disabled the all-invalid-JSONL rejection in `internal/ingest/parsers/claude.go`. | The command stayed successful but reported `errors=0 details=0`, where the scenario requires `1/1`. |
| `A file whose fingerprint is unchanged is not even opened` (`incremental.feature`) | Forced `internal/ingest/state.go:Unchanged` to return false. | The protected file was opened: `files_skipped=0 errors=1`, where the scenario requires `1/0`. |

Targeted command shape used for each proof:

```text
make build
go test -tags=acceptance ./test/acceptance -run 'TestIngestDomainSuite/<scenario>' -count=1
```
