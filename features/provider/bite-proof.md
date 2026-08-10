# Provider acceptance bite proof

Each mutation below was applied to production code only for the targeted run, produced the recorded red result, and was reverted immediately afterward. The final tree contains none of these mutations.

| Feature | Scenario | Temporary mutation | Red evidence |
|---|---|---|---|
| `config.feature` | The provider order in config decides who is asked first | `internal/provider/catalog.go`: replace the configured order with `DefaultOrder()` | Targeted acceptance failed: provider order was `codex, ollama`, wanted `ollama, codex` (exit 1). |
| `query.feature` | --sql-only prints the SQL and touches nothing | `internal/provider/service/llm.go`: bypass the `req.SQLOnly` rescue boundary | Targeted acceptance failed because one matching row and `match: found` came back (exit 1). |
| `credentials.feature` | An API-key login stores the key under the data directory at 0600, and never prints it | `internal/provider/apikey.go`: chmod the saved key to `0644` | Targeted acceptance failed: credential mode was `644`, wanted `600` (exit 1). |

The targeted command shape was `go test -tags=acceptance ./test/acceptance -run 'TestProviderAcceptanceSuite/<scenario>' -count=1`, after rebuilding the real binary for each mutation.
