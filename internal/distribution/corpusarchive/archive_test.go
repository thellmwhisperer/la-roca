package corpusarchive

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
)

func TestMergePreservesAliasesDivergenceAndSourceScopedChildIdentity(t *testing.T) {
	directory := t.TempDir()
	core := createFrozenSource(t, filepath.Join(directory, "core.db"), seedCoreArchive)
	corpus := createFrozenSource(t, filepath.Join(directory, "existing-corpus.db"), seedExistingCorpusArchive)
	destination := filepath.Join(directory, "merged-corpus.db")
	sources := []Source{
		{Database: "core", Path: core.path, SnapshotDigest: core.digest},
		{Database: "plugin:roca-corpus", Path: corpus.path, SnapshotDigest: corpus.digest, ExistingCorpus: true},
	}

	report, err := Merge(t.Context(), destination, sources, Options{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertFamily(t, report, "sessions", FamilyReport{
		Identities: 6, PhysicalRows: 5, ExactAliases: 1, DivergentKeys: 1, FTSRows: 5,
	})
	assertFamily(t, report, "exchanges", FamilyReport{
		Identities: 7, PhysicalRows: 6, ExactAliases: 1, DivergentKeys: 1, FTSRows: 6,
	})
	assertFamily(t, report, "tool_uses", FamilyReport{
		Identities: 5, PhysicalRows: 3, ExactAliases: 2,
	})
	assertFamily(t, report, "thinking_blocks", FamilyReport{
		Identities: 3, PhysicalRows: 2, ExactAliases: 1, FTSRows: 2,
	})
	assertFamily(t, report, "ingest_file_state", FamilyReport{
		Identities: 4, PhysicalRows: 4,
	})
	if report.VerificationDigest == "" {
		t.Fatal("verified merge returned no digest")
	}

	db := openTestDB(t, destination)
	assertCount(t, db, "sessions", 0)
	assertCount(t, db, "exchanges", 0)
	assertCount(t, db, "tool_uses", 0)
	assertCount(t, db, "thinking_blocks", 0)
	assertCount(t, db, "ingest_file_state", 0)
	assertCountQuery(t, db, `SELECT COUNT(*) FROM session_versions_fts
		WHERE session_versions_fts MATCH 'coreonly'`, 1)
	assertCountQuery(t, db, `SELECT COUNT(*) FROM exchange_versions_fts
		WHERE exchange_versions_fts MATCH 'exactanswer'`, 1)
	assertCountQuery(t, db, `SELECT COUNT(*) FROM thinking_block_versions_fts
		WHERE thinking_block_versions_fts MATCH 'sharedthought'`, 1)

	var fingerprint, source string
	if err := db.QueryRow(`SELECT v.fingerprint, h.source_database
		FROM ingest_file_state_heads AS h
		JOIN ingest_file_state_versions AS v USING (version_digest)
		WHERE h.path = '/shared/transcript'`).Scan(&fingerprint, &source); err != nil {
		t.Fatal(err)
	}
	if fingerprint != "corpus-newer" || source != "plugin:roca-corpus" {
		t.Fatalf("shared ingest head = %q from %q", fingerprint, source)
	}
	assertCountQuery(t, db, `SELECT COUNT(*) FROM tool_use_version_memberships
		WHERE source_database = 'core' AND source_row_id IN (10, 11)
		  AND occurrence_ordinal IN (0, 1)`, 2)
	assertCountQuery(t, db, `SELECT COUNT(DISTINCT version_digest)
		FROM tool_use_version_memberships
		WHERE tool_name = 'Read'`, 1)
	assertCountQuery(t, db, `SELECT COUNT(*) FROM corpus_source_rows
		WHERE source_table IN ('tool_uses', 'thinking_blocks')
		  AND source_key IN ('1', '2', '10', '11')`, 0)

	state, err := migrationledger.Inspect(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != migrationledger.StateVerified || state.VerificationDigest != report.VerificationDigest {
		t.Fatalf("migration state = %+v", state)
	}
	var batches int
	if err := db.QueryRow("SELECT COUNT(*) FROM migration_batches").Scan(&batches); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	replayed, err := Merge(t.Context(), destination, sources, Options{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.VerificationDigest != report.VerificationDigest {
		t.Fatalf("replayed digest = %s, want %s", replayed.VerificationDigest, report.VerificationDigest)
	}
	db = openTestDB(t, destination)
	var replayedBatches int
	if err := db.QueryRow("SELECT COUNT(*) FROM migration_batches").Scan(&replayedBatches); err != nil {
		t.Fatal(err)
	}
	if replayedBatches != batches {
		t.Fatalf("replay batches = %d, want %d", replayedBatches, batches)
	}
}

func TestMergeRejectsAnUnverifiedOrWritableSourceIdentity(t *testing.T) {
	directory := t.TempDir()
	source := createFrozenSource(t, filepath.Join(directory, "source.db"), seedExistingCorpusArchive)
	destination := filepath.Join(directory, "destination.db")

	_, err := Merge(t.Context(), destination, []Source{{
		Database: "plugin:roca-corpus", Path: source.path,
		SnapshotDigest: strings.Repeat("0", 64), ExistingCorpus: true,
	}}, Options{})
	if err == nil || !strings.Contains(err.Error(), "digest is") {
		t.Fatalf("wrong snapshot digest error = %v", err)
	}
	if _, err := Merge(t.Context(), source.path, []Source{{
		Database: "plugin:roca-corpus", Path: source.path,
		SnapshotDigest: source.digest, ExistingCorpus: true,
	}}, Options{}); err == nil || !strings.Contains(err.Error(), "writable destination") {
		t.Fatalf("source equals destination error = %v", err)
	}
}

func TestMergeRefusesAChildWithoutDeterministicParentIdentity(t *testing.T) {
	directory := t.TempDir()
	source := createFrozenSource(t, filepath.Join(directory, "source.db"), func(t *testing.T, db *sql.DB) {
		execTest(t, db, `INSERT INTO tool_uses
			(id, session_id, exchange_number, tool_name) VALUES (1, NULL, NULL, 'Read')`)
	})
	_, err := Merge(t.Context(), filepath.Join(directory, "destination.db"), []Source{{
		Database: "plugin:roca-corpus", Path: source.path,
		SnapshotDigest: source.digest, ExistingCorpus: true,
	}}, Options{BatchSize: 1})
	if err == nil || !strings.Contains(err.Error(), "no deterministic parent turn") {
		t.Fatalf("missing child identity error = %v", err)
	}
}

func TestMergeEncodesAnEmptyLegacySessionIDAsANonemptyMembership(t *testing.T) {
	directory := t.TempDir()
	source := createFrozenSource(t, filepath.Join(directory, "source.db"), func(t *testing.T, db *sql.DB) {
		seedSession(t, db, "", "empty legacy identity")
		execTest(t, db, `INSERT INTO exchanges
			(id, session_id, exchange_number, human_text, agent_text)
			VALUES (1, '', NULL, 'legacy fork', 'legacy answer')`)
		execTest(t, db, `INSERT INTO tool_uses
			(id, session_id, exchange_number, tool_name) VALUES (1, '', NULL, 'Read')`)
		execTest(t, db, `INSERT INTO thinking_blocks
			(id, session_id, exchange_number, full_text) VALUES (1, '', NULL, 'legacy thought')`)
	})
	destination := filepath.Join(directory, "destination.db")
	_, err := Merge(t.Context(), destination, []Source{{
		Database: "plugin:roca-corpus", Path: source.path,
		SnapshotDigest: source.digest, ExistingCorpus: true,
	}}, Options{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	db := openTestDB(t, destination)
	var sourceKey, legacyID string
	if err := db.QueryRow(`SELECT source_key, session_id FROM corpus_source_rows
		WHERE source_table = 'sessions'`).Scan(&sourceKey, &legacyID); err != nil {
		t.Fatal(err)
	}
	if sourceKey == "" || legacyID != "" {
		t.Fatalf("encoded source key = %q, legacy id = %q", sourceKey, legacyID)
	}
	assertCountQuery(t, db, `SELECT COUNT(*) FROM corpus_source_rows
		WHERE source_table IN ('exchanges', 'tool_uses', 'thinking_blocks')
		  AND exchange_number IS NULL AND occurrence_ordinal = 0`, 3)
}

type frozenFixture struct {
	path, digest string
}

func createFrozenSource(t *testing.T, path string, seed func(*testing.T, *sql.DB)) frozenFixture {
	t.Helper()
	db := openTestDB(t, path)
	if _, err := db.Exec(data.Schema); err != nil {
		t.Fatal(err)
	}
	seed(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	digest, err := SnapshotDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	return frozenFixture{path: path, digest: digest}
}

func seedCoreArchive(t *testing.T, db *sql.DB) {
	seedSession(t, db, "exact", "exact title")
	seedSession(t, db, "divergent", "core divergent")
	seedSession(t, db, "core-only", "coreonly title")
	seedExchange(t, db, 1, "exact", 1, "exact question", "exactanswer")
	seedExchange(t, db, 2, "divergent", 1, "question", "core answer")
	seedExchange(t, db, 3, "divergent", 2, "core extra", "core extra answer")
	seedExchange(t, db, 4, "core-only", 1, "core only", "core only answer")
	execTest(t, db, `INSERT INTO tool_uses VALUES
		(10, 'exact', 1, 'Read', 'shared params', 0, NULL, 'responsive'),
		(11, 'exact', 1, 'Read', 'shared params', 0, NULL, 'responsive'),
		(12, 'divergent', 1, 'Write', 'core params', 0, NULL, 'proactive')`)
	execTest(t, db, `INSERT INTO thinking_blocks VALUES
		(1, 'exact', 1, 1.0, 'medium', 0.1, 2, 0, 'sharedthought'),
		(2, 'divergent', 1, 1.0, 'deep', 0.2, 2, 0, 'core thought')`)
	seedState(t, db, "/shared/transcript", "core-older")
	seedState(t, db, "/core/transcript", "core-only")
}

func seedExistingCorpusArchive(t *testing.T, db *sql.DB) {
	seedSession(t, db, "exact", "exact title")
	seedSession(t, db, "divergent", "corpus divergent")
	seedSession(t, db, "corpus-only", "corpus only")
	seedExchange(t, db, 100, "exact", 1, "exact question", "exactanswer")
	seedExchange(t, db, 101, "divergent", 1, "question", "corpus answer")
	seedExchange(t, db, 102, "divergent", 3, "corpus extra", "corpus extra answer")
	execTest(t, db, `INSERT INTO tool_uses VALUES
		(10, 'exact', 1, 'Different', 'colliding integer id', 1, 'synthetic error', 'responsive'),
		(99, 'exact', 1, 'Read', 'shared params', 0, NULL, 'responsive')`)
	execTest(t, db, `INSERT INTO thinking_blocks VALUES
		(1, 'exact', 1, 1.0, 'medium', 0.1, 2, 0, 'sharedthought')`)
	seedState(t, db, "/shared/transcript", "corpus-newer")
	seedState(t, db, "/corpus/transcript", "corpus-only")
}

func seedSession(t *testing.T, db *sql.DB, id, title string) {
	t.Helper()
	execTest(t, db, `INSERT INTO sessions
		(session_id, source_agent, project, started_at, title, metadata)
		VALUES (?, 'synthetic-agent', 'synthetic-project', '2026-08-15T00:00:00Z', ?, '{}')`, id, title)
}

func seedExchange(t *testing.T, db *sql.DB, id int, session string, number int, human, agent string) {
	t.Helper()
	execTest(t, db, `INSERT INTO exchanges
		(id, session_id, exchange_number, human_text, agent_text, model, provider)
		VALUES (?, ?, ?, ?, ?, 'synthetic-model', 'synthetic-provider')`,
		id, session, number, human, agent)
}

func seedState(t *testing.T, db *sql.DB, path, fingerprint string) {
	t.Helper()
	execTest(t, db, `INSERT INTO ingest_file_state
		(path, source_kind, source_agent, project, fingerprint, last_synced_at, metadata)
		VALUES (?, 'synthetic-kind', 'synthetic-agent', 'synthetic-project', ?,
		        '2026-08-15T00:00:00Z', '{}')`, path, fingerprint)
}

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func execTest(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func assertFamily(t *testing.T, report Report, name string, want FamilyReport) {
	t.Helper()
	if got := report.Families[name]; got != want {
		t.Fatalf("%s report = %+v, want %+v", name, got, want)
	}
}

func assertCount(t *testing.T, db *sql.DB, table string, want int64) {
	t.Helper()
	assertCountQuery(t, db, "SELECT COUNT(*) FROM "+table, want)
}

func assertCountQuery(t *testing.T, db *sql.DB, query string, want int64) {
	t.Helper()
	var got int64
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("count for %q = %d, want %d", query, got, want)
	}
}
