package rocacorpus

import (
	"context"
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
		// Preserve memories and their parent reference; only the redundant session
		// and its harvested exchanges, thinking blocks and tool uses are removed.
		_, err = tx.ExecContext(ctx, `UPDATE memories SET source_session =
		  (SELECT keep_id FROM sibling_plan WHERE session_id = memories.source_session)
		  WHERE source_session IN (`+removableSiblings+`);
		  DELETE FROM thinking_blocks WHERE session_id IN (`+removableSiblings+`);
		  DELETE FROM tool_uses WHERE session_id IN (`+removableSiblings+`);
		  DELETE FROM exchanges WHERE session_id IN (`+removableSiblings+`);
		  DELETE FROM sessions WHERE session_id IN (`+removableSiblings+`);`)
	}
	if err == nil {
		err = tx.Commit()
	}
	return result, err
}

const removableSiblings = `SELECT session_id FROM sibling_plan
	WHERE NOT differs AND keep_id IS NOT NULL AND session_id <> keep_id`
