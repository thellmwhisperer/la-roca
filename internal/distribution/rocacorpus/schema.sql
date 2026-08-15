CREATE TABLE IF NOT EXISTS sessions (
  session_id    TEXT PRIMARY KEY,
  source_agent  TEXT DEFAULT 'claude-code',
  project       TEXT,
  started_at    TEXT,
  ended_at      TEXT,
  duration_minutes INTEGER,
  title         TEXT,
  metadata      TEXT DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS memories (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  layer           TEXT NOT NULL,
  content         TEXT NOT NULL,
  metadata        TEXT DEFAULT '{}',
  origin          TEXT NOT NULL CHECK (origin IN ('human', 'agent', 'cron') OR origin GLOB 'plugin:?*'),
  provenance      TEXT NOT NULL DEFAULT 'harvest-file'
                           CHECK (provenance IN ('harvest-corpus', 'harvest-file')),
  source_agent    TEXT,
  source_model    TEXT,
  source_surface  TEXT,
  source_session  TEXT REFERENCES sessions(session_id),
  source_sequence INTEGER,
  project         TEXT,
  status          TEXT DEFAULT 'active' CHECK (status IN ('active', 'pending', 'resolved')),
  supersedes      INTEGER REFERENCES memories(id),
  created_at      TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS exchanges (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id            TEXT REFERENCES sessions(session_id),
  exchange_number       INTEGER,
  is_after_compaction   INTEGER DEFAULT 0,
  human_text            TEXT,
  agent_text            TEXT,
  human_timestamp       TEXT,
  agent_timestamp       TEXT,
  response_latency_ms   INTEGER,
  model                 TEXT,
  provider              TEXT,
  tokens_in             INTEGER,
  tokens_out            INTEGER,
  tokens_reasoning      INTEGER,
  cost_usd              REAL
);

CREATE TABLE IF NOT EXISTS tool_uses (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id            TEXT REFERENCES sessions(session_id),
  exchange_number       INTEGER,
  tool_name             TEXT,
  tool_params_summary   TEXT,
  had_error             INTEGER DEFAULT 0,
  error_message         TEXT,
  initiative_type       TEXT
);

CREATE TABLE IF NOT EXISTS thinking_blocks (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id            TEXT REFERENCES sessions(session_id),
  exchange_number       INTEGER,
  position_in_session   REAL,
  depth                 TEXT,
  caution_ratio         REAL,
  word_count            INTEGER,
  is_after_compaction   INTEGER DEFAULT 0,
  full_text             TEXT
);

CREATE TABLE IF NOT EXISTS ingest_file_state (
  path           TEXT NOT NULL PRIMARY KEY,
  source_kind    TEXT NOT NULL,
  source_agent   TEXT,
  project        TEXT,
  fingerprint    TEXT,
  last_synced_at TEXT DEFAULT (datetime('now')),
  last_error     TEXT,
  metadata       TEXT DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_memories_layer ON memories(layer);
CREATE INDEX IF NOT EXISTS idx_memories_status ON memories(status);
CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project);
CREATE INDEX IF NOT EXISTS idx_memories_origin ON memories(origin);
CREATE INDEX IF NOT EXISTS idx_memories_provenance ON memories(provenance);
CREATE INDEX IF NOT EXISTS idx_exchanges_session ON exchanges(session_id);
CREATE INDEX IF NOT EXISTS idx_tool_uses_session ON tool_uses(session_id);
CREATE INDEX IF NOT EXISTS idx_thinking_session ON thinking_blocks(session_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_exchanges_session_number
  ON exchanges(session_id, exchange_number);
CREATE INDEX IF NOT EXISTS idx_ingest_state_project ON ingest_file_state(project);
CREATE INDEX IF NOT EXISTS idx_ingest_state_source_agent ON ingest_file_state(source_agent);

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
  content,
  content='memories',
  content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);

CREATE VIRTUAL TABLE IF NOT EXISTS exchanges_fts USING fts5(
  human_text,
  agent_text,
  content='exchanges',
  content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);

CREATE VIRTUAL TABLE IF NOT EXISTS thinking_fts USING fts5(
  full_text,
  content='thinking_blocks',
  content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);

CREATE VIRTUAL TABLE IF NOT EXISTS sessions_fts USING fts5(
  title,
  project,
  content='sessions',
  tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
  INSERT INTO memories_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;
CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, content) VALUES ('delete', old.id, old.content);
  INSERT INTO memories_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER IF NOT EXISTS exchanges_ai AFTER INSERT ON exchanges BEGIN
  INSERT INTO exchanges_fts(rowid, human_text, agent_text)
    VALUES (new.id, new.human_text, new.agent_text);
END;
CREATE TRIGGER IF NOT EXISTS exchanges_ad AFTER DELETE ON exchanges BEGIN
  INSERT INTO exchanges_fts(exchanges_fts, rowid, human_text, agent_text)
    VALUES ('delete', old.id, old.human_text, old.agent_text);
END;
CREATE TRIGGER IF NOT EXISTS exchanges_au AFTER UPDATE ON exchanges BEGIN
  INSERT INTO exchanges_fts(exchanges_fts, rowid, human_text, agent_text)
    VALUES ('delete', old.id, old.human_text, old.agent_text);
  INSERT INTO exchanges_fts(rowid, human_text, agent_text)
    VALUES (new.id, new.human_text, new.agent_text);
END;

CREATE TRIGGER IF NOT EXISTS thinking_ai AFTER INSERT ON thinking_blocks BEGIN
  INSERT INTO thinking_fts(rowid, full_text) VALUES (new.id, new.full_text);
END;
CREATE TRIGGER IF NOT EXISTS thinking_ad AFTER DELETE ON thinking_blocks BEGIN
  INSERT INTO thinking_fts(thinking_fts, rowid, full_text) VALUES ('delete', old.id, old.full_text);
END;
CREATE TRIGGER IF NOT EXISTS thinking_au AFTER UPDATE ON thinking_blocks BEGIN
  INSERT INTO thinking_fts(thinking_fts, rowid, full_text) VALUES ('delete', old.id, old.full_text);
  INSERT INTO thinking_fts(rowid, full_text) VALUES (new.id, new.full_text);
END;

CREATE TRIGGER IF NOT EXISTS sessions_ai AFTER INSERT ON sessions BEGIN
  INSERT INTO sessions_fts(rowid, title, project) VALUES (new.rowid, new.title, new.project);
END;
CREATE TRIGGER IF NOT EXISTS sessions_ad AFTER DELETE ON sessions BEGIN
  INSERT INTO sessions_fts(sessions_fts, rowid, title, project)
    VALUES ('delete', old.rowid, old.title, old.project);
END;
CREATE TRIGGER IF NOT EXISTS sessions_au AFTER UPDATE ON sessions BEGIN
  INSERT INTO sessions_fts(sessions_fts, rowid, title, project)
    VALUES ('delete', old.rowid, old.title, old.project);
  INSERT INTO sessions_fts(rowid, title, project) VALUES (new.rowid, new.title, new.project);
END;

CREATE TABLE IF NOT EXISTS search_state (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS legacy_flow_patterns (
  canonical_digest TEXT PRIMARY KEY,
  payload           TEXT NOT NULL CHECK (json_valid(payload))
);
