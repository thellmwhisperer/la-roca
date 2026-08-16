CREATE TABLE project (id TEXT PRIMARY KEY, worktree TEXT);
CREATE TABLE session (
  id TEXT PRIMARY KEY,
  project_id TEXT,
  parent_id TEXT,
  directory TEXT,
  version TEXT,
  time_created INTEGER,
  time_updated INTEGER,
  agent TEXT
);
CREATE TABLE message (
  id TEXT PRIMARY KEY,
  session_id TEXT,
  time_created INTEGER,
  time_updated INTEGER,
  data TEXT
);
CREATE TABLE part (
  id TEXT PRIMARY KEY,
  message_id TEXT,
  session_id TEXT,
  time_created INTEGER,
  time_updated INTEGER,
  data TEXT
);
CREATE TABLE todo (
  session_id TEXT,
  content TEXT,
  status TEXT,
  priority TEXT,
  position INTEGER,
  time_created INTEGER,
  time_updated INTEGER,
  PRIMARY KEY (session_id, position)
);
CREATE TABLE event (
  id TEXT PRIMARY KEY,
  aggregate_id TEXT,
  seq INTEGER,
  type TEXT,
  data TEXT
);

INSERT INTO project VALUES ('synthetic-project', '/synthetic/workspace/opal');
INSERT INTO session VALUES (
  'synthetic-opencode-session', 'synthetic-project', NULL,
  '/synthetic/workspace/opal', '9.9.9', 1785542400000, 1785542460000, 'build'
);

INSERT INTO message VALUES (
  'user-1', 'synthetic-opencode-session', 1785542400000, 1785542400000,
  '{"role":"user","time":{"created":1785542400000}}'
);
INSERT INTO message VALUES (
  'assistant-1', 'synthetic-opencode-session', 1785542401000, 1785542402000,
  '{"role":"assistant","parentID":"user-1","time":{"created":1785542401000,"completed":1785542402000},"modelID":"synthetic-model-a","providerID":"synthetic-provider","cost":0.25,"tokens":{"input":12,"output":5,"reasoning":3}}'
);
INSERT INTO message VALUES (
  'assistant-2', 'synthetic-opencode-session', 1785542403000, 1785542404000,
  '{"role":"assistant","parentID":"user-1","time":{"created":1785542403000,"completed":1785542404000},"modelID":"synthetic-model-b","providerID":"synthetic-provider"}'
);
INSERT INTO message VALUES (
  'assistant-3', 'synthetic-opencode-session', 1785542403500, 1785542403500,
  '{"role":"assistant","parentID":"user-1","time":{"created":1785542403500,"completed":1785542402000},"modelID":"synthetic-model-c","providerID":"synthetic-provider"}'
);
INSERT INTO message VALUES (
  'assistant-live', 'synthetic-opencode-session', 1785542405000, 1785542405000,
  '{"role":"assistant","parentID":"user-1","time":{"created":1785542405000},"modelID":"synthetic-live-model"}'
);
INSERT INTO message VALUES (
  'unsupported-1', 'synthetic-opencode-session', 1785542406000, 1785542406000,
  '{"role":"system","time":{"created":1785542406000}}'
);

INSERT INTO part VALUES (
  'part-user-text', 'user-1', 'synthetic-opencode-session', 1785542400000, 1785542400000,
  '{"type":"text","text":"map the synthetic beacon"}'
);
INSERT INTO part VALUES (
  'part-agent-text', 'assistant-1', 'synthetic-opencode-session', 1785542401000, 1785542401000,
  '{"type":"text","text":"the beacon is mapped"}'
);
INSERT INTO part VALUES (
  'part-agent-thinking', 'assistant-1', 'synthetic-opencode-session', 1785542401100, 1785542401100,
  '{"type":"reasoning","text":"follow the invented coordinates"}'
);
INSERT INTO part VALUES (
  'part-agent-tool', 'assistant-1', 'synthetic-opencode-session', 1785542401200, 1785542401200,
  '{"type":"tool","callID":"synthetic-call","tool":"grep","state":{"status":"completed","input":{"pattern":"synthetic beacon"},"output":"invented result"}}'
);
INSERT INTO part VALUES (
  'part-agent-patch', 'assistant-1', 'synthetic-opencode-session', 1785542401300, 1785542401300,
  '{"type":"patch","hash":"synthetic-patch-hash","files":["synthetic/beacon.txt"]}'
);
INSERT INTO part VALUES (
  'part-step-start', 'assistant-1', 'synthetic-opencode-session', 1785542401400, 1785542401400,
  '{"type":"step-start","snapshot":"TELEMETRY-MUST-NOT-LAND"}'
);
INSERT INTO part VALUES (
  'part-step-finish', 'assistant-1', 'synthetic-opencode-session', 1785542401500, 1785542401500,
  '{"type":"step-finish","reason":"TELEMETRY-MUST-NOT-LAND","cost":1}'
);
INSERT INTO part VALUES (
  'part-agent-two-text', 'assistant-2', 'synthetic-opencode-session', 1785542403000, 1785542403000,
  '{"type":"text","text":"a second independent answer"}'
);
INSERT INTO part VALUES (
  'part-agent-error', 'assistant-2', 'synthetic-opencode-session', 1785542403100, 1785542403100,
  '{"type":"tool","callID":"synthetic-error-call","tool":"read","state":{"status":"error","input":{"filePath":"synthetic/missing.txt"},"error":"invented missing file"}}'
);
INSERT INTO part VALUES (
  'part-live-tool', 'assistant-live', 'synthetic-opencode-session', 1785542405000, 1785542405000,
  '{"type":"tool","callID":"synthetic-live-call","tool":"read","state":{"status":"running","input":{"filePath":"synthetic/live.txt"}}}'
);

INSERT INTO todo VALUES (
  'synthetic-opencode-session', 'verify the invented map', 'pending', 'high', 0,
  1785542400000, 1785542401000
);
INSERT INTO todo VALUES (
  'synthetic-opencode-session', 'archive the synthetic beacon', 'completed', 'low', 1,
  1785542402000, 1785542403000
);

INSERT INTO event VALUES (
  'synthetic-event', 'synthetic-opencode-session', 1, 'session.telemetry',
  '{"payload":"EVENT-MUST-NOT-LAND"}'
);
