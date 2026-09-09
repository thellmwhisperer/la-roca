package service

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

// TestCostHubFTS observes the actual connection, including FTS shadow tables.
// The lab owns all rows; opening or searching an operator federation is forbidden.
func TestCostHubFTS(t *testing.T) {
	fixture := newHubFixture(t)
	seedHubCoreMemory(t, fixture.plugins, 101, "Synthetic quartz memory")
	db := openSQLite(t, filepath.Join(fixture.plugins, rocacorpus.Name, rocacorpus.DatabaseFilename))
	_, err := db.Exec(`INSERT INTO sessions(session_id, source_agent, title) VALUES ('lab', 'fixture', 'quartz');
 WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<10000)
 INSERT INTO exchanges(session_id, exchange_number, human_text, agent_text)
 SELECT 'lab', i, 'quartz human', 'quartz agent' FROM n;
 INSERT INTO thinking_blocks(session_id, full_text) VALUES ('lab', 'quartz thinking');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	svc := openHubService(t, fixture, LayoutCutover, nil)
	for _, tc := range []struct {
		table string
		count int
	}{
		{"exchanges_fts", 10000}, {"thinking_fts", 1}, {"sessions_fts", 1},
	} {
		t.Run(tc.table, func(t *testing.T) {
			stmt := fmt.Sprintf("SELECT COUNT(*) AS n FROM plugin_roca_corpus.%s WHERE %s MATCH 'quartz'", tc.table, tc.table)
			start := time.Now()
			result, err := svc.Exec(t.Context(), ExecRequest{SQL: stmt})
			elapsed := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(result.Rows[0]["n"]) != fmt.Sprint(tc.count) {
				t.Fatalf("result: %+v", result)
			}
			var objects int
			if err := svc.hub.QueryRow(`SELECT COUNT(*) FROM sqlite_temp_master WHERE name LIKE '%_fts%'`).Scan(&objects); err != nil {
				t.Fatal(err)
			}
			if objects != 0 {
				t.Fatalf("qualified query created %d temporary FTS objects", objects)
			}
			if elapsed >= 200*time.Millisecond {
				t.Fatalf("qualified FTS took %s; budget 200 ms", elapsed)
			}
			t.Logf("qualified FTS: %s; temporary FTS objects: %d", elapsed, objects)
			_, err = svc.Exec(t.Context(), ExecRequest{SQL: strings.Replace(stmt, "plugin_roca_corpus.", "", 1)})
			if err == nil || !strings.Contains(err.Error(), "plugin_roca_corpus."+tc.table) {
				t.Fatalf("unqualified suggestion: %v", err)
			}
		})
	}
	_, rows, _, provenance, _, err := svc.SearchByTerm(t.Context(), query.Plan{Term: "quartz", Layer: "project", Limit: 10}, "", DefaultMaxChars, false, PluginRoute{IncludeCore: true})
	if err != nil || len(rows) != 1 || fmt.Sprint(rows[0]["id"]) != "101" || provenance.Method != "fts" {
		t.Fatalf("legacy identity: rows=%v provenance=%+v err=%v", rows, provenance, err)
	}
	_, allRows, _, _, _, err := svc.SearchByTerm(t.Context(), query.Plan{Term: "quartz", Limit: 1000}, "", DefaultMaxChars, false, PluginRoute{IncludeCore: true})
	if err != nil || len(allRows) == 0 {
		t.Fatalf("legacy corpus search: rows=%d err=%v", len(allRows), err)
	}

	var tables int
	if err := svc.hub.QueryRow(`SELECT COUNT(*) FROM sqlite_temp_master WHERE type = 'table' AND name LIKE '%_fts%'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("legacy search created %d temporary FTS tables", tables)
	}
}
