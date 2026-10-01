package vector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/pkg/corpuswriter"
)

func frontierFixture(t *testing.T) (Federation, string) {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "roca-corpus")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "corpus.db")
	createSourceDatabase(t, path, data.Schema+data.VectorChangesSchema)
	tables := []vectorTable{
		{Name: "sessions", IDColumn: "session_id", TextColumns: []string{"title", "project"}, TimeColumns: []string{"started_at"}, Columns: []string{"session_id", "title", "project", "started_at"}},
		{Name: "exchanges", IDColumn: "id", TextColumns: []string{"human_text", "agent_text"}, TimeColumns: []string{"agent_timestamp", "human_timestamp"}, Columns: []string{"id", "session_id", "human_text", "agent_text", "agent_timestamp", "human_timestamp"}, TimeJoin: &vectorTimeJoin{Table: "sessions", LocalColumn: "session_id", ForeignColumn: "session_id", TimeColumns: []string{"started_at"}}},
		{Name: "thinking_blocks", IDColumn: "id", TextColumns: []string{"full_text"}, Columns: []string{"id", "session_id", "full_text"}, TimeJoin: &vectorTimeJoin{Table: "sessions", LocalColumn: "session_id", ForeignColumn: "session_id", TimeColumns: []string{"started_at"}}},
		{Name: "memories", IDColumn: "id", TextColumns: []string{"content"}, TimeColumns: []string{"created_at"}, Columns: []string{"id", "source_session", "content", "created_at", "project"}, TimeJoin: &vectorTimeJoin{Table: "sessions", LocalColumn: "source_session", ForeignColumn: "session_id", TimeColumns: []string{"started_at"}}},
	}
	writeRegistry(t, root, vectorRegistry{Schema: vectorRegistrySchema, Databases: []vectorDatabase{{Plugin: "roca-corpus", Database: "corpus", Path: "corpus.db", Alias: "plugin_roca_corpus", Tables: tables}}})
	f, err := LoadFederation(CoreCLI{Executable: "roca", readRequest: readerFixture(sqliteExecRunner(t, map[string]string{"plugin_roca_corpus": path}))}, root, DefaultModel, "test", &recordingEmbedder{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mutateSourceDatabase(t, path, `INSERT INTO sessions(session_id,title,project,started_at) VALUES ('january','January archive','archive','2026-01-01'),('september','September work','work','2026-09-20');
 WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<40)
 INSERT INTO exchanges(session_id,exchange_number,human_text,agent_text) SELECT 'january',x,'January question '||x,'January answer '||x FROM n;`)
	return f, path
}

func TestCorpusChangeFrontierAppend(t *testing.T) {
	f, path := frontierFixture(t)
	ctx := context.Background()
	first, err := f.Ingest(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.SourcesWalked != 42 || first.Added != 84 {
		t.Fatalf("initial: %+v", first)
	}
	db := openTestSQLite(t, path)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := corpuswriter.Write(ctx, tx, corpuswriter.Records{Sessions: []corpuswriter.Session{{ID: "september", SourceAgent: "claude-code", Title: "September work", Project: "work", StartedAt: "2026-09-20", Exchanges: []corpuswriter.Exchange{{Number: 1, HumanText: "September question", AgentText: "September answer"}}}}})
	if err != nil {
		tx.Rollback()
		db.Close()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if counts.Exchanges != 1 {
		t.Fatalf("append: %+v", counts)
	}
	delta, err := f.Ingest(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if delta.SourcesWalked != 1 || delta.Added != 2 || delta.Unchanged != 0 || delta.Removed != 0 || delta.Chunks != 86 || delta.Sources != 43 {
		t.Fatalf("append: %+v", delta)
	}
	t.Logf("append: sources_walked=%d chunks_added=%d chunks_unchanged=%d total_chunks=%d", delta.SourcesWalked, delta.Added, delta.Unchanged, delta.Chunks)
	mutateSourceDatabase(t, path, `INSERT INTO ingest_file_state(path,source_kind,fingerprint) VALUES ('synthetic','fixture','watermark-only'); UPDATE sessions SET ended_at='2026-09-21' WHERE session_id='september';`)
	delta, err = f.Ingest(ctx, "")
	if err != nil || delta.SourcesWalked != 0 || delta.Added != 0 || delta.Unchanged != 0 || delta.Chunks != 86 {
		t.Fatalf("metadata: %+v %v", delta, err)
	}
	t.Logf("metadata: sources_walked=%d chunks_added=%d chunks_unchanged=%d", delta.SourcesWalked, delta.Added, delta.Unchanged)
	mutateSourceDatabase(t, path, `UPDATE exchanges SET agent_text='corrected answer' WHERE session_id='september'`)
	delta, err = f.Ingest(ctx, "")
	if err != nil || delta.SourcesWalked != 1 || delta.Updated != 1 || delta.Unchanged != 1 {
		t.Fatalf("edit: %+v %v", delta, err)
	}
	mutateSourceDatabase(t, path, `DELETE FROM exchanges WHERE session_id='september'`)
	delta, err = f.Ingest(ctx, "")
	if err != nil || delta.SourcesWalked != 0 || delta.Removed != 2 || delta.Chunks != 84 {
		t.Fatalf("delete: %+v %v", delta, err)
	}
}

func TestCorpusChangeFrontierContextAndRecovery(t *testing.T) {
	for _, change := range []string{"context", "empty", "rollback", "failure", "contract", "model", "restore", "legacy"} {
		t.Run(change, func(t *testing.T) {
			f, path := frontierFixture(t)
			ctx := context.Background()
			if change == "legacy" {
				mutateSourceDatabase(t, path, `DROP TABLE vector_changes`)
			}
			if _, err := f.Ingest(ctx, ""); err != nil {
				t.Fatal(err)
			}
			walked := 42
			switch change {
			case "context":
				mutateSourceDatabase(t, path, `UPDATE sessions SET title='Corrected January' WHERE session_id='january'`)
				walked = 41
			case "empty":
				mutateSourceDatabase(t, path, `UPDATE exchanges SET human_text='',agent_text='' WHERE session_id='january'`)
				walked = 0
			case "rollback":
				mutateSourceDatabase(t, path, `BEGIN; UPDATE exchanges SET agent_text='rolled back'; ROLLBACK; INSERT INTO ingest_file_state(path,source_kind) VALUES ('metadata','fixture');`)
				walked = 0
			case "failure":
				mutateSourceDatabase(t, path, `INSERT INTO exchanges(session_id,human_text,agent_text) VALUES ('september','new question','new answer')`)
				original := f.Embedder
				f.Embedder = frontierFailure{}
				if _, err := f.Ingest(ctx, ""); err == nil {
					t.Fatal("expected embedding failure")
				}
				f.Embedder = original
				walked = 43
			case "model":
				f.Model = "replacement-model"
			case "contract":
				f.databases[0].Tables[1].Chunking = &chunkingHints{MaxChars: intPointer(100)}
			case "restore":
				mutateSourceDatabase(t, path, `UPDATE vector_changes SET token='different-history'; UPDATE exchanges SET agent_text='restored answer' WHERE id=1`)
			case "legacy":
				mutateSourceDatabase(t, path, data.VectorChangesSchema+`UPDATE exchanges SET agent_text='legacy changed' WHERE id=1`)
			}
			delta, err := f.Ingest(ctx, "")
			if err != nil || delta.SourcesWalked != walked {
				t.Fatalf("%s: %+v %v want walked=%d", change, delta, err, walked)
			}
			if change == "context" && delta.Updated != 82 {
				t.Fatalf("context not propagated: %+v", delta)
			}
			if change == "empty" && (delta.Removed != 80 || delta.Chunks != 4) {
				t.Fatalf("emptied sources retained: %+v", delta)
			}
		})
	}
}

type frontierFailure struct{}

func (frontierFailure) Pull(context.Context, string) error { return nil }
func (frontierFailure) Embed(context.Context, string, []string) ([][]float32, error) {
	return nil, fmt.Errorf("synthetic embedding failure")
}

func TestCorpusChangeFrontierDependentSources(t *testing.T) {
	f, path := frontierFixture(t)
	mutateSourceDatabase(t, path, `INSERT INTO thinking_blocks(session_id,full_text) VALUES ('september','September reasoning');
 INSERT INTO memories(layer,origin,source_session,content) VALUES ('project','human','september','September memory');`)
	if _, err := f.Ingest(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	mutateSourceDatabase(t, path, `UPDATE sessions SET title='Revised September' WHERE session_id='september'`)
	delta, err := f.Ingest(t.Context(), "")
	if err != nil || delta.SourcesWalked != 3 || delta.Updated != 4 || delta.Chunks != 86 {
		t.Fatalf("dependent context: %+v %v", delta, err)
	}
	mutateSourceDatabase(t, path, `UPDATE exchanges SET id=1000 WHERE id=1`)
	delta, err = f.Ingest(t.Context(), "")
	if err != nil || delta.SourcesWalked != 1 || delta.Added != 2 || delta.Removed != 2 || delta.Chunks != 86 {
		t.Fatalf("changed identity: %+v %v", delta, err)
	}
}
