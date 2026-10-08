package vector

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// When the outdated source cannot be counted, status must say so instead of
// leaving candidate_chunks as a bare null.
func TestStatusSaysCandidatesNotCountedWhenCountFails(t *testing.T) {
	f, _, ops, _ := federationFixture(t)
	ctx := context.Background()
	if _, err := f.Ingest(ctx, ""); err != nil {
		t.Fatal(err)
	}
	// A view that fails on read makes any candidate count fail.
	mutateSourceDatabase(t, ops, `ALTER TABLE memories RENAME TO originals;
		CREATE VIEW memories AS SELECT * FROM originals WHERE abs(-9223372036854775808)>0`)
	report, err := ReportVectorization(ctx, StatusRequest{PluginRoot: f.PluginRoot})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range report.Databases {
		if row.Database != "ops" {
			continue
		}
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		if row.State != StateOutdated || row.CandidateChunks != nil || !strings.Contains(string(raw), "not counted") {
			t.Fatalf("ops with an uncountable source: %s, want outdated and candidates explicitly not counted", raw)
		}
		return
	}
	t.Fatal("status has no ops row")
}
