package corpuswriter_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/internal/store/exactdedup"
	"github.com/thellmwhisperer/la-roca/pkg/corpuswriter"

	_ "modernc.org/sqlite"
)

func TestWritePersistsTheCorpusTreeWithDedupAndFTS(t *testing.T) {
	db := corpusDatabase(t)
	ctx := context.Background()
	records := corpuswriter.Records{Sessions: []corpuswriter.Session{{
		ID: "public-writer-one", SourceAgent: "synthetic-agent",
		SourceSurface: "Synthetic Surface", Project: "synthetic-project",
		Title: "Synthetic public writer", Metadata: map[string]any{"cwd": "/synthetic/demo"},
		Exchanges: []corpuswriter.Exchange{{
			Number: 1, HumanText: "find the beacon", AgentText: "beacon found",
			Thinking: []corpuswriter.Thinking{{Position: 1, Text: "inspect the signal", WordCount: 3}},
			Tools:    []corpuswriter.ToolUse{{Name: "inspect", ParamsSummary: "beacon"}},
		}},
		OrphanedTools: []corpuswriter.ToolUse{{Name: "interrupt", ParamsSummary: "open turn"}},
	}}}

	first := write(t, ctx, db, records)
	if first.Sessions != 1 || first.Exchanges != 1 || first.ThinkingBlocks != 1 || first.ToolUses != 2 {
		t.Fatalf("first write counts = %+v", first)
	}
	replay := write(t, ctx, db, records)
	if replay.Exchanges != 0 || replay.ThinkingBlocks != 0 || replay.ToolUses != 0 {
		t.Fatalf("replay duplicated children: %+v", replay)
	}

	assertCount(t, db, "sessions", 1)
	assertCount(t, db, "exchanges", 1)
	assertCount(t, db, "thinking_blocks", 1)
	assertCount(t, db, "tool_uses", 2)
	assertCount(t, db, "tool_uses WHERE exchange_number IS NULL", 1)
	assertMatchCount(t, db, "sessions_fts", "synthetic", 1)
	assertMatchCount(t, db, "exchanges_fts", "beacon", 1)
	assertMatchCount(t, db, "thinking_fts", "signal", 1)
}

func TestWriteKeepsExactPayloadCollisionSafety(t *testing.T) {
	db := corpusDatabase(t)
	ctx := context.Background()
	session := func(id string) corpuswriter.Records {
		return corpuswriter.Records{Sessions: []corpuswriter.Session{{
			ID: id, SourceAgent: "synthetic-agent", Project: "synthetic-project",
			Title: "same payload", Metadata: map[string]any{"cwd": "/synthetic/demo"},
		}}}
	}

	write(t, ctx, db, session("public-writer-one"))
	write(t, ctx, db, session("public-writer-two"))
	assertCount(t, db, "sessions", 2)

	var firstCwd, secondCwd sql.NullString
	if err := db.QueryRow(`
		SELECT
		  (SELECT json_extract(metadata, '$.cwd') FROM sessions WHERE session_id = 'public-writer-one'),
		  (SELECT json_extract(metadata, '$.cwd') FROM sessions WHERE session_id = 'public-writer-two')`,
	).Scan(&firstCwd, &secondCwd); err != nil {
		t.Fatal(err)
	}
	if !firstCwd.Valid || firstCwd.String != "/synthetic/demo" {
		t.Fatalf("first metadata cwd = %q, want the committed patch", firstCwd.String)
	}
	if secondCwd.Valid {
		t.Fatalf("second metadata cwd = %q, want its pre-patch metadata unchanged", secondCwd.String)
	}
}

func corpusDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(data.Schema + data.SearchSchema); err != nil {
		t.Fatal(err)
	}
	if err := exactdedup.EnsureGuards(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func write(t *testing.T, ctx context.Context, db *sql.DB,
	records corpuswriter.Records) corpuswriter.Counts {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := corpuswriter.Write(ctx, tx, records)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("public writer aborted: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return counts
}

func assertCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s rows = %d, want %d", table, got, want)
	}
}

func assertMatchCount(t *testing.T, db *sql.DB, table, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+table+" MATCH ?", query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s matches for %q = %d, want %d", table, query, got, want)
	}
}
