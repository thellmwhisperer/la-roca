package service_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestIngestNarratesEachSourceAndItsDelta(t *testing.T) {
	var progress []string
	home := t.TempDir()
	paths := freshPaths(t)
	svc := serviceOn(t, paths, func(options *service.Options) {
		options.Progress = func(line string) { progress = append(progress, line) }
		options.Sources = ingest.ResolveRoots(
			ingest.Environment{GOOS: "darwin", Home: home},
			ingest.Settings{WorkspaceRoots: []string{filepath.Join(home, "w")}})
	})
	if _, err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	seedATranscript(t, home)
	progress = nil

	if _, err := svc.Ingest(t.Context(), service.IngestRequest{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ingest: reading claude-code",
		"ingest: claude-code complete ·",
		"sessions=", "exchanges=", "memories=",
	} {
		if !containsProgress(progress, want) {
			t.Errorf("progress does not carry %q:\n%s", want, strings.Join(progress, "\n"))
		}
	}
}

func TestInitReportsRowsAfterItsBootstrapIngest(t *testing.T) {
	home := t.TempDir()
	seedATranscript(t, home)
	paths := freshPaths(t)
	svc := serviceOn(t, paths, func(options *service.Options) {
		options.Sources = ingest.ResolveRoots(
			ingest.Environment{GOOS: "darwin", Home: home},
			ingest.Settings{WorkspaceRoots: []string{filepath.Join(home, "w")}})
	})

	result, err := svc.Init(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows.Memories == 0 || result.Rows.Exchanges == 0 {
		t.Fatalf("final rows are stale after bootstrap ingest: %+v", result.Rows)
	}
	if result.Rows != result.Ingest.After {
		t.Fatalf("init rows = %+v, ingest left database at %+v", result.Rows, result.Ingest.After)
	}
}

// containsProgress says whether any progress line carries the wanted text.
func containsProgress(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}
