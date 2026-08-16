package ingest

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

func TestClaudeMemoryManifestIsCoverageEvidenceNotCorpus(t *testing.T) {
	world := newWorld(t)
	roots := world.roots()
	manifest := filepath.Join(roots.ClaudeProjects, world.projectDir(), "memory", "MEMORY.md")
	world.write(t, manifest, "- [present](note.md)\n- [missing](missing.md)\n")

	db := rocaDatabase(t)
	result, err := Run(context.Background(), db, registry(t), Options{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned["claude_memory_files"] != 2 || result.Coverage.Files.Seen == 0 {
		t.Fatalf("manifest was not counted as seen: scanned=%+v coverage=%+v",
			result.Scanned, result.Coverage)
	}
	assertCoverageReason(t, result.Coverage.Files.Skips,
		"Claude memory completeness manifest is not corpus content", 1)
	assertCoverageReason(t, result.Coverage.Gaps,
		"Claude memory manifest link is absent from disk", 1)
	if got := countRows(t, db.SQL(), `memories WHERE content LIKE '%[present](note.md)%'`); got != 0 {
		t.Fatalf("manifest rows = %d, want none", got)
	}
}

func TestManifestReportsAContentFileMarkedSeenWithoutALandedMemory(t *testing.T) {
	world := newWorld(t)
	roots := world.roots()
	memory := filepath.Join(roots.ClaudeProjects, world.projectDir(), "memory", "note.md")
	fingerprint, err := targetFingerprint(Target{Path: memory, Kind: parsers.KindClaudeMemory})
	if err != nil {
		t.Fatal(err)
	}
	db := rocaDatabase(t)
	exec(t, db.SQL(), `INSERT INTO ingest_file_state
		(path, source_kind, source_agent, fingerprint, last_error)
		VALUES (?, 'claude_memory', 'claude', ?, '')`, memory, fingerprint)

	result, err := Run(context.Background(), db, registry(t), Options{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	assertCoverageReason(t, result.Coverage.Gaps,
		"Claude memory manifest link is absent from corpus", 1)
}

func TestDryRunReportsAbsentDiskManifestLinksWithoutCorpusTables(t *testing.T) {
	world := newWorld(t)
	roots := world.roots()
	manifest := filepath.Join(roots.ClaudeProjects, world.projectDir(), "memory", "MEMORY.md")
	world.write(t, manifest, "- [missing](missing.md)\n")

	result, err := Run(context.Background(), emptyDatabase(t), registry(t),
		Options{Roots: roots, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	assertCoverageReason(t, result.Coverage.Gaps,
		"Claude memory manifest link is absent from disk", 1)
}

func TestClaudeConfigAttributesExistingMemoriesOnANormalIngest(t *testing.T) {
	world := newWorld(t)
	roots := world.roots()
	cwd := filepath.Join(world.home, ".treehouse", "Here comes the sun")
	dir := encodeRoot(cwd)
	memory := filepath.Join(roots.ClaudeProjects, dir, "memory", "fact.md")
	world.write(t, memory, "A synthetic attributed fact.\n")
	world.write(t, roots.ClaudeConfig, `{"projects":{"`+cwd+`":{}}}`)

	db := rocaDatabase(t)
	result, err := Run(context.Background(), db, registry(t), Options{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	if result.Sources["claude"].MemoriesInserted == 0 {
		t.Fatalf("Claude counts = %+v", result.Sources["claude"])
	}
	assertMemoryProject(t, db.SQL(), memory, "Here comes the sun")
}

func TestAReReadBackfillsAClaudeMemoryProjectWithoutChangingItsContent(t *testing.T) {
	world, db, ctx, options := seededWorld(t)
	memory := filepath.Join(options.Roots.ClaudeProjects, world.projectDir(), "memory", "note.md")
	exec(t, db.SQL(), `UPDATE memories SET project = NULL
		WHERE json_extract(metadata, '$.file_path') = ?`, memory)
	exec(t, db.SQL(), `UPDATE ingest_file_state
		SET fingerprint = replace(fingerprint, ':parser:claude-memory-v2', '') WHERE path = ?`, memory)

	result, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sources["claude"].MemoriesUpdated != 1 {
		t.Fatalf("Claude counts = %+v", result.Sources["claude"])
	}
	assertMemoryProject(t, db.SQL(), memory, "demo")
}

func TestAReReadCorrectsAStaleClaudeMemoryProjectFromSessionCwd(t *testing.T) {
	world, db, ctx, options := seededWorld(t)
	memory := filepath.Join(options.Roots.ClaudeProjects, world.projectDir(), "memory", "note.md")
	exec(t, db.SQL(), `UPDATE memories SET project = 'stale'
		WHERE json_extract(metadata, '$.file_path') = ?`, memory)
	exec(t, db.SQL(), `UPDATE ingest_file_state
		SET fingerprint = replace(fingerprint, ':parser:claude-memory-v2', '') WHERE path = ?`, memory)

	result, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sources["claude"].MemoriesUpdated != 1 {
		t.Fatalf("Claude counts = %+v", result.Sources["claude"])
	}
	assertMemoryProject(t, db.SQL(), memory, "demo")
}

func TestFallbackAttributionDoesNotClobberAnExistingClaudeMemoryProject(t *testing.T) {
	world := newWorld(t)
	roots := world.roots()
	cwd := filepath.Join(world.home, ".treehouse", "Here comes the sun")
	dir := encodeRoot(cwd)
	memory := filepath.Join(roots.ClaudeProjects, dir, "memory", "fact.md")
	world.write(t, memory, "A synthetic attributed fact.\n")
	world.write(t, roots.ClaudeConfig, `{"projects":{"`+cwd+`":{}}}`)

	db := rocaDatabase(t)
	if _, err := Run(context.Background(), db, registry(t), Options{Roots: roots}); err != nil {
		t.Fatal(err)
	}
	assertMemoryProject(t, db.SQL(), memory, "Here comes the sun")

	exec(t, db.SQL(), `UPDATE memories SET project = 'stale'
		WHERE json_extract(metadata, '$.file_path') = ?`, memory)
	exec(t, db.SQL(), `UPDATE ingest_file_state
		SET fingerprint = replace(fingerprint, ':parser:claude-memory-v2', '') WHERE path = ?`, memory)

	if _, err := Run(context.Background(), db, registry(t), Options{Roots: roots}); err != nil {
		t.Fatal(err)
	}
	assertMemoryProject(t, db.SQL(), memory, "stale")
}

func assertMemoryProject(t *testing.T, db *sql.DB, path, want string) {
	t.Helper()
	var project string
	if err := db.QueryRow(`SELECT COALESCE(project, '') FROM memories
		WHERE json_extract(metadata, '$.file_path') = ?`, path).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if project != want {
		t.Errorf("project = %q, want %q", project, want)
	}
}

func TestCoverageAccountsForEverySeenFileAndGrokMemtraceRecord(t *testing.T) {
	world := newWorld(t)
	roots := world.roots()
	trace := filepath.Join(roots.GrokMemtrace, "trace.jsonl")
	world.write(t, trace, "{\"kind\":\"start\"}\n{\"kind\":\"sample\"}\n{\"kind\":\"purge\"}\n")

	result, err := Run(context.Background(), rocaDatabase(t), registry(t), Options{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	files := result.Coverage.Files
	if files.Seen != files.Ingested+files.Skipped || files.Claimed != len(Scan(roots).Targets) {
		t.Fatalf("file coverage is not closed: %+v", files)
	}
	assertCoverageReason(t, files.Skips,
		"Grok process memory telemetry is not conversation content", 1)
	assertCoverageReason(t, result.Coverage.Records.Excluded,
		"Grok process memory telemetry is not conversation content", 3)
	if got := result.Coverage.OpenCode.Store["sessions"]; got != 1 {
		t.Errorf("OpenCode store sessions = %d, want 1", got)
	}
	if got := result.Coverage.OpenCode.Extracted["sessions"]; got != 1 {
		t.Errorf("OpenCode extracted sessions = %d, want 1", got)
	}
}

func TestMemtraceRecordCountDistinguishesEmptyAndLargeFiles(t *testing.T) {
	for _, test := range []struct {
		name, content string
		want          int
	}{
		{name: "empty", content: "", want: 0},
		{name: "large line", content: `{"kind":"sample","payload":"` + strings.Repeat("x", 70_000) + `"}`,
			want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			world := newWorld(t)
			roots := world.roots()
			world.write(t, filepath.Join(roots.GrokMemtrace, "trace.jsonl"), test.content)
			result, err := Run(context.Background(), rocaDatabase(t), registry(t), Options{Roots: roots})
			if err != nil {
				t.Fatal(err)
			}
			if got := coverageReasonCount(result.Coverage.Records.Excluded,
				"Grok process memory telemetry is not conversation content"); got != test.want {
				t.Errorf("record coverage = %d, want %d", got, test.want)
			}
			if got := discardReasonCount(result.DiscardSummary,
				"Grok process memory telemetry is not conversation content"); got != test.want {
				t.Errorf("excluded records = %d, want %d", got, test.want)
			}
		})
	}
}

func discardReasonCount(categories []DiscardCategory, reason string) int {
	for _, category := range categories {
		if category.Reason == reason {
			return category.Count
		}
	}
	return 0
}

func assertCoverageReason(t *testing.T, categories []CoverageCategory, reason string, want int) {
	t.Helper()
	if got := coverageReasonCount(categories, reason); got != want {
		t.Errorf("coverage %q = %d, want %d in %+v", reason, got, want, categories)
	}
}

func coverageReasonCount(categories []CoverageCategory, reason string) int {
	for _, category := range categories {
		if category.Reason == reason {
			return category.Count
		}
	}
	return 0
}
