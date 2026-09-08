SELECT 'memory' AS source,
       m.id,
       COALESCE(NULLIF(m.source_agent, ''), 'unknown') || '/' ||
       COALESCE(NULLIF(m.source_model, ''), 'unknown') || ' via ' ||
       COALESCE(NULLIF(m.source_surface, ''), 'unknown') AS author,
       m.source_model AS model,
       m.content AS text,
       0 AS source_priority,
       f.rango,
       'core' AS "database"
FROM (
    SELECT rowid AS fila, bm25(memories_fts) AS rango
    FROM memories_fts
    WHERE memories_fts MATCH '"fixtureterm"'
    ORDER BY rango
    LIMIT 20
) AS f
JOIN memories AS m ON m.id = f.fila
WHERE m.id NOT IN (
    SELECT supersedes FROM memories WHERE supersedes IS NOT NULL
)
UNION ALL
SELECT 'exchange' AS source,
       e.id,
       COALESCE(NULLIF(s.source_agent, ''), 'unknown') || '/' ||
       COALESCE(NULLIF(e.model, ''), 'unknown') || ' via ' ||
       COALESCE(NULLIF(s.source_surface, ''), 'unknown') AS author,
       e.model,
       e.agent_text AS text,
       1 AS source_priority,
       f.rango,
       'core' AS "database"
FROM (
    SELECT rowid AS fila, bm25(exchanges_fts) AS rango
    FROM exchanges_fts
    WHERE exchanges_fts MATCH '{agent_text} : ("fixtureterm")'
    ORDER BY rango
    LIMIT 20
) AS f
JOIN exchanges AS e ON e.id = f.fila
JOIN sessions AS s ON s.session_id = e.session_id
UNION ALL
SELECT 'human' AS source,
       e.id,
       COALESCE(NULLIF(s.source_agent, ''), 'unknown') || '/' ||
       COALESCE(NULLIF(e.model, ''), 'unknown') || ' via ' ||
       COALESCE(NULLIF(s.source_surface, ''), 'unknown') AS author,
       e.model,
       e.human_text AS text,
       1 AS source_priority,
       f.rango,
       'core' AS "database"
FROM (
    SELECT rowid AS fila, bm25(exchanges_fts) AS rango
    FROM exchanges_fts
    WHERE exchanges_fts MATCH '{human_text} : ("fixtureterm")'
    ORDER BY rango
    LIMIT 20
) AS f
JOIN exchanges AS e ON e.id = f.fila
JOIN sessions AS s ON s.session_id = e.session_id
WHERE e.human_text NOT LIKE '<task-notification%'
UNION ALL
SELECT 'thinking' AS source,
       t.id,
       COALESCE(NULLIF(s.source_agent, ''), 'unknown') || '/unknown via ' ||
       COALESCE(NULLIF(s.source_surface, ''), 'unknown') AS author,
       NULL AS model,
       t.full_text AS text,
       2 AS source_priority,
       f.rango,
       'core' AS "database"
FROM (
    SELECT rowid AS fila, bm25(thinking_fts) AS rango
    FROM thinking_fts
    WHERE thinking_fts MATCH '"fixtureterm"'
    ORDER BY rango
    LIMIT 20
) AS f
JOIN thinking_blocks AS t ON t.id = f.fila
JOIN sessions AS s ON s.session_id = t.session_id
ORDER BY source_priority, rango
LIMIT 20
