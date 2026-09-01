package service_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestListPillsDedupesSlugDuplicatesKeepingTheNewest(t *testing.T) {
	svc, _ := serviceWithPaths(t)
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

func TestListPillsScopesToTheProjectAndIncludesGlobals(t *testing.T) {
	svc, _ := serviceWithPaths(t)
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
	svc, _ := serviceWithPaths(t)
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
	svc, _ := serviceWithPaths(t)
	body := "full pill body " + strings.Repeat("x", 200)
	insertPill(t, svc, pillSeed{
		slug: "long", content: body, createdAt: "2026-06-01 00:00:00",
		project: "demo",
	})

	got, err := svc.ListPills(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pills) != 1 || got.Pills[0].Content != body {
		t.Fatalf("content was truncated or lost: %+v", got.Pills)
	}
}

func TestShowPillReturnsOneCompletePill(t *testing.T) {
	svc, _ := serviceWithPaths(t)
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
	svc, _ := serviceWithPaths(t)
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
	svc, _ := serviceWithPaths(t)
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
	svc, _ := serviceWithPaths(t)
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
	svc, _ := serviceWithPaths(t)
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

func TestListPillsIgnoresInactiveRows(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	metadata, err := json.Marshal(map[string]any{"pill_slug": "retired"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DB().SQL().Exec(
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

type pillSeed struct {
	slug, content, project, createdAt string
}

func insertPill(t *testing.T, svc *service.Service, seed pillSeed) int64 {
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

func insertHandoff(t *testing.T, svc *service.Service, seed handoffSeed) int64 {
	t.Helper()
	return insertMemory(t, svc, "handoff", seed.content, seed.project, seed.createdAt, seed.supersedes, nil)
}

func insertMemory(t *testing.T, svc *service.Service, layer, content, project, createdAt string,
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
	result, err := svc.DB().SQL().Exec(
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
