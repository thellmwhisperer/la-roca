INSERT INTO sessions
  (session_id, source_agent, project, started_at, ended_at, duration_minutes, title)
VALUES
  ('syn-aurora-001', 'codex', 'aurora', '2026-01-10T09:00:00Z', '2026-01-10T09:42:00Z', 42, 'Aurora launch retrospective'),
  ('syn-aurora-002', 'claude-code', 'aurora', '2026-02-18T14:00:00Z', '2026-02-18T14:31:00Z', 31, 'Aurora rollback review'),
  ('syn-orbit-001', 'opencode', 'orbit', '2025-11-03T10:00:00Z', '2025-11-03T10:55:00Z', 55, 'Orbit migration planning'),
  ('syn-nebula-001', 'claude-code', 'nebula', '2026-03-05T08:30:00Z', '2026-03-05T09:05:00Z', 35, 'Nebula release planning'),
  ('syn-harbor-001', 'pi', 'harbor', '2026-04-22T16:00:00Z', '2026-04-22T16:28:00Z', 28, 'Harbor security review'),
  ('syn-comet-001', 'codex', 'comet', '2026-05-01T11:00:00Z', '2026-05-01T11:20:00Z', 20, 'Comet approval');

INSERT INTO memories
  (layer, content, origin, source_agent, source_session, source_sequence, project, status, created_at)
VALUES
  ('decision', 'Nora Vale approved the Aurora launch after the final smoke test.', 'agent', 'codex', 'syn-aurora-001', 1, 'aurora', 'active', '2026-01-10T09:40:00Z'),
  ('project', 'Iris Chen led the Orbit migration and documented the cutover.', 'agent', 'opencode', 'syn-orbit-001', 1, 'orbit', 'active', '2025-11-03T10:50:00Z'),
  ('decision', 'Aurora chose SQLite WAL for predictable local concurrency.', 'human', 'codex', 'syn-aurora-001', 2, 'aurora', 'active', '2026-01-10T09:41:00Z'),
  ('project', 'Aurora kept a seven-day observation window before general release.', 'agent', 'claude-code', 'syn-aurora-002', 1, 'aurora', 'active', '2026-02-18T14:10:00Z'),
  ('discovery', 'The Aurora rollback rehearsal restored service in seven minutes.', 'agent', 'claude-code', 'syn-aurora-002', 2, 'aurora', 'active', '2026-02-18T14:20:00Z'),
  ('handoff', 'Aurora handoff: monitoring is green and the release owner has the checklist.', 'agent', 'claude-code', 'syn-aurora-002', 3, 'aurora', 'active', '2026-02-18T14:30:00Z'),
  ('decision', 'Jules Hart approved Project Comet after the accessibility review.', 'human', 'codex', 'syn-comet-001', 1, 'comet', 'active', '2026-05-01T11:15:00Z'),
  ('project', 'The Harbor security review was scheduled for 30 April 2026.', 'agent', 'pi', 'syn-harbor-001', 1, 'harbor', 'active', '2026-04-22T16:20:00Z'),
  ('project', 'Mara Quinn was the release coordinator for Nebula.', 'agent', 'claude-code', 'syn-nebula-001', 1, 'nebula', 'active', '2026-03-05T08:45:00Z'),
  ('project', 'The Nebula release schedule moved to Friday after the dependency audit.', 'agent', 'claude-code', 'syn-nebula-001', 2, 'nebula', 'active', '2026-03-05T08:55:00Z');

INSERT INTO exchanges
  (session_id, exchange_number, human_text, agent_text, human_timestamp, agent_timestamp,
   response_latency_ms, model, provider, tokens_in, tokens_out, tokens_reasoning, cost_usd)
VALUES
  ('syn-aurora-001', 1, 'What was the launch latency?', 'The launch answer recorded 180 ms at p95.', '2026-01-10T09:10:00Z', '2026-01-10T09:10:01Z', 900, 'gpt-5.4', 'codex', 120, 45, 10, 0.003),
  ('syn-aurora-001', 2, 'Who approved it?', 'Nora Vale approved the launch answer.', '2026-01-10T09:20:00Z', '2026-01-10T09:20:01Z', 850, 'gpt-5.4', 'codex', 90, 30, 8, 0.002),
  ('syn-orbit-001', 1, 'Summarize the migration.', 'Iris owns the cutover checklist.', '2025-11-03T10:30:00Z', '2025-11-03T10:30:02Z', 1400, 'claude-sonnet-4', 'claude', 200, 60, NULL, 0.004),
  ('syn-harbor-001', 1, 'What remains?', 'The security review remains before cutover.', '2026-04-22T16:10:00Z', '2026-04-22T16:10:03Z', 2400, 'qwen3.5:4b', 'ollama', 180, 55, 20, NULL);

INSERT INTO thinking_blocks
  (session_id, exchange_number, position_in_session, depth, caution_ratio, word_count, is_after_compaction, full_text)
VALUES
  ('syn-harbor-001', 1, 1.5, 'deep', 0.2, 12, 0,
   'The cobalt compass meant delaying the Harbor cutover until the audit closed.');
