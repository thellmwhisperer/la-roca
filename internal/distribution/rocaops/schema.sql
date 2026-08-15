CREATE TABLE IF NOT EXISTS memories (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  layer           TEXT NOT NULL,
  content         TEXT NOT NULL,
  metadata        TEXT DEFAULT '{}',
  origin          TEXT NOT NULL CHECK (origin IN ('human', 'agent', 'cron') OR origin GLOB 'plugin:?*'),
  source_agent    TEXT,
  source_model    TEXT,
  source_surface  TEXT,
  source_session  TEXT,
  source_sequence INTEGER,
  project         TEXT,
  status          TEXT DEFAULT 'active' CHECK (status IN ('active', 'pending', 'resolved')),
  supersedes      INTEGER,
  created_at      TEXT DEFAULT (datetime('now')),
  expires_at      TEXT
);

CREATE INDEX IF NOT EXISTS idx_memories_layer ON memories(layer);
CREATE INDEX IF NOT EXISTS idx_memories_status ON memories(status);
CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project);
CREATE INDEX IF NOT EXISTS idx_memories_origin ON memories(origin);
CREATE INDEX IF NOT EXISTS idx_memories_expires_at ON memories(expires_at);

-- The operational half issues identifiers above 2^60 so no ops memory ever
-- answers to the same id as a core memory while both halves are read as one.
-- The seed is conditional: a database that already counts keeps its own place.
INSERT INTO sqlite_sequence(name, seq)
SELECT 'memories', 1152921504606846976
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'memories');

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
  content,
  content='memories',
  content_rowid='id',
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

-- Rebuilt from `memories` on every apply, so a database that predates the index
-- answers for the rows it already held instead of only for what the triggers
-- see from here on.
INSERT INTO memories_fts(memories_fts) VALUES ('rebuild');

CREATE TABLE IF NOT EXISTS legacy_records (
  canonical_digest TEXT PRIMARY KEY,
  record_type       TEXT NOT NULL,
  payload           TEXT NOT NULL CHECK (json_valid(payload))
);

-- DATA-2 builds the future ops-owned memory route beside the currently served
-- `memories` table. Nothing in the semantic fragment exposes these structures,
-- so copying custody cannot change a query before the federation cutover.
CREATE TABLE IF NOT EXISTS memory_records (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  canonical_digest TEXT NOT NULL,
  provenance       TEXT NOT NULL CHECK (provenance IN ('core', 'plugin:roca-corpus', 'plugin:roca-ops')),
  layer            TEXT NOT NULL,
  content          TEXT NOT NULL,
  metadata         TEXT DEFAULT '{}',
  origin           TEXT NOT NULL CHECK (origin IN ('human', 'agent', 'cron') OR origin GLOB 'plugin:?*'),
  source_agent     TEXT,
  source_model     TEXT,
  source_surface   TEXT,
  source_session   TEXT,
  source_sequence  INTEGER,
  project          TEXT,
  status           TEXT DEFAULT 'active' CHECK (status IN ('active', 'pending', 'resolved')),
  supersedes       INTEGER,
  created_at       TEXT,
  expires_at       TEXT
);

CREATE INDEX IF NOT EXISTS idx_memory_records_digest ON memory_records(canonical_digest);
CREATE INDEX IF NOT EXISTS idx_memory_records_layer ON memory_records(layer);
CREATE INDEX IF NOT EXISTS idx_memory_records_status ON memory_records(status);
CREATE INDEX IF NOT EXISTS idx_memory_records_project ON memory_records(project);

-- Corpus provenance is source-specific: an exact core/corpus copy has one
-- physical payload but the corpus identity still needs to reproduce whether it
-- was harvested from a corpus or a file.
CREATE TABLE IF NOT EXISTS memory_provenance (
  source_database TEXT NOT NULL,
  source_key      TEXT NOT NULL,
  provenance      TEXT,
  PRIMARY KEY (source_database, source_key)
);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_records_fts USING fts5(
  content,
  content='memory_records',
  content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER IF NOT EXISTS memory_records_ai AFTER INSERT ON memory_records BEGIN
  INSERT INTO memory_records_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER IF NOT EXISTS memory_records_ad AFTER DELETE ON memory_records BEGIN
  INSERT INTO memory_records_fts(memory_records_fts, rowid, content)
    VALUES ('delete', old.id, old.content);
END;
CREATE TRIGGER IF NOT EXISTS memory_records_au AFTER UPDATE ON memory_records BEGIN
  INSERT INTO memory_records_fts(memory_records_fts, rowid, content)
    VALUES ('delete', old.id, old.content);
  INSERT INTO memory_records_fts(rowid, content) VALUES (new.id, new.content);
END;

INSERT INTO memory_records_fts(memory_records_fts) VALUES ('rebuild');

-- The future in-memory hub can reproduce every old source label and ID from
-- this view. It stays deliberately absent from semantic.yaml in shadow mode.
CREATE VIEW IF NOT EXISTS memory_compatibility AS
SELECT
  memberships.source_database,
  CAST(memberships.source_key AS INTEGER) AS id,
  records.id AS physical_id,
  records.layer,
  records.content,
  records.metadata,
  records.origin,
  source.provenance,
  records.source_agent,
  records.source_model,
  records.source_surface,
  records.source_session,
  records.source_sequence,
  records.project,
  records.status,
  records.supersedes,
  records.created_at,
  records.expires_at
FROM custody_memberships AS memberships
JOIN memory_records AS records
  ON memberships.destination_table = 'memory_records'
 AND CAST(memberships.destination_key AS INTEGER) = records.id
LEFT JOIN memory_provenance AS source
  ON source.source_database = memberships.source_database
 AND source.source_key = memberships.source_key;
