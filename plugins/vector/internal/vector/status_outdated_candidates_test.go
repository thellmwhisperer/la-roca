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
		if pending := *row.CandidateChunks; pending != 1 && pending != *row.EmbeddedChunks+1 {
			t.Fatalf("ops candidate_chunks=%d embedded=%d, want one chunk still to embed", pending, *row.EmbeddedChunks)
		}
		return
	}
	t.Fatal("status has no ops row")
}
