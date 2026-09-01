package service_test

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestListPillsDedupesSlugDuplicatesKeepingTheNewest(t *testing.T) {
	svc := sessionContextService(t)
	insertPill(t, svc, pillSeed{
		slug: "build", content: "April build pill", createdAt: "2026-04-01 00:00:00",
		project: "demo",
	})
	newer := insertPill(t, svc, pillSeed{
		slug: "build", content: "June build pill", createdAt: "2026-06-01 00:00:00",
		project: "demo",
	})

	got, err := svc.ListPills(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pills) != 1 {
		t.Fatalf("pills = %d, want 1 after slug dedupe: %+v", len(got.Pills), got.Pills)
	}
	if got.Pills[0].ID != newer || got.Pills[0].Content != "June build pill" {
		t.Fatalf("kept %+v, want newest id %d with June content", got.Pills[0], newer)
	}
}

func TestListPillsNormalizesTimestampsAndOrdersUnknownDatesDeterministically(t *testing.T) {
	svc := sessionContextService(t)
	insertPill(t, svc, pillSeed{
		slug: "mixed", content: "ISO midnight", createdAt: "2026-06-01T00:00:00Z",
		project: "demo",
	})
	later := insertPill(t, svc, pillSeed{
		slug: "mixed", content: "SQLite one o'clock", createdAt: "2026-06-01 01:00:00",
		project: "demo",
	})
	insertPill(t, svc, pillSeed{
		slug: "unknown", content: "invalid timestamp", createdAt: "not-a-time",
		project: "demo",
	})
	newerUnknown := insertPill(t, svc, pillSeed{
		slug: "unknown", content: "null timestamp", createdAt: nil,
		project: "demo",
	})

	got, err := svc.ListPills(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	bySlug := map[string]service.MemoryRecord{}
	for _, pill := range got.Pills {
		bySlug[pill.Slug] = pill
	}
	if bySlug["mixed"].ID != later || bySlug["mixed"].Content != "SQLite one o'clock" {
		t.Fatalf("mixed timestamp winner = %+v, want later id %d", bySlug["mixed"], later)
	}
	if bySlug["unknown"].ID != newerUnknown || bySlug["unknown"].CreatedAt != "" {
		t.Fatalf("unknown timestamp winner = %+v, want highest id %d with empty date", bySlug["unknown"], newerUnknown)
	}
}

func TestListPillsScopesToTheProjectAndIncludesGlobals(t *testing.T) {
	svc := sessionContextService(t)
	insertPill(t, svc, pillSeed{
		slug: "build", content: "demo build", createdAt: "2026-06-01 00:00:00",
		project: "demo",
	})
	insertPill(t, svc, pillSeed{
		slug: "global-rule", content: "applies everywhere", createdAt: "2026-05-01 00:00:00",
	})
	insertPill(t, svc, pillSeed{
		slug: "other-build", content: "other project", createdAt: "2026-07-01 00:00:00",
		project: "other",
	})

	got, err := svc.ListPills(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	slugs := pillSlugs(got)
	if strings.Contains(strings.Join(slugs, ","), "other-build") {
		t.Fatalf("listed another project's pill: %v", slugs)
	}
	if !containsAll(slugs, "build", "global-rule") {
		t.Fatalf("slugs = %v, want demo and global", slugs)
	}
}

func TestListPillsReportsUnsluggedIdsWithoutLoadingThem(t *testing.T) {
	svc := sessionContextService(t)
	insertPill(t, svc, pillSeed{
		slug: "build", content: "slugged", createdAt: "2026-06-01 00:00:00",
		project: "demo",
	})
	orphan := insertPill(t, svc, pillSeed{
		content: "no slug in metadata", createdAt: "2026-06-02 00:00:00",
		project: "demo",
	})

	got, err := svc.ListPills(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pills) != 1 || got.Pills[0].Slug != "build" {
		t.Fatalf("loaded unslugged rows as pills: %+v", got.Pills)
	}
	if len(got.Unslugged) != 1 || got.Unslugged[0] != orphan {
		t.Fatalf("unslugged = %v, want [%d]", got.Unslugged, orphan)
	}
}

func TestListPillsKeepsContentLongerThanTheTableRendererBudget(t *testing.T) {
	svc := sessionContextService(t)
	body := "full pill body " + strings.Repeat("x", 200)
	insertPill(t, svc, pillSeed{
		slug: "long", content: body, createdAt: "2026-06-01 00:00:00",
		project: "demo",
	})

	got := mustListPills(t, svc, "demo")
	if len(got.Pills) != 1 || got.Pills[0].Content != body {
		t.Fatalf("content was truncated or lost: %+v", got.Pills)
	}
}

func TestShowPillReturnsOneCompletePill(t *testing.T) {
	svc := sessionContextService(t)
	insertPill(t, svc, pillSeed{
		slug: "build", content: "April", createdAt: "2026-04-01 00:00:00",
		project: "demo",
	})
	insertPill(t, svc, pillSeed{
		slug: "build", content: "June", createdAt: "2026-06-01 00:00:00",
		project: "demo",
	})

	got, err := svc.ShowPill(t.Context(), "demo", "build")
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != "build" || got.Content != "June" {
		t.Fatalf("show = %+v, want the newest complete build pill", got)
	}
}

func TestLatestHandoffsSkipsSupersededRows(t *testing.T) {
	svc := sessionContextService(t)
	first := insertHandoff(t, svc, handoffSeed{
		content: "first close", createdAt: "2026-08-01 00:00:00", project: "demo",
	})
	insertHandoff(t, svc, handoffSeed{
		content: "replacement close", createdAt: "2026-08-02 00:00:00",
		project: "demo", supersedes: first,
	})

	got, err := svc.LatestHandoffs(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Handoffs) != 1 || got.Handoffs[0].Content != "replacement close" {
		t.Fatalf("handoffs = %+v, want only the replacement", got.Handoffs)
	}
}

func TestLatestHandoffsKeepsANewHandoffThatDoesNotSupersede(t *testing.T) {
	svc := sessionContextService(t)
	insertHandoff(t, svc, handoffSeed{
		content: "session close", createdAt: "2026-08-01 00:00:00", project: "demo",
	})
	insertHandoff(t, svc, handoffSeed{
		content: "worker receipt that did not supersede", createdAt: "2026-08-02 00:00:00",
		project: "demo",
	})

	got, err := svc.LatestHandoffs(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Handoffs) != 2 {
		t.Fatalf("handoffs = %d, want both active unsuperseded rows: %+v",
			len(got.Handoffs), got.Handoffs)
	}
}

func TestLatestHandoffsFallsBackToGlobalWhenTheProjectHasNone(t *testing.T) {
	svc := sessionContextService(t)
	insertHandoff(t, svc, handoffSeed{
		content: "global close", createdAt: "2026-08-01 00:00:00",
	})
	insertHandoff(t, svc, handoffSeed{
		content: "other project close", createdAt: "2026-08-02 00:00:00",
		project: "other",
	})

	got, err := svc.LatestHandoffs(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !got.GlobalFallback {
		t.Fatal("expected global fallback when the project has no handoff")
	}
	if len(got.Handoffs) != 1 || got.Handoffs[0].Content != "global close" {
		t.Fatalf("fallback = %+v, want the global handoff", got.Handoffs)
	}
}

func TestLatestHandoffsPrefersProjectRowsOverGlobals(t *testing.T) {
	svc := sessionContextService(t)
	insertHandoff(t, svc, handoffSeed{
		content: "global close", createdAt: "2026-08-03 00:00:00",
	})
	insertHandoff(t, svc, handoffSeed{
		content: "demo close", createdAt: "2026-08-01 00:00:00", project: "demo",
	})

	got, err := svc.LatestHandoffs(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.GlobalFallback || len(got.Handoffs) != 1 || got.Handoffs[0].Content != "demo close" {
		t.Fatalf("got %+v, want only the project handoff", got)
	}
}

func TestLatestHandoffsFallsBackAfterProjectRowsAreSuperseded(t *testing.T) {
	svc := sessionContextService(t)
	insertHandoff(t, svc, handoffSeed{
		content: "global close", createdAt: "2026-08-01 00:00:00",
	})
	old := insertHandoff(t, svc, handoffSeed{
		content: "obsolete demo close", createdAt: "2026-08-02 00:00:00", project: "demo",
	})
	insertMemory(t, svc, "decision", "replacement decision", "demo", "2026-08-03 00:00:00", old, nil)

	got, err := svc.LatestHandoffs(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !got.GlobalFallback || len(got.Handoffs) != 1 || got.Handoffs[0].Content != "global close" {
		t.Fatalf("got %+v, want the global current handoff", got)
	}
}

func TestSessionContextRequiresRocaOps(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	for _, load := range []func() error{
		func() error { _, err := svc.ListPills(t.Context(), "demo"); return err },
		func() error { _, err := svc.LatestHandoffs(t.Context(), "demo"); return err },
	} {
		if err := load(); err == nil || !strings.Contains(err.Error(), "features.roca_ops") {
			t.Fatalf("session context did not require roca-ops: %v", err)
		}
	}
}

func TestListPillsIgnoresInactiveRows(t *testing.T) {
	svc := sessionContextService(t)
	metadata, err := json.Marshal(map[string]any{"pill_slug": "retired"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.memories.Exec(
		`INSERT INTO memories (layer, content, metadata, origin, project, status, created_at)
		 VALUES ('pill', 'old', ?, 'agent', 'demo', 'resolved', '2026-06-01 00:00:00')`,
		string(metadata)); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ListPills(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pills) != 0 {
		t.Fatalf("inactive pills were loaded: %+v", got.Pills)
	}
}

type sessionContextFixture struct {
	*service.Service
	memories *sql.DB
}

func sessionContextService(t *testing.T) *sessionContextFixture {
	t.Helper()
	svc, plugins := enabledRocaOps(t)
	memories := openRocaOps(t, plugins)
	t.Cleanup(func() { _ = memories.Close() })
	return &sessionContextFixture{Service: svc, memories: memories}
}

func mustListPills(t *testing.T, svc *sessionContextFixture, project string) service.PillList {
	t.Helper()
	list, err := svc.ListPills(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

type pillSeed struct {
	slug, content, project string
	createdAt              any
}

func insertPill(t *testing.T, svc *sessionContextFixture, seed pillSeed) int64 {
	t.Helper()
	metadata := map[string]any{}
	if seed.slug != "" {
		metadata["pill_slug"] = seed.slug
	}
	return insertMemory(t, svc, "pill", seed.content, seed.project, seed.createdAt, 0, metadata)
}

type handoffSeed struct {
	content, project, createdAt string
	supersedes                  int64
}

func insertHandoff(t *testing.T, svc *sessionContextFixture, seed handoffSeed) int64 {
	t.Helper()
	return insertMemory(t, svc, "handoff", seed.content, seed.project, seed.createdAt, seed.supersedes, nil)
}

func insertMemory(t *testing.T, svc *sessionContextFixture, layer, content, project string, createdAt any,
	supersedes int64, metadata map[string]any) int64 {
	t.Helper()
	if metadata == nil {
		metadata = map[string]any{}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var projectArg any
	if project != "" {
		projectArg = project
	}
	var supersedesArg any
	if supersedes != 0 {
		supersedesArg = supersedes
	}
	result, err := svc.memories.Exec(
		`INSERT INTO memories (layer, content, metadata, origin, project, status, supersedes, created_at)
		 VALUES (?, ?, ?, 'agent', ?, 'active', ?, ?)`,
		layer, content, string(encoded), projectArg, supersedesArg, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func pillSlugs(got service.PillList) []string {
	slugs := make([]string, 0, len(got.Pills))
	for _, pill := range got.Pills {
		slugs = append(slugs, pill.Slug)
	}
	return slugs
}

func containsAll(have []string, want ...string) bool {
	present := map[string]bool{}
	for _, item := range have {
		present[item] = true
	}
	for _, item := range want {
		if !present[item] {
			return false
		}
	}
	return true
}
