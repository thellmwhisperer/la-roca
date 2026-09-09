-- Declared reproduction of issue 324. Only the already-public identity is real.
INSERT INTO sessions(session_id,source_agent,project,metadata) VALUES
('019aba72-aa57-7d93-a12c-b6e65c0dca60','codex','synthetic',
 '{"codex_thread_id":"019aba72-aa57-7d93-a12c-b6e65c0dca6b","codex_rollout_path":"/synthetic/rollout.jsonl"}'),
('019aba72-aa57-7d93-a12c-b6e65c0dca61','codex','synthetic',
 '{"codex_thread_id":"019aba72-aa57-7d93-a12c-b6e65c0dca6b","codex_rollout_path":"/synthetic/rollout.jsonl","source_exchange_ids":{"synthetic-history":{"exchange_number":1}}}'),
('synthetic-correct-thread','codex','synthetic-control',
 '{"codex_thread_id":"synthetic-correct-thread"}');
WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<6)
INSERT INTO exchanges(session_id,exchange_number,human_text)
SELECT '019aba72-aa57-7d93-a12c-b6e65c0dca61',i,'Synthetic prompt '||i FROM n;
WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<48)
INSERT INTO tool_uses(session_id,tool_name,tool_params_summary,had_error,error_message)
SELECT '019aba72-aa57-7d93-a12c-b6e65c0dca60','synthetic_tool','Synthetic call '||i,
 i=1,CASE WHEN i=1 THEN 'synthetic failure' END FROM n;
