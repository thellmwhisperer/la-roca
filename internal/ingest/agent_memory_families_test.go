package ingest

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestZCodeAndGrokMemoryFamiliesStayIncremental(t *testing.T) {
	home := t.TempDir()
	w := &world{home: home}
	roots := ResolveRoots(Environment{GOOS: "linux", Home: home}, Settings{})
	zcodeHarbor := filepath.Join(roots.ZCodeMemories, "projects", "cobalt-harbor", "memory", "chart.md")
	zcodeDefault := filepath.Join(roots.ZCodeMemories, "projects", "synthetic-default", "memory", "note.md")
	grokGlobal := filepath.Join(roots.GrokMemoryV2, "global", "MEMORY.md")
	grokWorkspace := filepath.Join(roots.GrokMemoryV2, "workspaces", "cobalt-harbor", "MEMORY.md")
	w.write(t, zcodeHarbor, ""+
		"---\nname: synthetic-harbor\ndescription: how the synthetic harbor is kept\nmetadata:\n  type: feedback\n---\n"+
		"Keep the synthetic harbor chart in cobalt ink.\n")
	w.write(t, zcodeDefault, "---\nname: synthetic-default\ndescription: the other folder\n---\nA second synthetic ZCode fact.\n")
	w.write(t, grokGlobal, "Global synthetic tide table.\n")
	w.write(t, grokWorkspace, "Workspace synthetic tide table.\n")
	w.write(t, filepath.Join(roots.GrokMemoryV2, "memory_state.sqlite"), "not corpus")
	w.write(t, filepath.Join(roots.GrokMemoryV2, "workspaces", "cobalt-harbor", "memory_state.sqlite-wal"), "not corpus")
	w.write(t, filepath.Join(roots.GrokMemoryV2, "workspaces", "cobalt-harbor", "notes.txt"), "not a memory file")

	ctx := context.Background()
	db := rocaDatabase(t)
	opts := Options{Roots: roots, DryRun: true}
	dry, err := Run(ctx, db, registry(t), opts)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if filesSeen(dry, "zcode") <= 1 {
		t.Fatalf("zcode files seen = %d, want more than 1: %+v", filesSeen(dry, "zcode"), dry.SourceStats["zcode"])
	}
	if dry.Sources["grok"] == nil || dry.Sources["grok"].MemoriesInserted == 0 {
		t.Fatalf("grok dry-run memories = %+v, want > 0", dry.Sources["grok"])
	}
	if dry.Delta != (Tables{}) {
		t.Fatalf("dry-run delta = %+v, want zero", dry.Delta)
	}
	if got := coverageReasonCount(dry.Coverage.Records.Excluded, grokMemoryStateExcluded); got != 2 {
		t.Fatalf("memory_state exclusions = %d, want 2: %+v", got, dry.Coverage.Records.Excluded)
	}

	opts.DryRun = false
	first, err := Run(ctx, db, registry(t), opts)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if first.Errors != 0 {
		t.Fatalf("errors = %d: %+v", first.Errors, first.ErrorDetails)
	}
	if countRows(t, db.SQL(), `memories WHERE source_agent = 'zcode'`) != 2 ||
		countRows(t, db.SQL(), `memories WHERE source_agent = 'grok'`) != 2 {
		t.Fatalf("census zcode=%d grok=%d", countRows(t, db.SQL(), `memories WHERE source_agent = 'zcode'`),
			countRows(t, db.SQL(), `memories WHERE source_agent = 'grok'`))
	}
	projects := queryColumn(t, db.SQL(),
		`SELECT project FROM memories WHERE source_agent = 'zcode' ORDER BY project`)
	if strings.Join(projects, " ") != "cobalt-harbor synthetic-default" {
		t.Fatalf("zcode projects = %v", projects)
	}
	var name, description, surface string
	if err := db.SQL().QueryRow(`SELECT json_extract(metadata, '$.memory_name'),
		json_extract(metadata, '$.memory_description'), source_surface
		FROM memories WHERE source_agent = 'zcode' AND project = 'cobalt-harbor'`).
		Scan(&name, &description, &surface); err != nil {
		t.Fatal(err)
	}
	if name != "synthetic-harbor" || description != "how the synthetic harbor is kept" || surface != "ZCode" {
		t.Fatalf("zcode memory = %q / %q / %q", name, description, surface)
	}
	if got := countRows(t, db.SQL(), `memories WHERE source_agent = 'grok' AND project = 'global' AND source_surface = 'Grok Build'`); got != 1 {
		t.Fatalf("global grok memories = %d, want 1", got)
	}
	if got := countRows(t, db.SQL(), `memories WHERE content LIKE '%not corpus%' OR content LIKE '%not a memory file%'`); got != 0 {
		t.Fatalf("excluded grok files became memories: %d", got)
	}

	var harborID int64
	if err := db.SQL().QueryRow(`SELECT id FROM memories WHERE json_extract(metadata, '$.file_path') = ?`,
		zcodeHarbor).Scan(&harborID); err != nil {
		t.Fatal(err)
	}
	second, err := Run(ctx, db, registry(t), opts)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if second.Errors != 0 || second.Delta.Memories != 0 ||
		second.Sources["zcode"].MemoriesInserted+second.Sources["zcode"].MemoriesUpdated != 0 ||
		second.Sources["grok"].MemoriesInserted+second.Sources["grok"].MemoriesUpdated != 0 {
		t.Fatalf("unchanged files produced changes: errors=%d delta=%+v zcode=%+v grok=%+v",
			second.Errors, second.Delta, second.Sources["zcode"], second.Sources["grok"])
	}
	w.write(t, zcodeHarbor, ""+
		"---\nname: synthetic-harbor\ndescription: how the synthetic harbor is kept\nmetadata:\n  type: feedback\n---\n"+
		"The synthetic harbor chart was revised.\n")
	w.write(t, grokWorkspace, "Workspace synthetic tide table, revised.\n")
	third, err := Run(ctx, db, registry(t), opts)
	if err != nil {
		t.Fatalf("changed ingest: %v", err)
	}
	if third.Sources["zcode"].MemoriesUpdated != 1 || third.Sources["grok"].MemoriesUpdated != 1 {
		t.Fatalf("changed memories were not superseded in place: zcode=%+v grok=%+v",
			third.Sources["zcode"], third.Sources["grok"])
	}
	if countRows(t, db.SQL(), `memories WHERE source_agent = 'zcode'`) != 2 ||
		countRows(t, db.SQL(), `memories WHERE source_agent = 'grok'`) != 2 {
		t.Fatal("a changed memory inserted a second row")
	}
	var revisedID int64
	var revised string
	if err := db.SQL().QueryRow(`SELECT id, content FROM memories WHERE json_extract(metadata, '$.file_path') = ?`,
		zcodeHarbor).Scan(&revisedID, &revised); err != nil {
		t.Fatal(err)
	}
	if revisedID != harborID || revised != "The synthetic harbor chart was revised." {
		t.Fatalf("supersede = id %d->%d content %q", harborID, revisedID, revised)
	}
}

func TestMissingAgentMemoryDirectoriesAreNamed(t *testing.T) {
	home := t.TempDir()
	roots := ResolveRoots(Environment{GOOS: "linux", Home: home}, Settings{})
	(&world{home: home}).write(t, roots.ZCodeDB, "route marker")
	if err := os.MkdirAll(roots.GrokSessions, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), rocaDatabase(t), registry(t), Options{Roots: roots, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	for _, reason := range []string{zcodeMemoryDirectoryAbsent, grokMemoryDirectoryAbsent} {
		if coverageReasonCount(result.Coverage.Files.Skips, reason) != 1 {
			t.Errorf("missing %q in %+v", reason, result.Coverage.Files.Skips)
		}
	}
	if !slices.Contains(result.DetectedAgents, "zcode") || !slices.Contains(result.DetectedAgents, "grok") {
		t.Fatalf("detected = %v", result.DetectedAgents)
	}
}

func TestMemoryDirectoriesDetectTheirAgentsWithoutTheSessionStore(t *testing.T) {
	home := t.TempDir()
	roots := ResolveRoots(Environment{GOOS: "linux", Home: home}, Settings{})
	w := &world{home: home}
	w.write(t, filepath.Join(roots.ZCodeMemories, "projects", "cobalt-harbor", "memory", "chart.md"),
		"A synthetic ZCode fact.\n")
	w.write(t, filepath.Join(roots.GrokMemoryV2, "global", "MEMORY.md"), "Global synthetic note.\n")
	plan := Scan(roots)
	if !slices.Contains(plan.DetectedAgents, "zcode") || !slices.Contains(plan.DetectedAgents, "grok") {
		t.Fatalf("detected = %v", plan.DetectedAgents)
	}
	if plan.Scanned["zcode_memory_files"] != 1 || plan.Scanned["grok_memory_files"] != 1 {
		t.Fatalf("scanned = %+v", plan.Scanned)
	}
}

func filesSeen(result Result, agent string) int {
	stats := result.SourceStats[agent]
	if stats == nil {
		return 0
	}
	return stats.Processed + stats.FilesExcluded
}
