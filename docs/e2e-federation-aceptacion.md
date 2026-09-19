# Aceptacion

Fixture: testdata/e2e-federation/frozen.tar.gz extracted into a disposable HOME.
Digest: testdata/e2e-federation/frozen.sha256
Binary: that HOME's .local/bin/roca.
Live hub: never selected.

## 321
command: roca ingest --json
exit: 0
stdout contains: "errors": 0
command: roca exec SELECT session_id, exchange_number FROM plugin_roca_corpus.exchanges WHERE session_id = '019aba72-aa57-7d93-a12c-b6e65c0dca6b' ORDER BY exchange_number --json
exit: 0
stdout contains: 019aba72-aa57-7d93-a12c-b6e65c0dca6b

## 325
command: roca store --layer pill --content Temporary pill for issue 320 acceptance --metadata {"pill_slug":"tmp-x"} --origin agent --agent codex
exit: 0
stdout contains: stored:
command: roca pill
exit: 0
stdout contains: tmp-x
command: roca pill delete tmp-x
exit: 0
stdout contains: deleted: 1
command: roca pill
exit: 0
stdout contains: no active pills
command: roca exec SELECT count(*) FROM plugin_roca_ops.memories WHERE json_extract(metadata,'$.pill_slug')='tmp-x'
exit: 0
stdout contains: 0

## 326
command: roca exec SELECT content FROM plugin_roca_ops.memories WHERE project='budgets' --max-chars 900
exit: 0
stdout digit run: >= 200
command: roca exec SELECT content FROM plugin_roca_ops.memories WHERE project='budgets' --max-chars 900 --json
exit: 0
JSON content length: 800-900 runes

## 315
command: roca mcp serve
repeat: 3
expect: one vector resident process

## 317
command: roca exec SELECT content FROM memories WHERE content LIKE '%PROCESO CARWOW%'
exit: 1
output contains: unqualified table "memories"
output contains: plugin_roca_ops.memories
output contains: plugin_roca_corpus.memories

## 318
command: roca handoff latest --project harbor --limit 1
exit: 0
stdout contains: harbor
command: roca handoff latest --all-projects
exit: 0
stdout contains: harbor
stdout contains: dock

## 427
command: roca exec SELECT id FROM plugin_roca_ops.memories WHERE id = '1152921504606846980' --json
exit: 0
rows[0].id: JS-safe integer of at most 12 digits

## 324
command: roca ingest --json
exit: 0
command: roca exec SELECT session_id FROM plugin_roca_corpus.sessions WHERE session_id LIKE '019aba72-aa57-7d93-a12c-b6e65c0dca6%' ORDER BY session_id --json
exit: 0
stdout contains: 019aba72-aa57-7d93-a12c-b6e65c0dca6b
frozen identity counts: 2 Codex sessions, 1 exact source session, 0 split siblings, 8 exchanges, 48 tools, 48 orphan tools, 1 failed tool, 1 control session
command: roca ingest --json
exit: 0
expect: frozen identity counts unchanged

## 232991
command: roca vector query harbor lantern 20 --databases corpus,ops --json
exit: 0
JSON vector_executed: true
notices do not contain: fts-only
notices do not contain: unavailable

## 233400
command: roca exec SELECT content FROM plugin_roca_ops.memories WHERE content LIKE '%harbor lantern%'
exit: 0

## 233508
command: roca query harbor lantern --json
exit: 0
stdout contains: engines

## 10387
command: roca exec SELECT COUNT(*) AS memories FROM plugin_roca_ops.memories
exit: 0

## 238277
command: roca vector query harbor lantern 20 --databases corpus,ops --json
exit: 0
JSON vector_executed: true
notices do not contain: fts-only
notices do not contain: unavailable

## 244386
command: roca exec SELECT layer, COUNT(*) AS n FROM plugin_roca_ops.memories GROUP BY layer
exit: 0

## 259288
command: roca handoff latest --project harbor
exit: 0
stdout contains: harbor

## 93762
command: roca query harbor lantern --json
exit: 0

## 19944
command: roca handoff latest --project harbor
exit: 0

## 7734
command: roca doctor
exit: 0

## 5740
command: roca vector query harbor lantern 20 --databases corpus,ops --json
exit: 0
JSON vector_executed: true
notices do not contain: fts-only
notices do not contain: unavailable

## 5950
command: roca exec SELECT content FROM plugin_roca_ops.memories LIMIT 1
exit: 0

## 4269
command: roca version
exit: 0
stdout contains: roca

## 4657
command: roca vector query harbor lantern 20 --databases corpus,ops --json
exit: 0
JSON vector_executed: true
notices do not contain: fts-only
notices do not contain: unavailable

## 44508
command: roca query harbor lantern
exit: 0

## 125372
command: roca vector query I inspected the harbor lantern 20 --databases corpus,ops --json
exit: 0
JSON vector_executed: true
notices do not contain: fts-only
notices do not contain: unavailable

## 126485
command: roca exec SELECT content FROM plugin_roca_ops.memories WHERE layer='discovery'
exit: 0

## 127663
command: roca doctor
exit: 0

## 296656
command: roca doctor
exit: 0

## 297007
command: roca doctor
exit: 0

## 1658381
command: roca doctor
exit: 0

## 1708690
command: roca pill show uso-de-la-roca
exit: 0
stdout contains: vectors first

## 1733215
command: roca handoff latest --project harbor
exit: 0

## real-usage-hooks
command: roca hooks run claude
stdin: {"hook_event_name":"SessionStart","tool_name":"","tool_input":{}}
exit: 0
duration_ms: 0

## real-usage-exec
command: roca exec SELECT id, legacy_id FROM plugin_roca_ops.memories WHERE id = '1152921504606846980' --json
exit: 0
duration_ms: < 5000
rows[0].id: JS-safe integer of at most 12 digits
stdout contains: 1152921504606846980

## real-usage-vector
command: roca vector query warm harbor index 1 --databases corpus,ops --json
exit: 0
command: roca vector query harbor lantern 20 --databases corpus,ops --json
exit: 0
duration_ms: < 2000
JSON vector_executed: true
notices do not contain: fts-only
notices do not contain: unavailable

## real-usage-query
command: roca query harbor lantern --json
exit: 0
duration_ms: < 3000
stdout contains: engines
if stdout contains search hybrid: stdout contains vector

## real-usage-handoff
command: roca handoff latest --project harbor
exit: 0
stdout contains: handoffs[1]
stdout contains: harbor
stdout does not contain: handoffs[2]

## real-usage-mcp-handoff
command: roca mcp serve
tool: roca_store
client: glm-5.2 (codex/slopslint-detector-a1)
layer: handoff
exit: tool error
output contains: handoff refused

## real-usage-e2e-smoke
command: make e2e-smoke
exit: 0
expect: published release updates to the branch artefact
expect: updated executable initializes a clean home

## real-usage-mcp-health
command: roca mcp serve
tool: roca_health
exit: 0
stdout contains: health: pass
