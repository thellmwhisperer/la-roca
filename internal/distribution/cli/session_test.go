package cli

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
	for _, mode := range []struct {
		name  string
		flags []string
	}{{name: "plain"}, {name: "json", flags: []string{"--json"}}} {
		t.Run(mode.name, func(t *testing.T) {
			home := sessionHome(t)
			insertOpsMemory(t, home, opsMemory{
				layer: "pill", project: "demo", createdAt: "2026-06-01 00:00:00",
				content: "temporary pill", metadata: map[string]any{"pill_slug": "tmp-x"},
			})
			if out := runRoot(t, contractBuild(), "pill", "--project", "demo"); !strings.Contains(out, "tmp-x") {
				t.Fatalf("tmp-x was not listed before delete:\n%s", out)
			}
			out, err := runRootErr(t, contractBuild(), nil, append([]string{"pill", "delete", "nope"}, mode.flags...)...)
			if err == nil || !strings.Contains(err.Error(), `no pill with slug "nope"`) {
				t.Fatalf("delete unknown error = %v", err)
			}
			if !strings.Contains(out, "help[1]:") || !strings.Contains(out, "tmp-x") {
				t.Fatalf("known slug help missing:\n%s", out)
			}
			assertOpsPillSlugCount(t, home, "tmp-x", 1)

			out = runRoot(t, contractBuild(), append([]string{"pill", "delete", "tmp-x"}, mode.flags...)...)
			if out != "deleted: 1" {
				t.Fatalf("delete output = %q, want deleted: 1", out)
			}
			if out := runRoot(t, contractBuild(), "pill", "--project", "demo"); strings.Contains(out, "tmp-x") {
				t.Fatalf("tmp-x was still listed after delete:\n%s", out)
			}
			assertOpsPillSlugCount(t, home, "tmp-x", 0)
		})
	}
}

func TestPillDeleteRejectsProjectScopeWithoutDeleting(t *testing.T) {
	for _, args := range [][]string{
		{"pill", "delete", "build", "--project", "alpha"},
		{"pill", "--project", "alpha", "delete", "build"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := sessionHome(t)
			for _, project := range []string{"alpha", "beta"} {
				insertOpsMemory(t, home, opsMemory{
					layer: "pill", project: project, createdAt: "2026-06-01 00:00:00",
					content: "Build", metadata: map[string]any{"pill_slug": "build"},
				})
			}
			_, err := runRootErr(t, contractBuild(), nil, args...)
			if err == nil || !strings.Contains(err.Error(), "unknown flag: --project") {
				t.Fatalf("delete with project error = %v", err)
			}
			assertOpsPillSlugCount(t, home, "build", 2)
		})
	}
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

func TestHandoffLatestLimitKeepsBareCommandCompatible(t *testing.T) {
	home := sessionHome(t)
	insertOpsMemory(t, home, opsMemory{
		layer: "handoff", project: "demo", createdAt: "2026-08-01 00:00:00",
		content: "older session close",
	})
	insertOpsMemory(t, home, opsMemory{
		layer: "handoff", project: "demo", createdAt: "2026-08-02 00:00:00",
		content: "newer session close",
	})

	out := runRoot(t, contractBuild(), "handoff", "latest", "--project", "demo")
	if !strings.Contains(out, "older session close") || !strings.Contains(out, "newer session close") {
		t.Fatalf("bare command no longer returns all active handoffs:\n%s", out)
	}
	limited := runRoot(t, contractBuild(), "handoff", "latest", "--project", "demo", "--limit", "1")
	if strings.Contains(limited, "older session close") || !strings.Contains(limited, "newer session close") {
		t.Fatalf("--limit 1 did not keep only the newest handoff:\n%s", limited)
	}
}

func TestHandoffLatestAllProjectsPrintsOneCappedHeadPerProject(t *testing.T) {
	home := sessionHome(t)
	oldAlpha := insertOpsMemory(t, home, opsMemory{
		layer: "handoff", project: "alpha", createdAt: "2026-08-01 00:00:00",
		content: "obsolete alpha",
	})
	insertOpsMemory(t, home, opsMemory{
		layer: "handoff", project: "alpha", createdAt: "2026-08-03 00:00:00",
		content: strings.Repeat("alpha", 800), supersedes: oldAlpha,
	})
	insertOpsMemory(t, home, opsMemory{
		layer: "handoff", project: "beta", createdAt: "2026-08-04 00:00:00",
		content: "newer beta",
	})
	insertOpsMemory(t, home, opsMemory{
		layer: "handoff", project: "gamma", createdAt: "2026-07-01 00:00:00",
		content: "too old",
	})

	out := runRoot(t, contractBuild(), "handoff", "latest", "--all-projects", "--since", "2026-08-02T00:00:00Z")
	if !strings.Contains(out, "lab[2]{project,last_handoff,head}:") {
		t.Fatalf("all-projects did not use the lab contract:\n%s", out)
	}
	if !strings.Contains(out, "beta,\"2026-08-04 00:00:00\",newer beta") {
		t.Fatalf("newest beta row missing:\n%s", out)
	}
	if !strings.Contains(out, "alpha,\"2026-08-03 00:00:00\",") || strings.Contains(out, strings.Repeat("alpha", 800)) {
		t.Fatalf("alpha row missing or not capped:\n%s", out)
	}
	if strings.Contains(out, "obsolete alpha") || strings.Contains(out, "gamma") {
		t.Fatalf("all-projects included superseded or out-of-window rows:\n%s", out)
	}
}

func TestParseSinceAcceptsDayDurations(t *testing.T) {
	before := time.Now().UTC().Add(-30 * 24 * time.Hour)
	got, err := parseSince("30d")
	after := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Fatalf("30d parsed to %s, want about 30 days ago", got)
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
		content: "older hook handoff",
	})
	insertOpsMemory(t, home, opsMemory{
		layer: "handoff", project: project, createdAt: "2026-08-02 00:00:00",
		content: "hook handoff " + strings.Repeat("x", 5000),
	})

	if out := runRoot(t, contractBuild(), "hooks", "run", "claude-pills"); !strings.Contains(out, "hook pill") {
		t.Fatalf("pills hook did not execute the pill loader:\n%s", out)
	}
	out := runRoot(t, contractBuild(), "hooks", "run", "claude-handoff")
	if !strings.Contains(out, "hook handoff") {
		t.Fatalf("handoff hook did not execute the handoff loader:\n%s", out)
	}
	if strings.Contains(out, "older hook handoff") {
		t.Fatalf("hook did not limit to the newest handoff:\n%s", out)
	}
	if len(out) >= 4000 || strings.Contains(out, strings.Repeat("x", 5000)) {
		t.Fatalf("hook output was not capped: len=%d\n%s", len(out), out)
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
	db := openSessionOps(t, home)
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
	db := openSessionOps(t, home)
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

func openSessionOps(t *testing.T, home string) *sql.DB {
	t.Helper()
	path := filepath.Join(home, ".roca", "plugins", "roca-ops", "roca-ops.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	return db
}
