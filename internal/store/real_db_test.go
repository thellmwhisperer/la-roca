package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

// TestAdoptingARealLabDatabase runs against a copy of a live reference
// database. It does not travel in the repo because of its size, so the test is
// skipped when it is not there: the fast lane stays hermetic and the evidence
// against real data is reproduced with
//
//	ROCA_BASE_REAL=.tmp/real-corpus.db go test ./internal/store/ -run Real -v
func TestAdoptingARealLabDatabase(t *testing.T) {
	path := os.Getenv("ROCA_BASE_REAL")
	if path == "" {
		t.Skip("no ROCA_BASE_REAL: there is no copy of a real database at hand")
	}

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	report, err := store.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	t.Logf("verdict=%s orphans=%v differences=%v",
		report.Verdict, report.Orphans, report.Differences)

	if report.Verdict != store.VerdictCurrent && report.Verdict != store.VerdictMigratable {
		t.Fatalf("verdict = %q (%s): a live lab database is adopted",
			report.Verdict, report.Reason)
	}
	if len(report.Orphans) == 0 {
		t.Error("orphans = none: a lab database carries at least messages")
	}

	var exchanges int
	if err := db.SQL().QueryRow("SELECT COUNT(*) FROM exchanges").Scan(&exchanges); err != nil {
		t.Fatalf("COUNT exchanges: %v", err)
	}
	t.Logf("exchanges in the real database: %d", exchanges)
	if exchanges < 1000 {
		t.Errorf("exchanges = %d: this does not look like a real database", exchanges)
	}
}
