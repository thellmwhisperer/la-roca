# Provider capability delta inventory

The approved 13 scenarios claim deterministic configuration, selection diagnosis, no-model degradation, the coarse SQL boundary, and the xAI API-key lifecycle. The provider code also contains the following capabilities that no approved scenario claims.

| Unclaimed capability | Where it lives |
|---|---|
| Successful model-generated SQL and successful model-generated prose | `internal/provider/service/llm.go` (`llmStage`, `Interpret`) |
| One correction attempt after the SQL gate rejects model output | `internal/provider/service/llm.go` (`correction`, `retriesOnRejection`) |
| Distinct rescue reasons for provider errors, invalid SQL, and SQL execution errors | `internal/provider/service/llm.go` |
| A provider that becomes faulty after readiness does not silently fall through to the next provider | `internal/provider/provider.go` and `internal/provider/service/llm.go` |
| Codex subscription chat, response parsing, account headers, and model catalogue | `internal/provider/codex.go` |
| Ollama chat, tags catalogue, keep-alive, and installation-specific remedies | `internal/provider/ollama.go` |
| DeepSeek, Z.ai, xAI, and custom OpenAI-compatible chat/catalogue adapters | `internal/provider/openai.go` and `internal/provider/catalog.go` |
| Environment order precedence, duplicate rejection, explicit disable aliases, and unknown environment provider errors | `internal/provider/catalog.go` and `internal/provider/provider.go` |
| Provider request/probe timeouts beyond their use as fast test budgets | `internal/provider/provider.go` and `internal/provider/config/file.go` |
| Config keys `preset`, `api_key`, `api_key_env`, `keep_alive`, `timeout_ms`, and `probe_ms` | `internal/provider/config/file.go` and `internal/provider/catalog.go` |
| Legacy root/default config lookup and `ROCA_CONFIG` relocation | `internal/provider/config/file.go` and `internal/provider/config/config.go` |
| Model switching, model-order editing, and preservation of unrelated TOML | `internal/provider/config/file.go` and `internal/distribution/cli/models.go` |
| Codex browser OAuth with PKCE/state, loopback callback, token exchange, refresh, storage, and logout | `internal/provider/oauth/`, `internal/provider/codex.go`, and `internal/distribution/cli/models.go` |
| API-key login/logout for DeepSeek and Z.ai, plus stored/file/environment credential precedence | `internal/provider/apikey.go`, `internal/provider/catalog.go`, and `internal/distribution/cli/models.go` |
| Provider catalogue output through `roca login`, `roca models`, and `roca model set` | `internal/distribution/cli/models.go` |
| Doctor's credential-presence, agent-detection, prompt, memory-count, version, and source fields | `internal/provider/service/doctor.go` |
| Schema-aware model prompt construction, joins, layer hints, FTS examples, and substring-LIKE rejection | `internal/provider/query/prompt.go` |
| SQL gate function allowlist, schema/table/column checks, hidden tables, chained-statement rejection, JOIN validation, and oversized LIMIT clamping | `internal/provider/query/sqlgate/` |
| Exact and lenient FTS compilation, LIKE fallback, term normalization, wildcard escaping, and layer exclusion | `internal/provider/query/fts.go`, `internal/provider/query/text.go`, and `internal/provider/service/llm.go` |
| Layer aliases, ingest admission, coordination groups, classifier labels, and the full embedded layer registry | `internal/provider/layers/layers.go` and `data/layers.yaml` |
| Row deduplication/relevance ordering, text budgets, and count narration | `internal/provider/service/query.go` |
