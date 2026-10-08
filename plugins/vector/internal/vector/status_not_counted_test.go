package vector

import (
	"context"
	"testing"
)

func TestStatusSaysCandidatesNotCountedWithoutHighWaterID(t *testing.T) {
	f, _, ops, _ := federationFixture(t)
	ctx := context.Background()
	if _, err := f.Ingest(ctx, ""); err != nil {
		t.Fatal(err)
	}
	mutateSourceDatabase(t, ops, `INSERT INTO memories VALUES (3,'Written after the delta','active','ops','2026-03-03')`)
	sidecar := openTestSQLite(t, SidecarPath(ops))
	if _, err := sidecar.Exec(`DELETE FROM meta WHERE key='progress_identity'`); err != nil {
		sidecar.Close()
		t.Fatal(err)
	}
	if err := sidecar.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := ReportVectorization(ctx, StatusRequest{PluginRoot: f.PluginRoot})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range report.Databases {
		if row.Database != "ops" {
			continue
		}
		if row.State != StateOutdated || row.CandidateChunks != nil || row.Candidates != "not counted" {
			t.Fatalf("ops with an uncountable source: %+v, want outdated and candidates explicitly not counted", row)
		}
		return
	}
	t.Fatal("status has no ops row")
}
