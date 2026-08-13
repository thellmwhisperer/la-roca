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

INSERT INTO sqlite_sequence(name, seq)
SELECT 'memories', 1152921504606846976
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'memories');
