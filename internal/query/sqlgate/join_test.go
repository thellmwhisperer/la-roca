package sqlgate_test

import (
	"strings"
	"testing"
)

// Tests of the gate against real joins.
//
// They exist because a report arrived saying that
// `no such column: "source_agent" does not exist in the referenced tables`
// smelled of a gate that does not resolve columns across joined tables. That
// hypothesis has to be measured before anything is touched: if the gate really
// broke on joins, the defect would be hiding behind the one that was reported,
// and fixing the visible one would leave the worse one in place.
//
// `tool_uses` and `thinking_blocks` carry `session_id` and nothing else about
// who ran them; `source_agent` and `project` live on `sessions`. So every real
// question about tools by agent has to cross that join, and the gate has to
// resolve it.

func TestTheGateResolvesColumnsAcrossAJoin(t *testing.T) {
	g := gate(t)
	benchCases := []string{
		// The join the reported question really needed.
		`SELECT s.source_agent, t.tool_name, COUNT(*) AS uses
		 FROM tool_uses t JOIN sessions s ON t.session_id = s.session_id
		 WHERE s.source_agent = 'pi' GROUP BY t.tool_name ORDER BY uses DESC LIMIT 10`,
		// The same thing with the older comma syntax.
		`SELECT sessions.source_agent, tool_uses.tool_name
		 FROM tool_uses, sessions WHERE tool_uses.session_id = sessions.session_id LIMIT 5`,
		// Unqualified columns whose table the engine has to work out on its own.
		`SELECT tool_name, source_agent FROM tool_uses
		 JOIN sessions ON tool_uses.session_id = sessions.session_id LIMIT 5`,
		// USING instead of ON.
		`SELECT source_agent, tool_name FROM tool_uses JOIN sessions USING (session_id) LIMIT 5`,
		// A left join, which keeps rows with no match.
		`SELECT s.project, COUNT(t.id) FROM sessions s
		 LEFT JOIN tool_uses t ON t.session_id = s.session_id GROUP BY s.project LIMIT 10`,
		// Three tables at once.
		`SELECT s.source_agent, e.human_text, t.tool_name
		 FROM sessions s
		 JOIN exchanges e ON e.session_id = s.session_id
		 JOIN tool_uses t ON t.session_id = s.session_id LIMIT 5`,
		// A join inside a subquery, which is where an AST walker usually stops
		// looking.
		`SELECT project FROM sessions WHERE session_id IN (
		   SELECT t.session_id FROM tool_uses t
		   JOIN sessions s ON s.session_id = t.session_id
		   WHERE s.source_agent = 'pi') LIMIT 10`,
		// The whole reported question, written the way it should have been.
		`SELECT t.tool_name, COUNT(*) AS uses
		 FROM tool_uses t JOIN sessions s ON s.session_id = t.session_id
		 WHERE s.source_agent = 'pi' GROUP BY t.tool_name ORDER BY uses DESC LIMIT 10`,
		// The other reported question, written the way it should have been.
		`SELECT project, MAX(duration_minutes) AS longest FROM sessions
		 WHERE started_at >= datetime('now', '-1 month')
		 GROUP BY project ORDER BY longest DESC LIMIT 10`,
	}
	for _, stmt := range benchCases {
		if _, err := g.Validate(stmt); err != nil {
			t.Errorf("the gate rejects a legitimate join:\n%s\n  -> %v", stmt, err)
		}
	}
}

// The other half of the same property: a column that exists on NO table of the
// join is still rejected. A gate that accepted joins by relaxing what it checks
// would be worse than one that rejected them.
func TestAcrossAJoinAColumnThatExistsNowhereIsStillRejected(t *testing.T) {
	g := gate(t)
	benchCases := []string{
		`SELECT t.tool_name FROM tool_uses t JOIN sessions s ON t.session_id = s.session_id
		 WHERE t.invented_column = 'x' LIMIT 5`,
		`SELECT invented_column FROM tool_uses JOIN sessions
		 ON tool_uses.session_id = sessions.session_id LIMIT 5`,
	}
	for _, stmt := range benchCases {
		if _, err := g.Validate(stmt); err == nil {
			t.Errorf("the gate let a non-existent column through a join:\n%s", stmt)
		}
	}
}

// And the defect that was actually reported, stated as what it is: `tool_uses`
// alone does not carry `source_agent`, so this SELECT is wrong and the gate is
// right to reject it. This test exists so that nobody "fixes" the gate into
// accepting it.
func TestAColumnOfAnotherTableWithoutTheJoinIsRejected(t *testing.T) {
	g := gate(t)
	stmt := `SELECT tool_name, COUNT(*) AS usage_count FROM tool_uses
	         WHERE source_agent = 'pi' GROUP BY tool_name ORDER BY usage_count DESC LIMIT 10`

	_, err := g.Validate(stmt)
	if err == nil {
		t.Fatal("tool_uses has no source_agent: accepting this would be accepting SQL that cannot run")
	}
	if !strings.Contains(err.Error(), "source_agent") {
		t.Errorf("the rejection does not name the column: %v", err)
	}
}

// A join that touches a table the gate hides is still rejected: the join must
// not become a way in.
func TestAJoinCannotReachAHiddenTable(t *testing.T) {
	g := gate(t)
	stmt := `SELECT s.project, i.path FROM sessions s
	         JOIN ingest_file_state i ON i.path = s.session_id LIMIT 5`

	if _, err := g.Validate(stmt); err == nil {
		t.Fatal("the join reached a table the gate hides")
	}
}
