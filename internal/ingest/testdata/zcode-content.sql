CREATE TABLE schema_migration (
  id TEXT PRIMARY KEY,
  checksum TEXT NOT NULL,
  app_version TEXT,
  time_applied INTEGER NOT NULL
);
CREATE TABLE session (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  workspace_id TEXT,
  parent_id TEXT,
  slug TEXT NOT NULL,
  directory TEXT NOT NULL,
  path TEXT,
  title TEXT NOT NULL,
  version TEXT NOT NULL,
  task_type TEXT NOT NULL,
  time_created INTEGER NOT NULL,
  time_updated INTEGER NOT NULL
);
CREATE TABLE message (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  time_created INTEGER NOT NULL,
  time_updated INTEGER NOT NULL,
  data TEXT NOT NULL,
  sequence INTEGER
);
CREATE TABLE part (
  id TEXT PRIMARY KEY,
  message_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  time_created INTEGER NOT NULL,
  time_updated INTEGER NOT NULL,
  data TEXT NOT NULL,
  sequence INTEGER
);

INSERT INTO schema_migration VALUES (
  '0018_session_input_failed_status', 'synthetic-checksum', '0.15.2', 1785542300000
);
INSERT INTO session VALUES (
  'synthetic-zcode-session', 'synthetic-project', 'synthetic-workspace', NULL,
  'synthetic-zcode-session', '/synthetic/workspace/opal', NULL,
  'Map the invented beacon', '0.16.5', 'interactive',
  1785542400000, 1785542460000
);

INSERT INTO message VALUES (
  'user-visible', 'synthetic-zcode-session', 1785542400000, 1785542400000,
  '{"role":"user","time":{"created":1785542400000},"agent":"zcode-agent","model":{"providerID":"synthetic-provider","modelID":"synthetic-model-a","variant":"max"},"semantics":{"origin":"real_user","kind":"user_prompt","uiVisibility":"visible","transcriptVisibility":"visible"},"anchor":{"turnId":"synthetic-turn-a","origin":"realUser"}}', 0
);
INSERT INTO message VALUES (
  'assistant-rich', 'synthetic-zcode-session', 1785542401000, 1785542405000,
  '{"role":"assistant","parentID":"user-visible","time":{"created":1785542401000,"completed":1785542405000},"modelID":"synthetic-model-a","providerID":"synthetic-provider","cost":0.25,"tokens":{"input":12,"output":5,"reasoning":3,"cache":{"read":2,"write":1}},"finish":"tool-calls","semantics":{"origin":"agent_runtime","kind":"assistant_response","uiVisibility":"visible","transcriptVisibility":"visible"},"anchor":{"turnId":"synthetic-turn-a"}}', 1
);
INSERT INTO message VALUES (
  'user-reminder', 'synthetic-zcode-session', 1785542402000, 1785542402000,
  '{"role":"user","time":{"created":1785542402000},"agent":"zcode-agent","model":{"providerID":"synthetic-provider","modelID":"synthetic-model-a"},"semantics":{"origin":"agent_runtime","kind":"todo_reminder","uiVisibility":"hidden","transcriptVisibility":"hidden"},"anchor":{"turnId":"synthetic-turn-a","origin":"synthetic"},"synthetic":true}', 2
);
INSERT INTO message VALUES (
  'assistant-tool-only', 'synthetic-zcode-session', 1785542406000, 1785542407000,
  '{"role":"assistant","parentID":"user-visible","time":{"created":1785542406000,"completed":1785542407000},"modelID":"synthetic-model-b","providerID":"synthetic-provider","semantics":{"origin":"agent_runtime","kind":"assistant_response","uiVisibility":"visible","transcriptVisibility":"visible"},"anchor":{"turnId":"synthetic-turn-a"}}', 3
);
INSERT INTO message VALUES (
  'assistant-live', 'synthetic-zcode-session', 1785542408000, 1785542408000,
  '{"role":"assistant","parentID":"user-visible","time":{"created":1785542408000},"modelID":"synthetic-model-c","providerID":"synthetic-provider","semantics":{"origin":"agent_runtime","kind":"assistant_response","uiVisibility":"visible","transcriptVisibility":"visible"},"anchor":{"turnId":"synthetic-turn-a"}}', 4
);
INSERT INTO message VALUES (
  'assistant-timeline', 'synthetic-zcode-session', 1785542409000, 1785542409000,
  '{"role":"assistant","time":{"created":1785542409000,"completed":1785542409000},"modelID":"synthetic-model-c","providerID":"synthetic-provider","semantics":{"origin":"system","kind":"timeline_event","uiVisibility":"visible","transcriptVisibility":"visible"}}', 5
);
INSERT INTO message VALUES (
  'user-without-model', 'synthetic-zcode-session', 1785542410000, 1785542410000,
  '{"role":"user","time":{"created":1785542410000},"semantics":{"origin":"real_user","kind":"user_prompt","uiVisibility":"visible","transcriptVisibility":"visible"}}', 6
);
INSERT INTO message VALUES (
  'assistant-without-model', 'synthetic-zcode-session', 1785542411000, 1785542412000,
  '{"role":"assistant","parentID":"user-without-model","time":{"created":1785542411000,"completed":1785542412000},"semantics":{"origin":"agent_runtime","kind":"assistant_response","uiVisibility":"visible","transcriptVisibility":"visible"}}', 7
);

INSERT INTO part VALUES (
  'part-user-text', 'user-visible', 'synthetic-zcode-session', 1785542400000, 1785542400000,
  '{"type":"text","text":"map the synthetic beacon","time":{"start":1785542400000,"end":1785542400000}}', 0
);
INSERT INTO part VALUES (
  'part-step-start', 'assistant-rich', 'synthetic-zcode-session', 1785542401000, 1785542401000,
  '{"type":"step-start","snapshot":"ZCODE-STEP-SENTINEL"}', 0
);
INSERT INTO part VALUES (
  'part-agent-thinking', 'assistant-rich', 'synthetic-zcode-session', 1785542401100, 1785542402000,
  '{"type":"reasoning","text":"follow the invented coordinates","time":{"start":1785542401100,"end":1785542402000}}', 1
);
INSERT INTO part VALUES (
  'part-agent-text', 'assistant-rich', 'synthetic-zcode-session', 1785542402100, 1785542403000,
  '{"type":"text","text":"the synthetic beacon is mapped","time":{"start":1785542402100,"end":1785542403000}}', 2
);
INSERT INTO part VALUES (
  'part-agent-tool', 'assistant-rich', 'synthetic-zcode-session', 1785542403100, 1785542404000,
  '{"type":"tool","callID":"synthetic-call-a","tool":"Bash","state":{"status":"completed","input":{"command":"find synthetic beacon"},"output":"invented result","time":{"start":1785542403100,"end":1785542404000}}}', 3
);
INSERT INTO part VALUES (
  'part-step-finish', 'assistant-rich', 'synthetic-zcode-session', 1785542404100, 1785542405000,
  '{"type":"step-finish","reason":"ZCODE-FINISH-SENTINEL"}', 4
);
INSERT INTO part VALUES (
  'part-reminder-text', 'user-reminder', 'synthetic-zcode-session', 1785542402000, 1785542402000,
  '{"type":"text","text":"ZCODE-HIDDEN-SENTINEL","synthetic":true}', 0
);
INSERT INTO part VALUES (
  'part-agent-error', 'assistant-tool-only', 'synthetic-zcode-session', 1785542406000, 1785542407000,
  '{"type":"tool","callID":"synthetic-call-b","tool":"Read","state":{"status":"error","input":{"file_path":"synthetic/missing.txt"},"error":"invented missing file","time":{"start":1785542406000,"end":1785542407000}}}', 0
);
INSERT INTO part VALUES (
  'part-live-tool', 'assistant-live', 'synthetic-zcode-session', 1785542408000, 1785542408000,
  '{"type":"tool","callID":"synthetic-call-live","tool":"Read","state":{"status":"running","input":{"file_path":"synthetic/live.txt"}}}', 0
);
INSERT INTO part VALUES (
  'part-timeline', 'assistant-timeline', 'synthetic-zcode-session', 1785542409000, 1785542409000,
  '{"type":"timeline","timelineType":"model_change","display":"separator","status":"completed","fromModel":{"modelID":"synthetic-model-old"},"toModel":{"modelID":"synthetic-model-c"},"time":{"start":1785542409000,"end":1785542409000},"text":"ZCODE-TIMELINE-SENTINEL"}', 0
);
INSERT INTO part VALUES (
  'part-model-less-user', 'user-without-model', 'synthetic-zcode-session', 1785542410000, 1785542410000,
  '{"type":"text","text":"ZCODE-MISSING-USER-MODEL-SENTINEL"}', 0
);
INSERT INTO part VALUES (
  'part-model-less-assistant', 'assistant-without-model', 'synthetic-zcode-session', 1785542411000, 1785542412000,
  '{"type":"text","text":"ZCODE-MISSING-ASSISTANT-MODEL-SENTINEL"}', 0
);
