package vector

import (
	"context"
	"testing"
)

// Corpus and ops are WAL databases. The delta's reader opens them and can leave
// an empty -wal behind; status right after must still see the sealed generation.
func TestStatusAfterDeltaReportsCompleteUntilASourceIsWritten(t *testing.T) {
	f, corpus, ops, _ := federationFixture(t)
	for _, path := range []string{corpus, ops} {
		mutateSourceDatabase(t, path, `PRAGMA journal_mode=WAL`)
	}
	ctx := context.Background()
	if _, err := f.Ingest(ctx, ""); err != nil {
		t.Fatal(err)
	}
	status := func() map[string]DatabaseVectorization {
		t.Helper()
		report, err := ReportVectorization(ctx, StatusRequest{PluginRoot: f.PluginRoot})
		if err != nil {
			t.Fatal(err)
		}
		rows := map[string]DatabaseVectorization{}
		for _, row := range report.Databases {
			rows[row.Database] = row
		}
		return rows
	}
	for name, row := range status() {
		if row.State != StateComplete || row.CandidateChunks == nil || row.EmbeddedChunks == nil ||
			*row.CandidateChunks != *row.EmbeddedChunks {
			t.Fatalf("%s right after delta: state=%s candidate=%v embedded=%v, want complete with exact count",
				name, row.State, row.CandidateChunks, row.EmbeddedChunks)
		}
	}

	mutateSourceDatabase(t, ops, `INSERT INTO memories VALUES (3,'Written after the delta','active','ops','2026-03-03')`)
	rows := status()
	if rows["ops"].State != StateOutdated {
		t.Fatalf("ops after a write: state=%s, want outdated", rows["ops"].State)
	}
	if rows["corpus"].State != StateComplete || rows["corpus"].CandidateChunks == nil {
		t.Fatalf("untouched corpus: state=%s candidate=%v, want complete", rows["corpus"].State, rows["corpus"].CandidateChunks)
	}
}
