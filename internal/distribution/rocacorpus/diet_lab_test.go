package rocacorpus_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	_ "modernc.org/sqlite"
)

// TestCompactPreservesCurrentRowsOnALabCopy runs against a copy of the live
// corpus database. It never writes the live file. Skip when the copy source is
// absent so the fast lane stays hermetic.
func TestCompactPreservesCurrentRowsOnALabCopy(t *testing.T) {
	if os.Getenv("ROCA_STORAGE_LAW_LAB") != "1" {
		t.Skip("set ROCA_STORAGE_LAW_LAB=1 to compact a lab copy of the live corpus")
	}
	live := liveCorpusPath()
	if _, err := os.Stat(live); err != nil {
		t.Skip("no live corpus database to copy")
	}
	copyPath := filepath.Join(t.TempDir(), "storage-law-lab.db")
	if err := copyCorpusReadOnly(t, live, copyPath); err != nil {
		t.Fatal(err)
	}

	before, err := countLabRows(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.exchanges < 1000 {
		t.Fatalf("lab copy exchanges = %d: this does not look like a real corpus", before.exchanges)
	}
	fts, execHits, err := labQuerySnapshot(copyPath)
	if err != nil {
		t.Fatal(err)
	}

	report, err := rocacorpus.Compact(context.Background(), copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.Sessions != before.sessions || report.Exchanges != before.exchanges ||
		report.ThinkingBlocks != before.thinking || report.ToolUses != before.tools {
		t.Fatalf("current rows drifted: compact=%+v before=%+v", report, before)
	}
	afterFTS, afterExec, err := labQuerySnapshot(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if fts != afterFTS || execHits != afterExec {
		t.Fatalf("query snapshot drifted: fts %d→%d exec %d→%d", fts, afterFTS, execHits, afterExec)
	}
	t.Logf("current rows sessions=%d exchanges=%d thinking=%d tools=%d",
		report.Sessions, report.Exchanges, report.ThinkingBlocks, report.ToolUses)
	t.Logf("archive bookkeeping custody=%d source_rows=%d",
		report.CustodyMemberships, report.CorpusSourceRows)
	t.Logf("size %.1f MB -> %.1f MB (reclaimed %.1f MB)",
		float64(report.BytesBefore)/(1024*1024), float64(report.BytesAfter)/(1024*1024),
		float64(report.ReclaimedBytes)/(1024*1024))
	if report.BytesAfter >= report.BytesBefore {
		t.Errorf("compact did not shrink the database: before=%d after=%d",
			report.BytesBefore, report.BytesAfter)
	}
	if report.CustodyMemberships != 0 || report.CorpusSourceRows != 0 {
		t.Errorf("archive bookkeeping remains: custody=%d source_rows=%d",
			report.CustodyMemberships, report.CorpusSourceRows)
	}
	if report.BytesBefore > 4*1024*1024*1024 && report.BytesAfter > 2*1024*1024*1024 {
		t.Errorf("compact left %.1f MB; want current content plus one FTS plus hash indexes (under 2 GB)",
			float64(report.BytesAfter)/(1024*1024))
	}
}

func liveCorpusPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".roca", "plugins", rocacorpus.Name, rocacorpus.DatabaseFilename)
}

func copyCorpusReadOnly(t *testing.T, source, dest string) error {
	t.Helper()
	db, err := bundledplugin.OpenDatabase(source, true)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(context.Background(), "VACUUM INTO ?", dest)
	return err
}

type labCounts struct {
	sessions, exchanges, thinking, tools int64
}

func countLabRows(path string) (labCounts, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return labCounts{}, err
	}
	defer db.Close()
	count := func(table string) (int64, error) {
		var n int64
		err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
		return n, err
	}
	var counts labCounts
	var errCount error
	if counts.sessions, errCount = count("sessions"); errCount != nil {
		return labCounts{}, errCount
	}
	if counts.exchanges, errCount = count("exchanges"); errCount != nil {
		return labCounts{}, errCount
	}
	if counts.thinking, errCount = count("thinking_blocks"); errCount != nil {
		return labCounts{}, errCount
	}
	if counts.tools, errCount = count("tool_uses"); errCount != nil {
		return labCounts{}, errCount
	}
	return counts, nil
}

func labQuerySnapshot(path string) (fts, execHits int, err error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, 0, err
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT COUNT(*) FROM exchanges_fts WHERE exchanges_fts MATCH 'the'`).
		Scan(&fts); err != nil {
		return 0, 0, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM exchanges WHERE exchange_number = 1`).
		Scan(&execHits); err != nil {
		return 0, 0, err
	}
	return fts, execHits, nil
}
