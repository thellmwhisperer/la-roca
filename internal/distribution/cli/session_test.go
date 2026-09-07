package cli

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestPillCommandDedupesSlugsAndPrintsFullContent(t *testing.T) {
	home := sessionHome(t)
	april := "April build pill " + strings.Repeat("a", 180)
	june := "June build pill " + strings.Repeat("j", 180)
	insertOpsMemory(t, home, opsMemory{
		layer: "pill", project: "demo", createdAt: "2026-04-01 00:00:00",
		content: april, metadata: map[string]any{"pill_slug": "build"},
	})
	insertOpsMemory(t, home, opsMemory{
		layer: "pill", project: "demo", createdAt: "2026-06-01 00:00:00",
		content: june, metadata: map[string]any{"pill_slug": "build"},
	})
	orphan := insertOpsMemory(t, home, opsMemory{
		layer: "pill", project: "demo", createdAt: "2026-06-02 00:00:00",
		content: "orphan without slug",
	})

	out := runRoot(t, contractBuild(), "pill", "--project", "demo")
	if !strings.Contains(out, june) {
		t.Fatalf("newest pill content missing or truncated:\n%s", out)
	}
	if strings.Contains(out, april) {
		t.Fatalf("April duplicate was loaded:\n%s", out)
	}
	if !strings.Contains(out, "unslugged") || !strings.Contains(out, strconv.FormatInt(orphan, 10)) {
		t.Fatalf("unslugged id %d was not listed:\n%s", orphan, out)
	}
}

func TestPillShowReturnsOneCompletePill(t *testing.T) {
	home := sessionHome(t)
	body := "only this build pill " + strings.Repeat("z", 180)
	insertOpsMemory(t, home, opsMemory{
		layer: "pill", project: "demo", createdAt: "2026-06-01 00:00:00",
		content: body, metadata: map[string]any{"pill_slug": "build"},
	})

	out := runRoot(t, contractBuild(), "pill", "show", "build", "--project", "demo")
	if !strings.Contains(out, body) {
		t.Fatalf("show dropped full content:\n%s", out)
	}
}

func TestPillDeleteRemovesSlugVersionsAndReportsRows(t *testing.T) {
	home := sessionHome(t)
	insertOpsMemory(t, home, opsMemory{
		layer: "pill", project: "demo", createdAt: "2026-06-01 00:00:00",
		content: "temporary pill", metadata: map[string]any{"pill_slug": "tmp-x"},
	})
	if out := runRoot(t, contractBuild(), "pill", "--project", "demo"); !strings.Contains(out, "tmp-x") {
		t.Fatalf("tmp-x was not listed before delete:\n%s", out)
	}
	out := runRoot(t, contractBuild(), "pill", "delete", "tmp-x")
	if out != "deleted: 1" {
		t.Fatalf("delete output = %q, want deleted: 1", out)
	}
	if out := runRoot(t, contractBuild(), "pill", "--project", "demo"); strings.Contains(out, "tmp-x") {
		t.Fatalf("tmp-x was still listed after delete:\n%s", out)
	}
	assertOpsPillSlugCount(t, home, "tmp-x", 0)
}

func TestPillDeleteRefusesUnknownSlugWithKnownSlugs(t *testing.T) {
	home := sessionHome(t)
	insertOpsMemory(t, home, opsMemory{
		layer: "pill", project: "demo", createdAt: "2026-06-01 00:00:00",
		content: "build pill", metadata: map[string]any{"pill_slug": "build"},
	})

	out, err := runRootErr(t, contractBuild(), nil, "pill", "delete", "nope")
	if err == nil || !strings.Contains(err.Error(), `no pill with slug "nope"`) {
		t.Fatalf("delete unknown error = %v", err)
	}
	if !strings.Contains(out, "help[1]:") || !strings.Contains(out, "build") {
		t.Fatalf("known slug help missing:\n%s", out)
	}
	assertOpsPillSlugCount(t, home, "build", 1)
}

func TestHandoffLatestSkipsSupersededAndKeepsUnsuperseded(t *testing.T) {
	home := sessionHome(t)
	first := insertOpsMemory(t, home, opsMemory{
		layer: "handoff", project: "demo", createdAt: "2026-08-01 00:00:00",
		content: "first session close " + strings.Repeat("f", 180),
	})
	insertOpsMemory(t, home, opsMemory{
		layer: "handoff", project: "demo", createdAt: "2026-08-02 00:00:00",
		content: "second session close " + strings.Repeat("s", 180),
	})
	insertOpsMemory(t, home, opsMemory{
		layer: "handoff", project: "demo", createdAt: "2026-08-03 00:00:00",
		content:    "replacement close " + strings.Repeat("r", 180),
		supersedes: first,
	})

	out := runRoot(t, contractBuild(), "handoff", "latest", "--project", "demo")
	if strings.Contains(out, "first session close") {
		t.Fatalf("superseded handoff was loaded:\n%s", out)
	}
	if !strings.Contains(out, "second session close") || !strings.Contains(out, strings.Repeat("s", 180)) {
		t.Fatalf("unsuperseded handoff missing or truncated:\n%s", out)
	}
	if !strings.Contains(out, "replacement close") || !strings.Contains(out, strings.Repeat("r", 180)) {
		t.Fatalf("replacement handoff missing or truncated:\n%s", out)
	}
}

func TestHandoffLatestFallsBackToGlobal(t *testing.T) {
	home := sessionHome(t)
	insertOpsMemory(t, home, opsMemory{
		layer: "handoff", createdAt: "2026-08-01 00:00:00",
		content: "global close",
	})

	out := runRoot(t, contractBuild(), "handoff", "latest", "--project", "demo")
	if !strings.Contains(out, "global close") {
		t.Fatalf("global fallback missing:\n%s", out)
	}
}

func TestSessionContextCommandsRequireEnabledExistingRocaOps(t *testing.T) {
	t.Run("feature disabled", func(t *testing.T) {
		home := sessionHome(t)
		configPath := filepath.Join(home, ".roca", "config.toml")
		body, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		disabled := strings.Replace(string(body), "roca_ops = true", "roca_ops = false", 1)
		if disabled == string(body) {
			t.Fatal("fixture config did not enable roca_ops")
		}
		if err := os.WriteFile(configPath, []byte(disabled), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"pill", "--project", "demo"},
			{"pill", "show", "build", "--project", "demo"},
			{"pill", "delete", "build"},
			{"handoff", "latest", "--project", "demo"},
		} {
			_, err = runRootErr(t, contractBuild(), nil, args...)
			if err == nil || !strings.Contains(err.Error(), "features.roca_ops") {
				t.Fatalf("roca %v did not refuse disabled roca_ops: %v", args, err)
			}
		}
	})

	t.Run("database missing", func(t *testing.T) {
		home := sessionHome(t)
		opsPath := filepath.Join(home, ".roca", "plugins", "roca-ops", "roca-ops.db")
		if err := os.Remove(opsPath); err != nil {
			t.Fatal(err)
		}
		_, err := runRootErr(t, contractBuild(), nil, "handoff", "latest", "--project", "demo")
		if err == nil || !strings.Contains(err.Error(), "existing roca-ops database") {
			t.Fatalf("handoff did not refuse missing roca_ops: %v", err)
		}
		if _, statErr := os.Stat(opsPath); !os.IsNotExist(statErr) {
			t.Fatalf("session context recreated the missing ops database: %v", statErr)
		}
	})
}

func TestClaudeSessionHookRunnersLoadSessionContext(t *testing.T) {
	home := sessionHome(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Base(cwd)
	insertOpsMemory(t, home, opsMemory{
		layer: "pill", project: project, createdAt: "2026-08-01 00:00:00",
		content: "hook pill", metadata: map[string]any{"pill_slug": "hook"},
	})
	insertOpsMemory(t, home, opsMemory{
		layer: "handoff", project: project, createdAt: "2026-08-01 00:00:00",
		content: "hook handoff",
	})

	if out := runRoot(t, contractBuild(), "hooks", "run", "claude-pills"); !strings.Contains(out, "hook pill") {
		t.Fatalf("pills hook did not execute the pill loader:\n%s", out)
	}
	if out := runRoot(t, contractBuild(), "hooks", "run", "claude-handoff"); !strings.Contains(out, "hook handoff") {
		t.Fatalf("handoff hook did not execute the handoff loader:\n%s", out)
	}
}

func sessionHome(t *testing.T) string {
	t.Helper()
	return fixtureInstallation(t).home
}

type opsMemory struct {
	layer, content, project, createdAt string
	supersedes                         int64
	metadata                           map[string]any
}

func insertOpsMemory(t *testing.T, home string, seed opsMemory) int64 {
	t.Helper()
	if seed.metadata == nil {
		seed.metadata = map[string]any{}
	}
	encoded, err := json.Marshal(seed.metadata)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".roca", "plugins", "roca-ops", "roca-ops.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var projectArg any
	if seed.project != "" {
		projectArg = seed.project
	}
	var supersedesArg any
	if seed.supersedes != 0 {
		supersedesArg = seed.supersedes
	}
	result, err := db.Exec(
		`INSERT INTO memories (layer, content, metadata, origin, project, status, supersedes, created_at)
		 VALUES (?, ?, ?, 'agent', ?, 'active', ?, ?)`,
		seed.layer, seed.content, string(encoded), projectArg, supersedesArg, seed.createdAt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func assertOpsPillSlugCount(t *testing.T, home, slug string, want int) {
	t.Helper()
	path := filepath.Join(home, ".roca", "plugins", "roca-ops", "roca-ops.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(
		`SELECT count(*) FROM memories WHERE json_extract(metadata,'$.pill_slug') = ?`, slug).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("pill slug %q count = %d, want %d", slug, count, want)
	}
}
