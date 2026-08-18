SELECT source, id, author, text, created_at, "database"
FROM (
  SELECT 'memory' AS source,
         m.id,
         COALESCE(NULLIF(m.source_agent, ''), 'unknown') || '/' ||
         COALESCE(NULLIF(m.source_model, ''), 'unknown') || ' via ' ||
         COALESCE(NULLIF(m.source_surface, ''), 'unknown') AS author,
         m.content AS text,
         m.created_at,
         'core' AS "database"
  FROM (
    SELECT rowid AS fila
    FROM memories_fts
    WHERE memories_fts MATCH '"fixture" "vector" "rename"'
  ) AS f
  JOIN memories AS m ON m.id = f.fila
  WHERE m.layer = 'handoff'
    AND m.id NOT IN (
      SELECT supersedes
      FROM memories
      WHERE supersedes IS NOT NULL
    )

  UNION ALL

  SELECT 'memory' AS source,
         m.id,
         COALESCE(NULLIF(m.source_agent, ''), 'unknown') || '/' ||
         COALESCE(NULLIF(m.source_model, ''), 'unknown') || ' via ' ||
         COALESCE(NULLIF(m.source_surface, ''), 'unknown') AS author,
         m.content AS text,
         m.created_at,
         'plugin:ops-fixture' AS "database"
  FROM (
    SELECT rowid AS fila
    FROM plugin_ops_fixture.memories_fts
    WHERE memories_fts MATCH '"fixture" "vector" "rename"'
  ) AS f
  JOIN plugin_ops_fixture.memories AS m ON m.id = f.fila
  WHERE m.layer = 'handoff'
    AND m.id NOT IN (
      SELECT supersedes
      FROM plugin_ops_fixture.memories
      WHERE supersedes IS NOT NULL
    )

  UNION ALL

  SELECT 'memory' AS source,
         m.id,
         COALESCE(NULLIF(m.source_agent, ''), 'unknown') || '/' ||
         COALESCE(NULLIF(m.source_model, ''), 'unknown') || ' via ' ||
         COALESCE(NULLIF(m.source_surface, ''), 'unknown') AS author,
         m.content AS text,
         m.created_at,
         'plugin:corpus-fixture' AS "database"
  FROM (
    SELECT rowid AS fila
    FROM plugin_corpus_fixture.memories_fts
    WHERE memories_fts MATCH '"fixture" "vector" "rename"'
  ) AS f
  JOIN plugin_corpus_fixture.memories AS m ON m.id = f.fila
  WHERE m.layer = 'handoff'
    AND m.id NOT IN (
      SELECT supersedes
      FROM plugin_corpus_fixture.memories
      WHERE supersedes IS NOT NULL
    )
)
ORDER BY created_at DESC
LIMIT 1
