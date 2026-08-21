package ingest

import (
	"context"
	"path/filepath"
	"testing"
)

func TestQwenCodeAndGLMIngestIsReportedAndIdempotent(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".qwen", "projects", "-synthetic-orbit", "chats", "fixture-qwen.jsonl"),
		filepath.Join("..", "..", "pkg", "parsers", "testdata", "conformance", "qwen-code-session", "session.data"))
	writeFixture(t, filepath.Join(home, ".glm", "skills", "synthetic-compass", "SKILL.md"),
		filepath.Join("..", "..", "pkg", "parsers", "testdata", "conformance", "glm-skill", "skill.data"))

	db := rocaDatabase(t)
	options := Options{Roots: ResolveRoots(Environment{GOOS: "darwin", Home: home}, Settings{})}
	first, err := Run(context.Background(), db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	for key := range map[string]bool{"qwen_code_files": true, "glm_skill_files": true} {
		if first.Scanned[key] != 1 {
			t.Errorf("first scanned[%s] = %d, want 1", key, first.Scanned[key])
		}
	}
	if got := countRows(t, db.SQL(), "sessions WHERE source_agent = 'qwen-code'"); got != 1 {
		t.Errorf("Qwen sessions = %d, want 1", got)
	}
	if got := countRows(t, db.SQL(), "memories WHERE source_agent = 'glm'"); got != 1 {
		t.Errorf("GLM memories = %d, want 1", got)
	}
	if models := queryColumn(t, db.SQL(), `SELECT model FROM exchanges WHERE session_id = 'fixture-qwen'`); len(models) != 1 || models[0] != "synthetic-lab/Quartz-7B" {
		t.Errorf("Qwen models = %v", models)
	}

	second, err := Run(context.Background(), db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if second.FilesRead != 0 || second.Delta != (Tables{}) {
		t.Errorf("second run = files_read %d, delta %+v", second.FilesRead, second.Delta)
	}
	for key := range map[string]bool{"qwen_code_files": true, "glm_skill_files": true} {
		if second.Scanned[key] != 1 {
			t.Errorf("second scanned[%s] = %d, want 1", key, second.Scanned[key])
		}
	}
}
