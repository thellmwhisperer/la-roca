-- La Roca v1 schema.
--
-- Eight tables, no views. Each one's DDL is copied literally from the lab
-- (the frozen reference schema) because
-- database adoption (docs/TECH-SPEC.md 2.4) compares structure: an
-- "improvement" to the DDL turns a clean adoption into a migration.
--
-- Out of v1 by the scope decision: proposals, proposal_annotations,
-- runs and run_logs (v2, and a database that already has them keeps them
-- intact). Out because they are dead in the lab's own catalog: messages,
-- layer_stats.

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
  origin          TEXT NOT NULL CHECK (origin IN ('human', 'agent', 'cron')),
  source_agent    TEXT,
  source_session  TEXT REFERENCES sessions(session_id),
  source_sequence INTEGER,
  project         TEXT,
  status          TEXT DEFAULT 'active' CHECK (status IN ('active', 'pending', 'resolved')),
  supersedes      INTEGER REFERENCES memories(id),
  created_at      TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS layers (
  name            TEXT PRIMARY KEY,
  description     TEXT NOT NULL,
  schema_file     TEXT NOT NULL,
  access_mode     TEXT DEFAULT 'read-write',
  ingest_allowed  INTEGER DEFAULT 1,
  is_coordination INTEGER DEFAULT 0,
  search_excluded INTEGER DEFAULT 0,
  is_classifier_label INTEGER DEFAULT 0,
  alias_of        TEXT,
  added_by        TEXT DEFAULT 'kernel',
  deprecated      INTEGER DEFAULT 0,
  lifecycle       TEXT DEFAULT 'curated',
  capabilities    TEXT DEFAULT '{}',
  since_version   TEXT
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
  response_latency_ms   INTEGER
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

-- Incremental ingest state
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

-- Examples taught in place to the route classifier
CREATE TABLE IF NOT EXISTS queryplan_teach_examples (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  template             TEXT NOT NULL,
  question             TEXT NOT NULL,
  normalized_question  TEXT NOT NULL UNIQUE,
  source_agent         TEXT,
  metadata             TEXT DEFAULT '{}',
  created_at           TEXT DEFAULT (datetime('now')),
  updated_at           TEXT DEFAULT (datetime('now'))
);

-- Query indexes
CREATE INDEX IF NOT EXISTS idx_memories_layer ON memories(layer);
CREATE INDEX IF NOT EXISTS idx_memories_status ON memories(status);
CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project);
CREATE INDEX IF NOT EXISTS idx_memories_origin ON memories(origin);
CREATE INDEX IF NOT EXISTS idx_exchanges_session ON exchanges(session_id);
CREATE INDEX IF NOT EXISTS idx_tool_uses_session ON tool_uses(session_id);
CREATE INDEX IF NOT EXISTS idx_thinking_session ON thinking_blocks(session_id);

-- Ingest hardening: re-ingesting does not duplicate
CREATE UNIQUE INDEX IF NOT EXISTS idx_exchanges_session_number
  ON exchanges(session_id, exchange_number);

CREATE INDEX IF NOT EXISTS idx_ingest_state_project ON ingest_file_state(project);
CREATE INDEX IF NOT EXISTS idx_ingest_state_source_agent ON ingest_file_state(source_agent);

CREATE INDEX IF NOT EXISTS idx_queryplan_teach_examples_template
  ON queryplan_teach_examples(template);
