CREATE TABLE IF NOT EXISTS sessions (
  session_id    TEXT PRIMARY KEY,
  source_agent  TEXT DEFAULT 'claude-code',
  project       TEXT,
  started_at    TEXT,
  ended_at      TEXT,
  duration_minutes INTEGER,
  title         TEXT,
  metadata      TEXT DEFAULT '{}',
  source_surface TEXT
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

-- DATA SPLIT shadow archive. These tables are deliberately separate from the
-- currently served harvest tables above: copying the retired core archive must
-- not change an answer before the later atomic cutover.
CREATE TABLE IF NOT EXISTS corpus_source_snapshots (
  source_database     TEXT PRIMARY KEY,
  snapshot_digest     TEXT NOT NULL CHECK (length(snapshot_digest) = 64),
  destination_source INTEGER NOT NULL DEFAULT 0 CHECK (destination_source IN (0, 1)),
  batch_size         INTEGER NOT NULL CHECK (batch_size > 0)
);

CREATE TABLE IF NOT EXISTS corpus_source_tables (
  source_database TEXT NOT NULL REFERENCES corpus_source_snapshots(source_database),
  source_table    TEXT NOT NULL,
  expected_rows   INTEGER NOT NULL CHECK (expected_rows >= 0),
  PRIMARY KEY (source_database, source_table)
);

CREATE TABLE IF NOT EXISTS session_versions (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  version_digest   TEXT NOT NULL UNIQUE CHECK (length(version_digest) = 64),
  session_id       TEXT NOT NULL,
  source_agent     TEXT,
  project          TEXT,
  started_at       TEXT,
  ended_at         TEXT,
  duration_minutes INTEGER,
  title            TEXT,
  metadata         TEXT
);

CREATE TABLE IF NOT EXISTS exchange_versions (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  version_digest      TEXT NOT NULL UNIQUE CHECK (length(version_digest) = 64),
  session_id          TEXT NOT NULL,
  exchange_number     INTEGER,
  is_after_compaction INTEGER,
  human_text          TEXT,
  agent_text          TEXT,
  human_timestamp     TEXT,
  agent_timestamp     TEXT,
  response_latency_ms INTEGER,
  model               TEXT,
  provider            TEXT,
  tokens_in           INTEGER,
  tokens_out          INTEGER,
  tokens_reasoning    INTEGER,
  cost_usd            REAL
);

CREATE TABLE IF NOT EXISTS tool_use_versions (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  version_digest      TEXT NOT NULL UNIQUE CHECK (length(version_digest) = 64),
  session_id          TEXT NOT NULL,
  exchange_number     INTEGER,
  tool_name           TEXT,
  tool_params_summary TEXT,
  had_error           INTEGER,
  error_message       TEXT,
  initiative_type     TEXT
);

CREATE TABLE IF NOT EXISTS thinking_block_versions (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  version_digest      TEXT NOT NULL UNIQUE CHECK (length(version_digest) = 64),
  session_id          TEXT NOT NULL,
  exchange_number     INTEGER,
  position_in_session REAL,
  depth               TEXT,
  caution_ratio       REAL,
  word_count          INTEGER,
  is_after_compaction INTEGER,
  full_text           TEXT
);

CREATE TABLE IF NOT EXISTS ingest_file_state_versions (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  version_digest   TEXT NOT NULL UNIQUE CHECK (length(version_digest) = 64),
  path             TEXT NOT NULL,
  source_kind      TEXT,
  source_agent     TEXT,
  project          TEXT,
  fingerprint      TEXT,
  last_synced_at   TEXT,
  last_error       TEXT,
  metadata         TEXT
);

CREATE TABLE IF NOT EXISTS ingest_file_state_heads (
  path                 TEXT PRIMARY KEY,
  version_digest       TEXT NOT NULL REFERENCES ingest_file_state_versions(version_digest),
  source_database      TEXT NOT NULL,
  destination_priority INTEGER NOT NULL CHECK (destination_priority IN (0, 1))
);

-- source_key is the natural session/exchange key, or for tool/thinking rows the
-- canonical payload digest plus its occurrence ordinal inside the parent turn.
-- source_row_id is evidence for the old compatibility envelope, never identity.
CREATE TABLE IF NOT EXISTS corpus_source_rows (
  source_database   TEXT NOT NULL,
  source_table      TEXT NOT NULL,
  source_key        TEXT NOT NULL,
  destination_table TEXT NOT NULL,
  version_digest    TEXT NOT NULL CHECK (length(version_digest) = 64),
  source_row_id     INTEGER,
  session_id        TEXT,
  exchange_number   INTEGER,
  occurrence_ordinal INTEGER,
  PRIMARY KEY (source_database, source_table, source_key)
);

CREATE INDEX IF NOT EXISTS session_versions_logical_id
  ON session_versions(session_id);
CREATE INDEX IF NOT EXISTS exchange_versions_logical_key
  ON exchange_versions(session_id, exchange_number);
CREATE INDEX IF NOT EXISTS tool_use_versions_parent
  ON tool_use_versions(session_id, exchange_number);
CREATE INDEX IF NOT EXISTS thinking_block_versions_parent
  ON thinking_block_versions(session_id, exchange_number);
CREATE INDEX IF NOT EXISTS ingest_file_state_versions_path
  ON ingest_file_state_versions(path);
CREATE INDEX IF NOT EXISTS corpus_source_rows_destination
  ON corpus_source_rows(destination_table, version_digest);

CREATE VIRTUAL TABLE IF NOT EXISTS session_versions_fts USING fts5(
  title,
  project,
  content='session_versions',
  content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);

CREATE VIRTUAL TABLE IF NOT EXISTS exchange_versions_fts USING fts5(
  human_text,
  agent_text,
  content='exchange_versions',
  content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);

CREATE VIRTUAL TABLE IF NOT EXISTS thinking_block_versions_fts USING fts5(
  full_text,
  content='thinking_block_versions',
  content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);

CREATE VIEW IF NOT EXISTS session_version_memberships AS
SELECT m.source_database, r.session_id AS source_session_id,
       r.source_row_id, v.*
FROM custody_memberships AS m
JOIN corpus_source_rows AS r
  USING (source_database, source_table, source_key)
JOIN session_versions AS v ON v.version_digest = m.destination_key
WHERE m.destination_table = 'session_versions';

CREATE VIEW IF NOT EXISTS exchange_version_memberships AS
SELECT m.source_database, r.source_row_id, r.occurrence_ordinal, v.*
FROM custody_memberships AS m
JOIN corpus_source_rows AS r
  USING (source_database, source_table, source_key)
JOIN exchange_versions AS v ON v.version_digest = m.destination_key
WHERE m.destination_table = 'exchange_versions';

CREATE VIEW IF NOT EXISTS tool_use_version_memberships AS
SELECT m.source_database, r.source_row_id, r.occurrence_ordinal, v.*
FROM custody_memberships AS m
JOIN corpus_source_rows AS r
  USING (source_database, source_table, source_key)
JOIN tool_use_versions AS v ON v.version_digest = m.destination_key
WHERE m.destination_table = 'tool_use_versions';

CREATE VIEW IF NOT EXISTS thinking_block_version_memberships AS
SELECT m.source_database, r.source_row_id, r.occurrence_ordinal, v.*
FROM custody_memberships AS m
JOIN corpus_source_rows AS r
  USING (source_database, source_table, source_key)
JOIN thinking_block_versions AS v ON v.version_digest = m.destination_key
WHERE m.destination_table = 'thinking_block_versions';

CREATE VIEW IF NOT EXISTS ingest_file_state_version_memberships AS
SELECT m.source_database, v.*,
       h.version_digest = v.version_digest AS is_selected
FROM custody_memberships AS m
JOIN ingest_file_state_versions AS v ON v.version_digest = m.destination_key
LEFT JOIN ingest_file_state_heads AS h ON h.path = v.path
WHERE m.destination_table = 'ingest_file_state_versions';
