SELECT 'memory' AS source,
       m.id,
       m.layer,
       COALESCE(NULLIF(m.source_agent, ''), 'unknown') || '/' ||
       COALESCE(NULLIF(m.source_model, ''), 'unknown') || ' via ' ||
       COALESCE(NULLIF(m.source_surface, ''), 'unknown') AS author,
       m.content AS text,
       'core' AS "database",
       0 AS source_priority,
       f.rango
FROM (
    SELECT rowid AS fila, bm25(memories_fts) AS rango
    FROM memories_fts
    WHERE memories_fts MATCH '"fixture" "protocol"'
    ORDER BY rango
    LIMIT 20
) AS f
JOIN memories AS m ON m.id = f.fila
WHERE m.id NOT IN (
    SELECT supersedes FROM memories WHERE supersedes IS NOT NULL
)
UNION ALL
SELECT 'memory',
       m.id,
       m.layer,
       COALESCE(NULLIF(m.source_agent, ''), 'unknown') || '/' ||
       COALESCE(NULLIF(m.source_model, ''), 'unknown') || ' via ' ||
       COALESCE(NULLIF(m.source_surface, ''), 'unknown'),
       m.content,
       'plugin:ops-fixture',
       0,
       f.rango
FROM (
    SELECT rowid AS fila, bm25(plugin_ops_fixture.memories_fts) AS rango
    FROM plugin_ops_fixture.memories_fts
    WHERE plugin_ops_fixture.memories_fts MATCH '"fixture" "protocol"'
    ORDER BY rango
    LIMIT 20
) AS f
JOIN plugin_ops_fixture.memories AS m ON m.id = f.fila
WHERE m.id NOT IN (
    SELECT supersedes
    FROM plugin_ops_fixture.memories
    WHERE supersedes IS NOT NULL
)
UNION ALL
SELECT 'memory',
       m.id,
       m.layer,
       COALESCE(NULLIF(m.source_agent, ''), 'unknown') || '/' ||
       COALESCE(NULLIF(m.source_model, ''), 'unknown') || ' via ' ||
       COALESCE(NULLIF(m.source_surface, ''), 'unknown'),
       m.content,
       'plugin:corpus-fixture',
       0,
       f.rango
FROM (
    SELECT rowid AS fila, bm25(plugin_corpus_fixture.memories_fts) AS rango
    FROM plugin_corpus_fixture.memories_fts
    WHERE plugin_corpus_fixture.memories_fts MATCH '"fixture" "protocol"'
    ORDER BY rango
    LIMIT 20
) AS f
JOIN plugin_corpus_fixture.memories AS m ON m.id = f.fila
WHERE m.id NOT IN (
    SELECT supersedes
    FROM plugin_corpus_fixture.memories
    WHERE supersedes IS NOT NULL
)
ORDER BY source_priority, rango
LIMIT 20
