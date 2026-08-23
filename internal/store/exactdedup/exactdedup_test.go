package exactdedup_test

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/internal/store/exactdedup"
	_ "modernc.org/sqlite"
)

func TestExactCleanupRemapsSessionsAndPreservesAmbiguousPayloads(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "roca.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(data.Schema + data.SearchSchema); err != nil {
		t.Fatal(err)
	}
	seed := `
		INSERT INTO sessions(session_id,source_agent,title,started_at,metadata) VALUES
		 ('session-a','fixture','Exact fixture','2026-08-16T10:00:00Z','{}'),
		 ('session-b','fixture','Exact fixture','2026-08-16T10:00:00Z','{}'),
		 ('session-near','fixture','Exact fixture','2026-08-16T10:00:00Z','{"different":true}');
		INSERT INTO exchanges(id,session_id,exchange_number,human_text,agent_text) VALUES
		 (1,'session-a',1,'prompt','answer'), (2,'session-b',1,'prompt','answer'),
		 (3,'session-near',NULL,'one','first'), (4,'session-near',NULL,'two','second');
		INSERT INTO thinking_blocks(id,session_id,exchange_number,position_in_session,depth,word_count,full_text) VALUES
		 (1,'session-a',1,1,'deep',2,'exact thought'),
		 (2,'session-b',1,1,'deep',2,'exact thought');
		INSERT INTO memories(id,layer,content,metadata,origin,status,created_at) VALUES
		 (10,'project','exact memory','{}','agent','active','2026-08-16 10:00:00'),
		 (11,'project','exact memory','{}','agent','active','2026-08-16 10:00:01'),
		 (12,'project','near memory','{"different":true}','agent','active','2026-08-16 10:00:02');
		INSERT INTO memories(id,layer,content,metadata,origin,status,supersedes,created_at)
		 VALUES (13,'handoff','lineage','{}','agent','active',11,'2026-08-16 10:00:03');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := exactdedup.Inspect(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	assertTable(t, report, "sessions", 1, 1, 1, 3)
	assertTable(t, report, "exchanges", 1, 1, 1, 2)
	assertTable(t, report, "thinking_blocks", 1, 1, 0, 0)
	assertTable(t, report, "memories", 1, 1, 0, 0)

	backup := filepath.Join(t.TempDir(), "pre-apply.db")
	backedUp, err := exactdedup.Backup(ctx, path, backup)
	if err != nil {
		t.Fatal(err)
	}
	if backedUp.ManifestSHA256 != report.ManifestSHA256 {
		t.Fatalf("backup manifest = %s, want %s", backedUp.ManifestSHA256, report.ManifestSHA256)
	}
	if backedUp.FileSHA256 == "" || backedUp.Bytes == 0 || backedUp.SchemaVersion == 0 {
		t.Fatalf("backup evidence is incomplete: %+v", backedUp)
	}
	after, err := exactdedup.Apply(ctx, path, report.ManifestSHA256, "fixture-run", backup)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range after.Tables {
		if table.Losers != 0 {
			t.Errorf("%s still has %d exact losers", table.Table, table.Losers)
		}
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	counts := map[string]int{
		"SELECT COUNT(*) FROM sessions": 2, "SELECT COUNT(*) FROM exchanges": 3,
		"SELECT COUNT(*) FROM thinking_blocks": 1, "SELECT COUNT(*) FROM memories": 3,
		"SELECT COUNT(*) FROM session_id_remaps WHERE old_id='session-b' AND canonical_id='session-a'": 1,
		"SELECT COUNT(*) FROM exchange_id_remaps WHERE old_id=2 AND canonical_id=1":                    1,
		"SELECT COUNT(*) FROM memory_id_remaps WHERE old_id=11 AND canonical_id=10":                    1,
		"SELECT COUNT(*) FROM memories WHERE id=13 AND supersedes=10":                                  1,
		"SELECT COUNT(*) FROM exchanges WHERE session_id='session-near' AND exchange_number IS NULL":   2,
	}
	for query, want := range counts {
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = nil
	svc, err := service.Open(service.Options{DBPath: path})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := svc.ResolveMemory(ctx, 11)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Alias || resolved.RequestedID != 11 || resolved.CanonicalID != 10 {
		t.Fatalf("alias resolution = %+v", resolved)
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE sessions SET source_agent = COALESCE(source_agent, 'fixture')
		WHERE session_id = 'session-a'`); err != nil {
		t.Fatalf("update a guarded canonical session: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO memories(layer,content,metadata,origin,status)
		VALUES('project','exact memory','{}','agent','active')`); err == nil {
		t.Fatal("the exact memory guard accepted a duplicate")
	}
	if _, err := db.Exec(`INSERT INTO memories(layer,content,metadata,origin,status)
		VALUES('project','exact memory','{"near":true}','agent','active')`); err != nil {
		t.Fatalf("the exact memory guard rejected a near duplicate: %v", err)
	}
	for name, statement := range map[string]string{
		"session": `INSERT INTO sessions(session_id,source_agent,title,started_at,metadata)
			VALUES('session-copy','fixture','Exact fixture','2026-08-16T10:00:00Z','{}')`,
		"exchange": `INSERT INTO exchanges(session_id,exchange_number,human_text,agent_text)
			VALUES('session-a',1,'prompt','answer')`,
		"thinking": `INSERT INTO thinking_blocks(session_id,exchange_number,position_in_session,depth,word_count,full_text)
			VALUES('session-a',1,1,'deep',2,'exact thought')`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("the exact %s guard accepted a duplicate", name)
		}
	}
}

func TestExactPayloadGuardIndexesAHashNotThePayload(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "roca.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(data.Schema + data.SearchSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata)
		VALUES ('session-a', 'fixture', 'hash fixture', '2026-08-16T10:00:00Z', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO exchanges(session_id, exchange_number, human_text, agent_text)
		VALUES ('session-a', 1, 'a long unique prompt that must not live in the index', 'answer')`); err != nil {
		t.Fatal(err)
	}
	if err := exactdedup.EnsureGuards(ctx, db); err != nil {
		t.Fatal(err)
	}
	var statement string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_exchanges_exact_payload'`).
		Scan(&statement); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(statement), "roca_payload_hash(") {
		t.Fatalf("exact-payload index still stores the payload: %s", statement)
	}
	if _, err := db.Exec(`INSERT INTO exchanges(session_id, exchange_number, human_text, agent_text)
		VALUES ('session-a', 1, 'a long unique prompt that must not live in the index', 'answer')`); err == nil {
		t.Fatal("hash exact-payload guard accepted a duplicate")
	}
	if _, err := db.Exec(`INSERT INTO exchanges(session_id, exchange_number, human_text, agent_text)
		VALUES ('session-a', 2, 'a different prompt', 'answer')`); err != nil {
		t.Fatalf("hash exact-payload guard rejected a different payload: %v", err)
	}
}

func TestExactPayloadGuardFramesEmbeddedNULValues(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "roca.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(data.Schema + data.SearchSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata)
		VALUES ('session-a', 'fixture', 'nul fixture', '2026-08-16T10:00:00Z', '{}')`); err != nil {
		t.Fatal(err)
	}
	for _, human := range []string{"a\x00x", "a\x00y"} {
		if _, err := db.Exec(`INSERT INTO exchanges(session_id, exchange_number, human_text, agent_text)
			VALUES ('session-a', 1, ?, 'answer')`, human); err != nil {
			t.Fatal(err)
		}
	}
	if err := exactdedup.EnsureGuards(ctx, db); err != nil {
		t.Fatalf("distinct NUL-bearing payloads collided: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO exchanges(session_id, exchange_number, human_text, agent_text)
		VALUES ('session-a', 1, ?, 'answer')`, "a\x00x"); err == nil {
		t.Fatal("hash exact-payload guard accepted a NUL-bearing duplicate")
	}
}

func TestExactPayloadGuardCanonicalizesSignedZero(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "roca.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(data.Schema + data.SearchSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata)
		VALUES ('session-a', 'fixture', 'zero fixture', '2026-08-16T10:00:00Z', '{}')`); err != nil {
		t.Fatal(err)
	}
	if err := exactdedup.EnsureGuards(ctx, db); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO thinking_blocks
		(session_id, exchange_number, position_in_session, caution_ratio, full_text)
		VALUES ('session-a', 1, 1, ?, 'same thought')`
	if _, err := db.Exec(insert, 0.0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, math.Copysign(0, -1)); err == nil {
		t.Fatal("hash exact-payload guard accepted a signed-zero duplicate")
	}
}

func assertTable(t *testing.T, report exactdedup.DatabaseReport, name string,
	groups, losers, ambiguousGroups, ambiguousRows int) {
	t.Helper()
	for _, table := range report.Tables {
		if table.Table != name {
			continue
		}
		if table.ExactGroups != groups || table.Losers != losers ||
			table.AmbiguousGroups != ambiguousGroups || table.AmbiguousRows != ambiguousRows {
			t.Fatalf("%s report = %+v", name, table)
		}
		return
	}
	t.Fatalf("missing %s report", name)
}
