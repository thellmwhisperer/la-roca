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
    FROM memories AS m
    WHERE m.layer = 'handoff'
      AND m.project = 'synthetic-orchard'
      AND m.id NOT IN (
          SELECT supersedes FROM memories WHERE supersedes IS NOT NULL
      )

    UNION ALL

    SELECT 'memory',
           m.id,
           COALESCE(NULLIF(m.source_agent, ''), 'unknown') || '/' ||
           COALESCE(NULLIF(m.source_model, ''), 'unknown') || ' via ' ||
           COALESCE(NULLIF(m.source_surface, ''), 'unknown'),
           m.content,
           m.created_at,
           'plugin:ops-fixture'
    FROM plugin_ops_fixture.memories AS m
    WHERE m.layer = 'handoff'
      AND m.project = 'synthetic-orchard'
      AND m.id NOT IN (
          SELECT supersedes
          FROM plugin_ops_fixture.memories
          WHERE supersedes IS NOT NULL
      )

    UNION ALL

    SELECT 'memory',
           m.id,
           COALESCE(NULLIF(m.source_agent, ''), 'unknown') || '/' ||
           COALESCE(NULLIF(m.source_model, ''), 'unknown') || ' via ' ||
           COALESCE(NULLIF(m.source_surface, ''), 'unknown'),
           m.content,
           m.created_at,
           'plugin:corpus-fixture'
    FROM plugin_corpus_fixture.memories AS m
    WHERE m.layer = 'handoff'
      AND m.project = 'synthetic-orchard'
      AND m.id NOT IN (
          SELECT supersedes
          FROM plugin_corpus_fixture.memories
          WHERE supersedes IS NOT NULL
      )
)
ORDER BY created_at DESC
LIMIT 1
