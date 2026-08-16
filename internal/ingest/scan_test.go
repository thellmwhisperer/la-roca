package ingest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const cwdFixtureSessionID = "99999999-8888-7777-6666-555555555555"

func TestClaudeCwdAttributionNamesAPunctuationBearingProject(t *testing.T) {
	world := newWorld(t)
	roots := world.roots()
	cwd := filepath.Join(world.home, ".treehouse", "Here comes the sun")
	dir := encodeRoot(cwd)
	world.write(t, filepath.Join(roots.ClaudeProjects, dir, cwdFixtureSessionID+".jsonl"),
		fmt.Sprintf("{\"type\":\"user\",\"timestamp\":\"2026-08-01T10:00:00Z\",\"cwd\":%q,\"message\":{\"content\":\"question\"}}\n"+
			"{\"type\":\"assistant\",\"timestamp\":\"2026-08-01T10:00:01Z\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"answer\"}]}}\n", cwd))
	memory := filepath.Join(roots.ClaudeProjects, dir, "memory", "fact.md")
	world.write(t, memory, "---\nname: fact\ntype: project\n---\nA synthetic attributed fact.\n")

	db := rocaDatabase(t)
	result, err := Run(context.Background(), db, registry(t), Options{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	if result.Sources["claude"].MemoriesInserted == 0 {
		t.Fatalf("Claude counts = %+v", result.Sources["claude"])
	}
	assertMemoryProject(t, db.SQL(), memory, "Here comes the sun")
	var project string
	if err := db.SQL().QueryRow(`SELECT COALESCE(project, '') FROM sessions WHERE session_id = ?`,
		cwdFixtureSessionID).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if project != "Here comes the sun" {
		t.Errorf("session project = %q, want %q", project, "Here comes the sun")
	}
}

func TestClaudeCwdAttributionOutranksTheConfigMapping(t *testing.T) {
	world := newWorld(t)
	roots := world.roots()
	sessionCwd := filepath.Join(world.workspace, "demo", "project")
	configCwd := filepath.Join(world.workspace, "demo-project")
	if encodeRoot(sessionCwd) != encodeRoot(configCwd) {
		t.Fatalf("fixture cwds do not share an encoding: %q vs %q",
			encodeRoot(sessionCwd), encodeRoot(configCwd))
	}
	dir := encodeRoot(sessionCwd)
	world.write(t, filepath.Join(roots.ClaudeProjects, dir, cwdFixtureSessionID+".jsonl"),
		fmt.Sprintf("{\"type\":\"user\",\"timestamp\":\"2026-08-01T10:00:00Z\",\"cwd\":%q,\"message\":{\"content\":\"question\"}}\n"+
			"{\"type\":\"assistant\",\"timestamp\":\"2026-08-01T10:00:01Z\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"answer\"}]}}\n", sessionCwd))
	memory := filepath.Join(roots.ClaudeProjects, dir, "memory", "fact.md")
	world.write(t, memory, "---\nname: fact\ntype: project\n---\nA synthetic attributed fact.\n")
	world.write(t, roots.ClaudeConfig, `{"projects":{"`+configCwd+`":{}}}`)

	db := rocaDatabase(t)
	if _, err := Run(context.Background(), db, registry(t), Options{Roots: roots}); err != nil {
		t.Fatal(err)
	}
	assertMemoryProject(t, db.SQL(), memory, "project")
}

func TestFirstClaudeCwdReadsUntilTheFirstCwd(t *testing.T) {
	content := `{"type":"user","message":{"content":"no cwd"}}` + "\n" +
		`{"type":"user","cwd":"/w/demo","message":{"content":"x"}}` + "\n"
	cwd, ok := firstClaudeCwdFrom(bufio.NewReader(strings.NewReader(content)))
	if !ok || cwd != "/w/demo" {
		t.Fatalf("cwd = %q, ok = %v", cwd, ok)
	}
}

func TestFirstClaudeCwdReportsNoCwd(t *testing.T) {
	cwd, ok := firstClaudeCwdFrom(bufio.NewReader(strings.NewReader(
		`{"type":"user","message":{"content":"x"}}` + "\n")))
	if ok || cwd != "" {
		t.Fatalf("cwd = %q, ok = %v", cwd, ok)
	}
}

// stopAfterFirstRecord serves exactly one record and then fails the read, so a
// discovery pass that keeps scanning past the first usable record is observable.
type stopAfterFirstRecord struct {
	line []byte
	done bool
}

func (r *stopAfterFirstRecord) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("read past the first usable record")
	}
	r.done = true
	return copy(p, r.line), nil
}

func TestFirstClaudeCwdStopsAtTheFirstUsableRecord(t *testing.T) {
	line := `{"type":"user","cwd":"/w/.treehouse/Here comes the sun","message":{"content":"x"}}` + "\n"
	cwd, ok := firstClaudeCwdFrom(bufio.NewReader(&stopAfterFirstRecord{line: []byte(line)}))
	if !ok || cwd != "/w/.treehouse/Here comes the sun" {
		t.Fatalf("cwd = %q, ok = %v", cwd, ok)
	}
}
