package rocaops

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	_ "modernc.org/sqlite"
)

type custodyFixture struct {
	core, corpus, ops, snapshots string
}

type fixtureMemory struct {
	id         int64
	layer      string
	content    string
	metadata   string
	origin     string
	status     string
	createdAt  string
	supersedes any
}

func TestDATA2MovesEveryMemoryIdentityIntoVerifiedOpsShadowCustody(t *testing.T) {
	fixture := largeCustodyFixture(t)
	options := MemoryCustodyOptions{
		CorePath: fixture.core, CorpusPath: fixture.corpus, OpsPath: fixture.ops,
		SnapshotDir: fixture.snapshots, LockPath: filepath.Join(t.TempDir(), ".roca.lock"),
		BatchSize: 73,
	}
	report, err := MigrateMemoryCustody(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != migrationledger.StateVerified || report.Memberships != 2114+481+25 ||
		report.PhysicalRecords != 2114+(481-166)+25 || report.FTSRecords != report.PhysicalRecords ||
		len(report.Snapshots) != 3 || report.VerificationDigest == "" {
		t.Fatalf("DATA-2 report = %+v", report)
	}

	db := openCustodyDB(t, fixture.ops)
	defer db.Close()
	assertCustodyCount(t, db, "SELECT COUNT(*) FROM memories", 25)
	assertCustodyCount(t, db, `SELECT COUNT(*) FROM memory_compatibility AS legacy
		JOIN memories AS served ON legacy.source_database = 'plugin:roca-ops'
		 AND legacy.id = served.id AND legacy.content = served.content`, 25)
	assertCustodyCount(t, db, `SELECT COUNT(DISTINCT physical_id)
		FROM memory_compatibility WHERE source_database = 'core'`, 2114)
	assertCustodyCount(t, db, `SELECT COUNT(*) FROM (
		SELECT physical_id FROM memory_compatibility
		GROUP BY physical_id HAVING COUNT(DISTINCT source_database) > 1)`, 166)

	probes := []struct {
		name, query string
		want        int
	}{
		{"nine core duplicate identities", `SELECT COUNT(*) FROM memory_compatibility
			WHERE source_database = 'core' AND content = 'duplicate dupmarker000'`, 9},
		{"nine corpus aliases", `SELECT COUNT(*) FROM memory_compatibility
			WHERE source_database = 'plugin:roca-corpus' AND content = 'duplicate dupmarker000'`, 9},
		{"nine physical duplicate records", `SELECT COUNT(DISTINCT physical_id) FROM memory_compatibility
			WHERE content = 'duplicate dupmarker000'`, 9},
		{"eighteen legacy FTS results", `SELECT COUNT(*) FROM memory_records_fts AS hits
			JOIN memory_compatibility AS legacy ON legacy.physical_id = hits.rowid
			WHERE memory_records_fts MATCH 'dupmarker000'`, 18},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) { assertCustodyCount(t, db, probe.query, probe.want) })
	}
	var database string
	var id int64
	if err := db.QueryRow(`SELECT source_database, id FROM memory_compatibility
		WHERE source_database = 'core' AND id = 9346`).Scan(&database, &id); err != nil {
		t.Fatal(err)
	}
	if database != coreMemorySource || id != 9346 {
		t.Fatalf("legacy identity = %s/%d", database, id)
	}
	var provenance string
	if err := db.QueryRow(`SELECT provenance FROM memory_compatibility
		WHERE source_database = 'plugin:roca-corpus' AND id = 1`).Scan(&provenance); err != nil {
		t.Fatal(err)
	}
	if provenance != "harvest-file" {
		t.Fatalf("corpus provenance = %q", provenance)
	}
	assertCustodyCount(t, db, `SELECT COUNT(DISTINCT physical_id) FROM memory_compatibility
		WHERE content = 'unique-core-1035'`, 2)
	assertCustodyCount(t, db, `SELECT COUNT(*) FROM memory_compatibility
		WHERE source_database = 'core' AND supersedes = 11458`, 1)
	coreSource := openCustodyDB(t, fixture.core)
	assertCustodyCount(t, coreSource, "SELECT COUNT(*) FROM memories", 2114)
	if err := coreSource.Close(); err != nil {
		t.Fatal(err)
	}
	corpusSource := openCustodyDB(t, fixture.corpus)
	assertCustodyCount(t, corpusSource, "SELECT COUNT(*) FROM memories", 481)
	if err := corpusSource.Close(); err != nil {
		t.Fatal(err)
	}

	beforeSnapshots := snapshotCount(t, fixture.snapshots)
	second, err := MigrateMemoryCustody(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != migrationledger.StateVerified || second.Memberships != report.Memberships ||
		second.PhysicalRecords != report.PhysicalRecords || snapshotCount(t, fixture.snapshots) != beforeSnapshots {
		t.Fatalf("idempotent rerun = %+v; snapshots before=%d after=%d",
			second, beforeSnapshots, snapshotCount(t, fixture.snapshots))
	}
}

func TestDATA2ResumesAfterACommittedBatchWithoutHalfRows(t *testing.T) {
	fixture := smallCustodyFixture(t)
	interrupted := errors.New("synthetic stop after a committed batch")
	committed := 0
	options := MemoryCustodyOptions{
		CorePath: fixture.core, CorpusPath: fixture.corpus, OpsPath: fixture.ops,
		SnapshotDir: fixture.snapshots, BatchSize: 2,
		AfterBatch: func(MemoryBatch) error {
			committed++
			if committed < 2 {
				return nil
			}
			return interrupted
		},
	}
	stopped, err := MigrateMemoryCustody(t.Context(), options)
	if !errors.Is(err, interrupted) {
		t.Fatalf("interrupted migration = %v", err)
	}
	if stopped.State != migrationledger.StateBatchInProgress || len(stopped.Snapshots) != 3 {
		t.Fatalf("interrupted report = %+v", stopped)
	}
	db := openCustodyDB(t, fixture.ops)
	assertCustodyCount(t, db, "SELECT COUNT(*) FROM migration_batches", 2)
	assertCustodyCount(t, db, "SELECT COUNT(*) FROM custody_memberships", 3)
	assertCustodyCount(t, db, "SELECT COUNT(*) FROM memory_records", 3)
	var state string
	if err := db.QueryRow("SELECT migration_state FROM plugin_schema").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if state != string(migrationledger.StateBatchInProgress) {
		t.Fatalf("interrupted state = %q", state)
	}
	driftCore(t, fixture.core)

	options.AfterBatch = nil
	beforeSnapshots := snapshotCount(t, fixture.snapshots)
	report, err := MigrateMemoryCustody(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != migrationledger.StateVerified || report.Memberships != 10 ||
		report.PhysicalRecords != 8 || report.FTSRecords != 8 ||
		snapshotCount(t, fixture.snapshots) != beforeSnapshots {
		t.Fatalf("resumed report = %+v; snapshots before=%d after=%d", report,
			beforeSnapshots, snapshotCount(t, fixture.snapshots))
	}
	want := []MemoryDrift{
		{SourceDatabase: coreMemorySource, SourceKey: "10#2", Kind: MemoryDriftMutated},
		{SourceDatabase: coreMemorySource, SourceKey: "11", Kind: MemoryDriftDeleted},
	}
	got := make([]MemoryDrift, 0, len(report.Drift))
	for _, event := range report.Drift {
		got = append(got, MemoryDrift{SourceDatabase: event.SourceDatabase,
			SourceKey: event.SourceKey, Kind: event.Kind})
	}
	slices.SortFunc(got, func(left, right MemoryDrift) int {
		return strings.Compare(left.SourceKey, right.SourceKey)
	})
	if !slices.Equal(got, want) {
		t.Fatalf("drift = %+v, want %+v", got, want)
	}

	resumed := openCustodyDB(t, fixture.ops)
	defer resumed.Close()
	probes := []struct {
		name, query string
		want        int
	}{
		{"the mutated row keeps both versions under one legacy id",
			"SELECT COUNT(*) FROM memory_compatibility WHERE source_database = 'core' AND id = 10", 2},
		{"the deleted row keeps the membership its batch recorded",
			"SELECT COUNT(*) FROM memory_compatibility WHERE source_database = 'core' AND id = 11", 1},
		{"the rewritten payload is searchable in the shadow index",
			`SELECT COUNT(*) FROM memory_records_fts WHERE memory_records_fts MATCH 'rewritten'`, 1},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) { assertCustodyCount(t, resumed, probe.query, probe.want) })
	}
}

func TestDATA2VerifiesAVirginHomeWithNothingToCarry(t *testing.T) {
	fixture := newCustodyFixture(t)
	insertFixtureMemories(t, fixture.core, false, nil)
	insertFixtureMemories(t, fixture.corpus, true, nil)
	insertOpsMemories(t, fixture.ops, 0)
	options := MemoryCustodyOptions{
		CorePath: fixture.core, CorpusPath: fixture.corpus, OpsPath: fixture.ops,
		SnapshotDir: fixture.snapshots,
	}
	report, err := MigrateMemoryCustody(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != migrationledger.StateVerified || report.Memberships != 0 ||
		report.PhysicalRecords != 0 || report.VerificationDigest == "" {
		t.Fatalf("virgin home report = %+v", report)
	}
	again, err := MigrateMemoryCustody(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if again.State != migrationledger.StateVerified || again.Memberships != 0 {
		t.Fatalf("virgin home rerun = %+v", again)
	}
}

// driftCore moves the source under an interrupted migration the way an ordinary
// ingest would: one committed row is rewritten, one is dropped, one is appended.
func driftCore(t *testing.T, path string) {
	t.Helper()
	core := openCustodyDB(t, path)
	statements := []string{
		`UPDATE memories SET content = 'core alpha rewritten' WHERE id = 10`,
		`DELETE FROM memories WHERE id = 11`,
		`INSERT INTO memories (id, layer, content, metadata, origin, status, created_at)
			VALUES (14, 'project', 'core appended between runs', '{}', 'agent', 'active', '2026-08-01')`,
	}
	for _, statement := range statements {
		if _, err := core.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := core.Close(); err != nil {
		t.Fatal(err)
	}
}

func largeCustodyFixture(t *testing.T) custodyFixture {
	t.Helper()
	fixture := newCustodyFixture(t)
	core := make([]fixtureMemory, 0, 2114)
	row := 0
	for group := range 129 {
		size := 8
		if group < 3 {
			size = 9
		}
		for range size {
			core = append(core, fixtureMemory{
				id: 9346 + int64(row), layer: "project",
				content:  fmt.Sprintf("duplicate dupmarker%03d", group),
				metadata: fmt.Sprintf(`{"group":%d}`, group), origin: "agent", status: "active",
				createdAt: fmt.Sprintf("2026-08-01 00:%02d:00", group%60),
			})
			row++
		}
	}
	for len(core) < 2114 {
		index := len(core)
		memory := fixtureMemory{
			id: 9346 + int64(index), layer: "project", content: fmt.Sprintf("unique-core-%d", index),
			metadata: fmt.Sprintf(`{"core":%d}`, index), origin: "agent", status: "active",
			createdAt: "2026-08-02 00:00:00",
		}
		if index == 2113 {
			memory.supersedes = int64(11458)
		}
		core = append(core, memory)
	}
	insertFixtureMemories(t, fixture.core, false, core)

	corpus := make([]fixtureMemory, 0, 481)
	for index := range 481 {
		if index < 166 {
			memory := core[index]
			memory.id = int64(index + 1)
			corpus = append(corpus, memory)
			continue
		}
		if index == 166 {
			memory := core[1035]
			memory.id = int64(index + 1)
			memory.metadata = `{"divergent":true}`
			corpus = append(corpus, memory)
			continue
		}
		corpus = append(corpus, fixtureMemory{
			id: int64(index + 1), layer: "project", content: fmt.Sprintf("unique-corpus-%d", index),
			metadata: fmt.Sprintf(`{"corpus":%d}`, index), origin: "cron", status: "active",
			createdAt: "2026-08-03 00:00:00",
		})
	}
	insertFixtureMemories(t, fixture.corpus, true, corpus)
	insertOpsMemories(t, fixture.ops, 25)
	return fixture
}

func smallCustodyFixture(t *testing.T) custodyFixture {
	t.Helper()
	fixture := newCustodyFixture(t)
	core := []fixtureMemory{
		{id: 10, layer: "project", content: "shared alpha", metadata: `{}`, origin: "agent", status: "active", createdAt: "2026-08-01"},
		{id: 11, layer: "project", content: "shared alpha", metadata: `{}`, origin: "agent", status: "active", createdAt: "2026-08-01"},
		{id: 12, layer: "project", content: "core beta", metadata: `{}`, origin: "agent", status: "active", createdAt: "2026-08-01"},
		{id: 13, layer: "project", content: "core gamma", metadata: `{}`, origin: "agent", status: "active", createdAt: "2026-08-01"},
	}
	corpus := []fixtureMemory{
		{id: 1, layer: "project", content: "shared alpha", metadata: `{}`, origin: "agent", status: "active", createdAt: "2026-08-01"},
		{id: 2, layer: "project", content: "shared alpha", metadata: `{}`, origin: "agent", status: "active", createdAt: "2026-08-01"},
		{id: 3, layer: "project", content: "corpus delta", metadata: `{}`, origin: "cron", status: "active", createdAt: "2026-08-01"},
	}
	insertFixtureMemories(t, fixture.core, false, core)
	insertFixtureMemories(t, fixture.corpus, true, corpus)
	insertOpsMemories(t, fixture.ops, 1)
	return fixture
}

func newCustodyFixture(t *testing.T) custodyFixture {
	t.Helper()
	root := t.TempDir()
	return custodyFixture{
		core: filepath.Join(root, "core.db"), corpus: filepath.Join(root, "corpus.db"),
		ops: filepath.Join(root, DatabaseFilename), snapshots: filepath.Join(root, "snapshots"),
	}
}

func insertFixtureMemories(t *testing.T, path string, corpus bool, memories []fixtureMemory) {
	t.Helper()
	db := openCustodyDB(t, path)
	provenance := ""
	if corpus {
		provenance = `, provenance TEXT NOT NULL DEFAULT 'harvest-file'`
	}
	if _, err := db.Exec(`CREATE TABLE memories (
		id INTEGER PRIMARY KEY, layer TEXT NOT NULL, content TEXT NOT NULL, metadata TEXT,
		origin TEXT NOT NULL, source_agent TEXT, source_model TEXT, source_surface TEXT,
		source_session TEXT, source_sequence INTEGER, project TEXT, status TEXT,
		supersedes INTEGER, created_at TEXT` + provenance + `)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement := `INSERT INTO memories
		(id, layer, content, metadata, origin, status, supersedes, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	for _, memory := range memories {
		if _, err := tx.Exec(statement, memory.id, memory.layer, memory.content, memory.metadata,
			memory.origin, memory.status, memory.supersedes, memory.createdAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func insertOpsMemories(t *testing.T, path string, count int) {
	t.Helper()
	if err := ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	db := openCustodyDB(t, path)
	for index := range count {
		if _, err := db.Exec(`INSERT INTO memories (layer, content, metadata, origin, status, created_at)
			VALUES ('handoff', ?, '{}', 'agent', 'active', '2026-08-04 00:00:00')`,
			fmt.Sprintf("existing ops %d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func openCustodyDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func assertCustodyCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	scanErr := db.QueryRow(query).Scan(&got)
	switch {
	case scanErr != nil:
		t.Fatalf("count %s: %v", query, scanErr)
	case got != want:
		t.Fatalf("count = %d, want %d for %s", got, want, query)
	}
}

func snapshotCount(t *testing.T, directory string) int {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
