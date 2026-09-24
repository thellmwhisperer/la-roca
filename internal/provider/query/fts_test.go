package query_test

import (
	"database/sql"
	"slices"
	"testing"

	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	_ "modernc.org/sqlite"
)

func TestPlaygroundFTSRenderersExecuteAcrossSources(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(data.Schema + data.SearchSchema + `
		INSERT INTO memories(id, layer, content, origin) VALUES (1, 'fact', 'cobalt old', 'agent');
		INSERT INTO memories(layer, content, origin, supersedes) VALUES ('fact', 'cobalt current', 'agent', 1);
		INSERT INTO sessions(session_id) VALUES ('fixture');
		INSERT INTO exchanges(session_id, human_text, agent_text) VALUES ('fixture', 'cobalt human', 'cobalt agent');
		INSERT INTO thinking_blocks(session_id, full_text) VALUES ('fixture', 'cobalt thinking');`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, layer string
		any         bool
		want        []string
	}{
		{"any word across sources", "", true, []string{"cobalt agent", "cobalt current", "cobalt human", "cobalt thinking"}},
		{"all words required", "", false, nil},
		{"layer restricts to current memories", "fact", true, []string{"cobalt current"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := query.Plan{Template: query.TemplateSearchByTerm, Term: "cobalt+absent", Layer: tc.layer}
			render := query.RenderSQLFTS
			if tc.any {
				render = query.RenderSQLFTSAny
			}
			stmt, err := render(plan, nil, 10)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := db.Query("SELECT text FROM (" + stmt + ")")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var got []string
			for rows.Next() {
				var text string
				if err := rows.Scan(&text); err != nil {
					t.Fatal(err)
				}
				got = append(got, text)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("rendered search returned %v, want %v", got, tc.want)
			}
		})
	}
}
