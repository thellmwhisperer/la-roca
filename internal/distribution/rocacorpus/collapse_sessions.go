package rocacorpus

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
)

type SessionCollapseReport struct {
	Applied bool                   `json:"applied"`
	Groups  []SessionCollapseGroup `json:"groups"`
}

type SessionCollapseGroup struct {
	Stem      string   `json:"stem"`
	StartedAt *string  `json:"started_at"`
	Title     *string  `json:"title"`
	IDs       []string `json:"session_ids"`
	Keep      string   `json:"keep"`
	Status    string   `json:"status"`
	Remove    []string `json:"remove"`
	Exchanges int      `json:"exchanges_to_remove"`
}

// CollapseSessions compares only current exchanges. Archive and vector data are
// outside this repair; ordinary DELETE triggers maintain the current FTS data.
func CollapseSessions(ctx context.Context, path string, apply bool) (SessionCollapseReport, error) {
	var result SessionCollapseReport
	db, err := bundledplugin.OpenDatabase(path, !apply)
	if err != nil {
		return result, err
	}
	defer db.Close()
	if err := ensureCorpusOwned(ctx, db); err != nil {
		return result, err
	}
	tx, err := db.BeginTx(ctx, nil)
	var report string
	if err == nil {
		defer tx.Rollback()
		// The temporary plan and all deletes share one snapshot. A concurrent writer
		// cannot change a copy between comparison and deletion without failing the write.
		_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE sibling_plan AS
		WITH siblings AS (
		  SELECT substr(session_id, 1, 35) AS stem, started_at, title,
		    COALESCE(MIN(CASE WHEN EXISTS (SELECT 1 FROM exchanges e WHERE e.session_id = s.session_id)
		        THEN session_id END), MIN(session_id)) AS keep_id
		  FROM sessions s WHERE length(session_id) = 36
		  GROUP BY stem, started_at, title HAVING COUNT(*) > 1
		), copies AS (
		  SELECT g.*, s.session_id,
		    (SELECT COUNT(*) FROM exchanges e WHERE e.session_id = s.session_id) AS turns,
		    EXISTS (
		      SELECT exchange_number, human_text, agent_text, COUNT(*) FROM exchanges
		      WHERE session_id = s.session_id GROUP BY exchange_number, human_text, agent_text
		      EXCEPT
		      SELECT exchange_number, human_text, agent_text, COUNT(*) FROM exchanges
		      WHERE session_id = g.keep_id GROUP BY exchange_number, human_text, agent_text
		    ) OR EXISTS (
		      SELECT exchange_number, human_text, agent_text, COUNT(*) FROM exchanges
		      WHERE session_id = g.keep_id GROUP BY exchange_number, human_text, agent_text
		      EXCEPT
		      SELECT exchange_number, human_text, agent_text, COUNT(*) FROM exchanges
		      WHERE session_id = s.session_id GROUP BY exchange_number, human_text, agent_text
		    ) AS different
		  FROM siblings g JOIN sessions s ON length(s.session_id) = 36
		    AND substr(s.session_id, 1, 35) = g.stem
		    AND s.started_at IS g.started_at AND s.title IS g.title
		)
		SELECT *, MAX(CASE WHEN turns > 0 THEN different ELSE 0 END)
		  OVER (PARTITION BY stem, started_at, title) AS differs
		FROM copies`)
	}
	if err == nil {
		err = tx.QueryRowContext(ctx, `SELECT json_object('applied', json(CASE WHEN ? THEN 'true' ELSE 'false' END), 'groups', json_group_array(json(group_report)))
		  FROM (SELECT json_object('stem', stem, 'started_at', started_at, 'title', title,
		    'session_ids', json_group_array(session_id), 'keep', keep_id,
		    'status', CASE WHEN differs THEN 'different' ELSE 'collapse' END,
		    'remove', json_group_array(session_id) FILTER (WHERE NOT differs AND keep_id IS NOT NULL AND session_id <> keep_id),
		    'exchanges_to_remove', SUM(CASE WHEN NOT differs AND session_id <> keep_id THEN turns ELSE 0 END)) AS group_report
		  FROM (SELECT * FROM sibling_plan ORDER BY session_id)
		  GROUP BY stem, started_at, title ORDER BY stem, started_at, title)`,
			apply).Scan(&report)
	}
	if err == nil {
		err = json.Unmarshal([]byte(report), &result)
	}
	if err == nil && apply {
		err = collapseSessionData(ctx, tx)
	}
	if err == nil {
		err = tx.Commit()
	}
	return result, err
}

func collapseSessionData(ctx context.Context, tx *sql.Tx) (err error) {
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE session_collapse_map AS
		SELECT session_id AS old_id, keep_id AS new_id FROM sibling_plan
		WHERE NOT differs AND keep_id IS NOT NULL AND session_id <> keep_id;
		CREATE TEMP TABLE exchange_collapse_map AS
		WITH copies AS (
		  SELECT e.id, e.session_id, p.keep_id, e.exchange_number, e.human_text, e.agent_text,
		    ROW_NUMBER() OVER (PARTITION BY e.session_id, e.exchange_number, e.human_text, e.agent_text ORDER BY e.id) AS n
		  FROM exchanges e JOIN sibling_plan p ON p.session_id = e.session_id
		  WHERE NOT p.differs AND p.keep_id IS NOT NULL
		)
		SELECT removed.id AS old_id, kept.id AS new_id FROM copies removed JOIN copies kept
		  ON kept.session_id = removed.keep_id AND kept.exchange_number IS removed.exchange_number
		  AND kept.human_text IS removed.human_text AND kept.agent_text IS removed.agent_text AND kept.n = removed.n
		WHERE removed.session_id <> removed.keep_id;
		CREATE TEMP TABLE thinking_collapse_map AS
		SELECT removed.id AS old_id, kept.id AS new_id FROM thinking_blocks removed
		JOIN sibling_plan p ON p.session_id = removed.session_id
		JOIN thinking_blocks kept ON kept.session_id = p.keep_id
		  AND kept.exchange_number IS removed.exchange_number AND kept.full_text IS removed.full_text
		WHERE NOT p.differs AND p.keep_id IS NOT NULL AND removed.session_id <> p.keep_id`)
	for _, remap := range []struct{ table, mapping string }{
		{"session_id_remaps", "session_collapse_map"},
		{"exchange_id_remaps", "exchange_collapse_map"},
		{"thinking_block_id_remaps", "thinking_collapse_map"},
	} {
		if err == nil {
			var present bool
			present, err = tableExists(tx, remap.table)
			if err == nil && present {
				_, err = tx.ExecContext(ctx, `UPDATE `+remap.table+` SET canonical_id =
				(SELECT new_id FROM `+remap.mapping+` WHERE old_id = canonical_id)
				WHERE canonical_id IN (SELECT old_id FROM `+remap.mapping+`)`)
			}
		}
	}
	var memoriesPresent bool
	if err == nil {
		memoriesPresent, err = tableExists(tx, "memories")
	}
	if err == nil && memoriesPresent {
		var conditions string
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(group_concat(
			' AND duplicate."' || name || '" IS kept."' || name || '"', ''), '')
			FROM pragma_table_info('memories')
			WHERE name NOT IN ('id', 'created_at', 'source_session')`).Scan(&conditions)
		if err == nil {
			_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE memory_collapse_map AS
			SELECT duplicate.id AS old_id, MIN(kept.id) AS new_id FROM memories duplicate
			JOIN session_collapse_map sessions ON duplicate.source_session = sessions.old_id
			JOIN memories kept ON kept.source_session = sessions.new_id`+conditions+`
			GROUP BY duplicate.id`)
		}
		var remapsPresent bool
		if err == nil {
			remapsPresent, err = tableExists(tx, "memory_id_remaps")
		}
		if err == nil && remapsPresent {
			_, err = tx.ExecContext(ctx, `UPDATE memory_id_remaps SET canonical_id =
				(SELECT new_id FROM memory_collapse_map WHERE old_id = canonical_id)
				WHERE canonical_id IN (SELECT old_id FROM memory_collapse_map)`)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE memories SET supersedes =
			(SELECT new_id FROM memory_collapse_map WHERE old_id = supersedes)
			WHERE supersedes IN (SELECT old_id FROM memory_collapse_map);
			DELETE FROM memories WHERE id IN (SELECT old_id FROM memory_collapse_map);
			UPDATE memories SET source_session =
			(SELECT new_id FROM session_collapse_map WHERE old_id = source_session)
			WHERE source_session IN (SELECT old_id FROM session_collapse_map)`)
		}
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM thinking_blocks WHERE session_id IN (`+removableSiblings+`);
			DELETE FROM tool_uses WHERE session_id IN (`+removableSiblings+`);
			DELETE FROM exchanges WHERE session_id IN (`+removableSiblings+`);
			DELETE FROM sessions WHERE session_id IN (`+removableSiblings+`);`)
	}
	return err
}

const removableSiblings = `SELECT session_id FROM sibling_plan
	WHERE NOT differs AND keep_id IS NOT NULL AND session_id <> keep_id`
