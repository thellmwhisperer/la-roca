package service_test

import (
	"context"
	"os"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// TestSearchOverARealLabDatabase runs the search layer against a copy of a live
// reference database: real corpus and vocabulary this repo does not know.
//
// It is the evidence that the FTS/LIKE search reaches real terms the binary was
// never taught. It does not travel in the repo because of its size, so it is
// skipped when it is not there and the lane stays hermetic:
//
//	ROCA_BASE_REAL=$PWD/.tmp/real-corpus.db go test ./internal/service -run Real -v
func TestSearchOverARealLabDatabase(t *testing.T) {
	path := os.Getenv("ROCA_BASE_REAL")
	if path == "" {
		t.Skip("no ROCA_BASE_REAL: there is no copy of a real database at hand")
	}
	if !filepathIsAbsolute(path) {
		t.Fatalf("ROCA_BASE_REAL must be an absolute path, got %q", path)
	}

	dir := t.TempDir()
	svc, err := service.Open(service.Options{
		DBPath:  path,
		DataDir: dir,
		Version: "0.0.0-test", Commit: "0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer svc.Close()

	// A project the real corpus knows, read out of the data and not from a
	// constant in the code.
	project := aProjectFromTheData(t, svc)
	for _, question := range []string{
		"handoff",
		project,
		"deployment",
	} {
		res, err := svc.Search(context.Background(), service.SearchRequest{Question: question})
		if err != nil {
			t.Errorf("Search(%q): %v", question, err)
			continue
		}
		method := ""
		if res.Search != nil {
			method = res.Search.Method
		}
		t.Logf("%-30q -> method=%s rows=%d %d ms", question, method, res.RowCount, res.LatencyMS)
	}
}

func aProjectFromTheData(t *testing.T, svc *service.Service) string {
	t.Helper()
	var project string
	err := svc.DB().SQL().QueryRow(
		`SELECT project FROM sessions WHERE project IS NOT NULL AND project <> ''
		 GROUP BY project ORDER BY COUNT(*) DESC LIMIT 1`).Scan(&project)
	if err != nil {
		t.Fatalf("look for a project in the data: %v", err)
	}
	return project
}

func filepathIsAbsolute(path string) bool {
	return len(path) > 0 && (path[0] == '/' || (len(path) >= 3 && path[1] == ':' && (path[2] == '/' || path[2] == '\\')))
}
