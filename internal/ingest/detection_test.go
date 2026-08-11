package ingest

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDetectedAgentsFollowExistingStores(t *testing.T) {
	home := t.TempDir()
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: home}, Settings{})
	if detected := DetectAgents(roots); detected == nil || len(detected) != 0 {
		t.Fatalf("empty machine detected agents = %#v, want []", detected)
	}

	for _, path := range []string{
		roots.ClaudeProjects,
		roots.ClaudeDesktopSessions,
		roots.CoworkSessions,
		roots.CodexRoot,
		roots.PiSessions,
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{roots.OpenCodeDB, roots.HermesDB} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	plan := Scan(roots)
	want := []string{"claude", "claude-desktop", "cowork", "codex", "opencode", "pi", "hermes"}
	if !slices.Equal(plan.DetectedAgents, want) {
		t.Fatalf("detected agents = %v, want %v", plan.DetectedAgents, want)
	}

	if err := os.RemoveAll(roots.CoworkSessions); err != nil {
		t.Fatal(err)
	}
	plan = Scan(roots)
	if slices.Contains(plan.DetectedAgents, "cowork") {
		t.Fatalf("absent Cowork was detected: %v", plan.DetectedAgents)
	}
}

func TestWorkspaceRootsResolveSessionIdentityWithoutBecomingContent(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "work")
	project := filepath.Join(workspace, "demo")
	settings := Settings{WorkspaceRoots: []string{workspace}}
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: home}, settings)
	encoded := encodeRoot(project)
	transcript := filepath.Join(roots.ClaudeProjects, encoded,
		"99999999-8888-7777-6666-555555555555.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("never ingest this"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan := Scan(roots)
	if _, found := plan.Scanned["config_files"]; found {
		t.Fatalf("repository configuration is still a source: %v", plan.Scanned)
	}
	for _, target := range plan.Targets {
		if target.Path == transcript && target.Project != "demo" {
			t.Fatalf("session project = %q, want demo", target.Project)
		}
		if target.Path == filepath.Join(project, "AGENTS.md") {
			t.Fatal("the repository AGENTS.md became ingest content")
		}
	}
}
