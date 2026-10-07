CREATE INDEX IF NOT EXISTS idx_memories_source_session ON memories(source_session);

CREATE TABLE IF NOT EXISTS vector_changes (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  token TEXT NOT NULL DEFAULT (lower(hex(randomblob(16)))),
  source_kind TEXT NOT NULL,
  source_id TEXT NOT NULL
);

INSERT INTO vector_changes(source_kind,source_id) SELECT '', '' WHERE NOT EXISTS (SELECT 1 FROM vector_changes);

CREATE TRIGGER IF NOT EXISTS vector_changes_sessions_insert AFTER INSERT ON sessions
BEGIN
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('sessions',CAST(NEW.session_id AS TEXT));
  INSERT INTO vector_changes(source_kind,source_id) SELECT 'exchanges',CAST(id AS TEXT) FROM exchanges WHERE session_id=NEW.session_id;
  INSERT INTO vector_changes(source_kind,source_id) SELECT 'thinking_blocks',CAST(id AS TEXT) FROM thinking_blocks WHERE session_id=NEW.session_id;
  INSERT INTO vector_changes(source_kind,source_id) SELECT 'memories',CAST(id AS TEXT) FROM memories WHERE source_session=NEW.session_id;
END;

CREATE TRIGGER IF NOT EXISTS vector_changes_sessions_delete AFTER DELETE ON sessions
BEGIN
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('sessions',CAST(OLD.session_id AS TEXT));
  INSERT INTO vector_changes(source_kind,source_id) SELECT 'exchanges',CAST(id AS TEXT) FROM exchanges WHERE session_id=OLD.session_id;
  INSERT INTO vector_changes(source_kind,source_id) SELECT 'thinking_blocks',CAST(id AS TEXT) FROM thinking_blocks WHERE session_id=OLD.session_id;
  INSERT INTO vector_changes(source_kind,source_id) SELECT 'memories',CAST(id AS TEXT) FROM memories WHERE source_session=OLD.session_id;
END;

CREATE TRIGGER IF NOT EXISTS vector_changes_sessions_update AFTER UPDATE ON sessions
WHEN OLD.session_id IS NOT NEW.session_id OR OLD.title IS NOT NEW.title OR OLD.project IS NOT NEW.project OR OLD.started_at IS NOT NEW.started_at
BEGIN
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('sessions',CAST(OLD.session_id AS TEXT));
  INSERT INTO vector_changes(source_kind,source_id) SELECT 'exchanges',CAST(id AS TEXT) FROM exchanges WHERE session_id=OLD.session_id;
  INSERT INTO vector_changes(source_kind,source_id) SELECT 'thinking_blocks',CAST(id AS TEXT) FROM thinking_blocks WHERE session_id=OLD.session_id;
  INSERT INTO vector_changes(source_kind,source_id) SELECT 'memories',CAST(id AS TEXT) FROM memories WHERE source_session=OLD.session_id;
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('sessions',CAST(NEW.session_id AS TEXT));
  INSERT INTO vector_changes(source_kind,source_id) SELECT 'exchanges',CAST(id AS TEXT) FROM exchanges WHERE session_id=NEW.session_id;
  INSERT INTO vector_changes(source_kind,source_id) SELECT 'thinking_blocks',CAST(id AS TEXT) FROM thinking_blocks WHERE session_id=NEW.session_id;
  INSERT INTO vector_changes(source_kind,source_id) SELECT 'memories',CAST(id AS TEXT) FROM memories WHERE source_session=NEW.session_id;
END;

CREATE TRIGGER IF NOT EXISTS vector_changes_memories_insert AFTER INSERT ON memories
BEGIN
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('memories',CAST(NEW.id AS TEXT));
END;

CREATE TRIGGER IF NOT EXISTS vector_changes_memories_delete AFTER DELETE ON memories
BEGIN
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('memories',CAST(OLD.id AS TEXT));
END;

CREATE TRIGGER IF NOT EXISTS vector_changes_memories_update AFTER UPDATE ON memories
WHEN OLD.id IS NOT NEW.id OR OLD.content IS NOT NEW.content OR OLD.created_at IS NOT NEW.created_at OR OLD.source_session IS NOT NEW.source_session OR OLD.project IS NOT NEW.project
BEGIN
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('memories',CAST(OLD.id AS TEXT));
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('memories',CAST(NEW.id AS TEXT));
END;

CREATE TRIGGER IF NOT EXISTS vector_changes_exchanges_insert AFTER INSERT ON exchanges
BEGIN
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('exchanges',CAST(NEW.id AS TEXT));
END;

CREATE TRIGGER IF NOT EXISTS vector_changes_exchanges_delete AFTER DELETE ON exchanges
BEGIN
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('exchanges',CAST(OLD.id AS TEXT));
END;

CREATE TRIGGER IF NOT EXISTS vector_changes_exchanges_update AFTER UPDATE ON exchanges
WHEN OLD.id IS NOT NEW.id OR OLD.human_text IS NOT NEW.human_text OR OLD.agent_text IS NOT NEW.agent_text OR OLD.human_timestamp IS NOT NEW.human_timestamp OR OLD.agent_timestamp IS NOT NEW.agent_timestamp OR OLD.session_id IS NOT NEW.session_id
BEGIN
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('exchanges',CAST(OLD.id AS TEXT));
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('exchanges',CAST(NEW.id AS TEXT));
END;

CREATE TRIGGER IF NOT EXISTS vector_changes_thinking_blocks_insert AFTER INSERT ON thinking_blocks
BEGIN
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('thinking_blocks',CAST(NEW.id AS TEXT));
END;

CREATE TRIGGER IF NOT EXISTS vector_changes_thinking_blocks_delete AFTER DELETE ON thinking_blocks
BEGIN
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('thinking_blocks',CAST(OLD.id AS TEXT));
END;

CREATE TRIGGER IF NOT EXISTS vector_changes_thinking_blocks_update AFTER UPDATE ON thinking_blocks
WHEN OLD.id IS NOT NEW.id OR OLD.full_text IS NOT NEW.full_text OR OLD.session_id IS NOT NEW.session_id
BEGIN
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('thinking_blocks',CAST(OLD.id AS TEXT));
  INSERT INTO vector_changes(source_kind,source_id) VALUES ('thinking_blocks',CAST(NEW.id AS TEXT));
END;
