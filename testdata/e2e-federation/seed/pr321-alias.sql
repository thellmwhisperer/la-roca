-- Declared reproduction of pull request 321. The session id is the public issue identity.
INSERT INTO sessions
  (session_id, source_agent, source_surface, started_at, ended_at,
   duration_minutes, title, project, metadata, machine)
VALUES
  ('019aba72-aa57-7d93-a12c-b6e65c0dca6b-history-envelope-alias', 'codex', 'Codex CLI',
   '2025-11-25T09:57:52Z', '2025-11-25T16:18:52Z', 381, NULL, '.codex', '{}', 'hub'),
  ('019aba72-aa57-7d93-a12c-b6e65c0dca6b', 'codex', NULL,
   NULL, NULL, NULL, NULL, NULL, '{}', 'hub');
