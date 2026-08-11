package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexAggregateIngestSplitsExcludesAndReingestsIdempotently(t *testing.T) {
	world := newWorld(t)
	roots := world.roots()
	memoryDir := filepath.Join(roots.CodexRoot, "memories")
	aggregate := filepath.Join(memoryDir, "raw_memories.md")
	fixture, err := os.ReadFile("testdata/codex_raw_memories.fixture")
	if err != nil {
		t.Fatal(err)
	}
	world.write(t, aggregate, string(fixture))
	world.write(t, filepath.Join(memoryDir, "MEMORY.md"), "# Derived index\n\nSynthetic downstream content.\n")
	world.write(t, filepath.Join(memoryDir, "memory_summary.md"), "# Derived summary\n\nSynthetic downstream content.\n")

	db := rocaDatabase(t)
	opts := Options{Roots: roots}
	// A deployment upgrading from the old parser can already have one row keyed
	// by the aggregate path. Per-thread synthetic paths must coexist with it
	// instead of updating it or preventing the split rows from landing.
	exec(t, db.SQL(), `INSERT INTO memories
		(layer, content, metadata, origin, source_agent)
		VALUES ('feedback', 'legacy synthetic whole blob', ?, 'cron', 'codex')`,
		`{"_cron_source":"codex","file_path":"`+aggregate+`"}`)
	first, err := Run(context.Background(), db, registry(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.FilesExcluded != 2 || first.RecordsDiscarded != 2 {
		t.Fatalf("excluded/discarded = %d/%d, want 2/2: %+v",
			first.FilesExcluded, first.RecordsDiscarded, first.DiscardDetails)
	}
	if first.Scanned["codex_files"] != 6 {
		t.Errorf("codex files scanned = %d, want 6 including two honest exclusions",
			first.Scanned["codex_files"])
	}
	if got := countRows(t, db.SQL(), `memories WHERE json_extract(metadata, '$.aggregate_file_path') = '`+aggregate+`'`); got != 2 {
		t.Fatalf("aggregate memories = %d, want 2", got)
	}
	if got := countRows(t, db.SQL(), `memories WHERE content = 'legacy synthetic whole blob'`); got != 1 {
		t.Errorf("legacy aggregate row was overwritten or duplicated: %d", got)
	}
	if got := countRows(t, db.SQL(), `memories WHERE content LIKE '%Derived %'`); got != 0 {
		t.Errorf("derived aggregates ingested = %d, want none", got)
	}

	rows, err := db.SQL().Query(`
		SELECT json_extract(metadata, '$.thread_id'), project, created_at
		FROM memories WHERE json_extract(metadata, '$.aggregate_file_path') = ? ORDER BY id`, aggregate)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := [][]string{
		{"11111111-2222-3333-4444-555555555555", "atlas", "2026-08-01T10:20:30Z"},
		{"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "beacon", "2026-08-02T11:22:33.456Z"},
	}
	for i := 0; rows.Next(); i++ {
		var id, project, stamp string
		if err := rows.Scan(&id, &project, &stamp); err != nil {
			t.Fatal(err)
		}
		if i >= len(want) || id != want[i][0] || project != want[i][1] || stamp != want[i][2] {
			t.Errorf("row %d = %q/%q/%q, want %v", i+1, id, project, stamp, want[i])
		}
	}

	// Defeat the file-level fingerprint so the stable per-thread identity is the
	// contract under test, not merely the fast-path skip.
	touchFuture(t, aggregate)
	second, err := Run(context.Background(), db, registry(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	if second.Delta != (Tables{}) || countRows(t, db.SQL(),
		`memories WHERE json_extract(metadata, '$.aggregate_file_path') = '`+aggregate+`'`) != 2 {
		t.Errorf("second ingest duplicated aggregate blocks: delta=%+v", second.Delta)
	}

	updatedStamp := "2026-08-03T12:34:56Z"
	world.write(t, aggregate, strings.Replace(string(fixture),
		"2026-08-01T10:20:30Z", updatedStamp, 1))
	third, err := Run(context.Background(), db, registry(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	if third.Sources["codex"].MemoriesUpdated != 1 || countRows(t, db.SQL(),
		`memories WHERE json_extract(metadata, '$.aggregate_file_path') = '`+aggregate+`'`) != 2 {
		t.Errorf("changed thread was not updated in place: %+v", third.Sources["codex"])
	}
	var storedStamp string
	if err := db.SQL().QueryRow(`SELECT created_at FROM memories
		WHERE json_extract(metadata, '$.thread_id') = '11111111-2222-3333-4444-555555555555'`).
		Scan(&storedStamp); err != nil || storedStamp != updatedStamp {
		t.Errorf("updated source timestamp = %q, err=%v, want %q", storedStamp, err, updatedStamp)
	}
}
