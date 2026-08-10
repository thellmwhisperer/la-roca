-- La Roca v1 search artefact: the FTS5 lexical index.
--
-- This is NOT the identity schema. `schema.sql` declares the eight tables that
-- make a database a Roca one and adoption compares them one by one; what is
-- here is a derived index, it can be thrown away and rebuilt without losing a
-- single piece of data, and that is why it lives apart: if it counted as
-- structure, adopting a large live database would require an unnecessary backup
-- in order to create an index that rebuilds itself.
--
-- Everything goes with IF NOT EXISTS: applying it twice changes nothing.

-- --- lexical index ---
--
-- External content (`content=`): the index stores the position of each word,
-- never a second copy of the text. On the real database that is the difference
-- between growing 26% and growing more than double.
--
-- `remove_diacritics 2` folds diacritics while preserving U+00F1, which is
-- required for consistent Unicode folding. The folding has to be the same one
-- `query.Fold` does and the same one the lexical MATCH expression is built
-- with: what the index stores and what the query asks for start from the same
-- tokens or the search misses its own index.

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

-- --- synchronization triggers ---
--
-- With external content, FTS5 does not notice the changes: it has to be told.
-- A delete is told with the old text because the index needs to know which
-- positions to remove, and by then the row is no longer there to look at.

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

-- --- index state ---
--
-- Which artefacts the indexer has built. Today that is the lexical index alone;
-- the marker is kept so the indexer can ask the question once and stay
-- incremental, instead of rebuilding on every pass.
CREATE TABLE IF NOT EXISTS search_state (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT DEFAULT (datetime('now'))
);
