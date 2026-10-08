package vector

import (
	"context"
	"testing"
)

// After a completed delta, one row written to ops leaves exactly one chunk to
// embed. Status must say how far behind ops is instead of null.
func TestStatusAfterDeltaCountsCandidatesStillToEmbed(t *testing.T) {
	f, _, ops, _ := federationFixture(t)
	ctx := context.Background()
	if _, err := f.Ingest(ctx, ""); err != nil {
		t.Fatal(err)
	}
	mutateSourceDatabase(t, ops, `INSERT INTO memories VALUES (3,'Written after the delta','active','ops','2026-03-03')`)
	report, err := ReportVectorization(ctx, StatusRequest{PluginRoot: f.PluginRoot})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range report.Databases {
		if row.Database != "ops" {
			continue
		}
		if row.State != StateOutdated || row.CandidateChunks == nil || row.EmbeddedChunks == nil {
			t.Fatalf("ops after a write: state=%s candidate=%v, want outdated with a numeric candidate count",
				row.State, row.CandidateChunks)
		}
		const expectedCandidates = 3 // Three short fixture memories each produce one chunk.
		if got := *row.CandidateChunks; got != expectedCandidates {
			t.Fatalf("ops candidate_chunks=%d, want %d", got, expectedCandidates)
		}
		return
	}
	t.Fatal("status has no ops row")
}
