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
