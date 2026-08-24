package service_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestSearchRunsFTSAloneWhenVectorIsAbsent(t *testing.T) {
	svc := seededHybridService(t, nil)
	result := mustHybridSearch(t, svc, "salud mental", false)
	if strings.Join(result.Engines, ",") != "fts" {
		t.Fatalf("engines = %v, want fts-only", result.Engines)
	}
	if !strings.Contains(strings.Join(result.Notices, "\n"), "FTS-only") {
		t.Fatalf("missing FTS-only notice: %v", result.Notices)
	}
	if !hybridHitContains(result, "salud mental") {
		t.Fatalf("FTS-only search missed the rare document: %+v", result.Hits)
	}
	if len(result.Hits) == 0 || result.Hits[0].FTSRank == nil || result.Hits[0].VectorRank != nil {
		t.Fatalf("FTS-only hit should label the FTS leg: %+v", result.Hits)
	}
}

func TestSearchFusesVectorAndFTSAndCanRequireBoth(t *testing.T) {
	svc := seededHybridService(t,
		func(_ context.Context, _ string, _ int, _ string) (service.VectorHits, error) {
			return service.VectorHits{Results: []service.VectorHit{
				{Rank: 1, Score: 0.51, Database: "core", Table: "memories", ID: "1",
					Text: "a private note about salud mental in therapy"},
				{Rank: 2, Score: 0.44, Database: "core", Table: "sessions", ID: "session-salud",
					Text: "Therapy notes\n\nrecovery"},
			}}, nil
		})
	result := mustHybridSearch(t, svc, "salud mental", false)
	if strings.Join(result.Engines, ",") != "fts,vector" {
		t.Fatalf("engines = %v", result.Engines)
	}
	foundBoth := false
	for _, hit := range result.Hits {
		if hit.Table == "memories" && hit.Consensus && hit.VectorRank != nil && hit.FTSRank != nil {
			foundBoth = true
		}
	}
	if !foundBoth {
		t.Fatalf("expected a dual-confirmed memory: %+v", result.Hits)
	}

	precise := mustHybridSearch(t, svc, "salud mental", true)
	if len(precise.Hits) == 0 {
		t.Fatal("require-both dropped every hit")
	}
	for _, hit := range precise.Hits {
		if !hit.Consensus {
			t.Fatalf("require-both leaked a single-leg hit: %+v", hit)
		}
	}
}

func TestSearchDoesNotLabelAnUnavailableVectorEngine(t *testing.T) {
	svc := seededHybridService(t,
		func(context.Context, string, int, string) (service.VectorHits, error) {
			return service.VectorHits{Notices: []string{
				"database corpus has no ready vector sidecar; continuing with FTS-only",
			}}, nil
		})
	result := mustHybridSearch(t, svc, "salud mental", false)
	if strings.Join(result.Engines, ",") != "fts" {
		t.Fatalf("engines = %v, want fts-only", result.Engines)
	}
}

func TestSearchReturnsFTSWhenEmbeddingModelIsNotDownloaded(t *testing.T) {
	svc := seededHybridService(t,
		func(context.Context, string, int, string) (service.VectorHits, error) {
			return service.VectorHits{Notices: []string{
				"vector search unavailable: the embedding model is not downloaded",
			}}, nil
		})
	started := time.Now()
	result := mustHybridSearch(t, svc, "salud mental", false)
	if strings.Join(result.Engines, ",") != "fts" {
		t.Fatalf("engines = %v, want fts-only", result.Engines)
	}
	if !hybridHitContains(result, "salud mental") {
		t.Fatalf("FTS results were lost while the model was downloading: %+v", result.Hits)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("search blocked for %s waiting on a model download", time.Since(started))
	}
}

func TestSearchDoesNotFlattenMixedModelVectorGroups(t *testing.T) {
	svc := seededHybridService(t,
		func(context.Context, string, int, string) (service.VectorHits, error) {
			return service.VectorHits{Executed: true, MixedModels: true, Results: []service.VectorHit{
				{Rank: 1, Score: 0.91, Database: "corpus", Table: "memories", ID: "1"},
				{Rank: 1, Score: 0.42, Database: "ops", Table: "memories", ID: "1"},
			}}, nil
		})
	result := mustHybridSearch(t, svc, "salud mental", false)
	if strings.Join(result.Engines, ",") != "fts" {
		t.Fatalf("mixed-model engines = %v, want fts-only", result.Engines)
	}
	for _, hit := range result.Hits {
		if hit.VectorRank != nil {
			t.Fatalf("mixed-model vector hit leaked into fusion: %+v", hit)
		}
	}
	if !strings.Contains(strings.Join(result.Notices, "\n"), "cannot be fused") {
		t.Fatalf("mixed-model degradation was not explained: %v", result.Notices)
	}
}

func TestSearchUsesOnlyVectorWhenEveryFTSTermHasZeroDocumentFrequency(t *testing.T) {
	svc := seededHybridService(t,
		func(context.Context, string, int, string) (service.VectorHits, error) {
			return service.VectorHits{Executed: true, Results: []service.VectorHit{{
				Rank: 1, Score: 0.58, Database: "core", Table: "memories", ID: "1",
				Text: "semantic neighbor",
			}}}, nil
		})
	result := mustHybridSearch(t, svc, "tulipanismo", false)
	if len(result.Terms) != 0 || strings.Join(result.Engines, ",") != "vector" {
		t.Fatalf("zero-DF route terms=%v engines=%v", result.Terms, result.Engines)
	}
}

func TestPluginVectorSearchPreservesMixedModelAndExecutionState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roca-vector")
	script := `#!/bin/sh
printf '%s' '{"mixed_models":true,"vector_executed":true,"results":[],"database_results":[{"database":"corpus","model":"a","results":[{"rank":1,"score":0.9,"database":"corpus","table":"memories","id":"1"}]}],"notices":[]}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	hits, err := service.PluginVectorSearch("")(context.Background(), "salud mental", 100, "all")
	if err != nil {
		t.Fatal(err)
	}
	if !hits.Executed || !hits.MixedModels || len(hits.Results) != 0 {
		t.Fatalf("vector envelope state = %+v", hits)
	}
}

func TestSearchResolvesSessionSnippetsFromDeclaredColumns(t *testing.T) {
	svc := seededHybridService(t, nil)
	result := mustHybridSearch(t, svc, "recovery", false)
	found := false
	for _, hit := range result.Hits {
		if hit.Table == "sessions" && hit.ID == "session-salud" {
			found = true
			if !strings.Contains(hit.Snippet, "Therapy notes") {
				t.Fatalf("session snippet missing catalog title: %+v", hit)
			}
		}
	}
	if !found {
		t.Fatalf("sessions hit missing: %+v", result.Hits)
	}
}

func TestSearchLongQuestionDoesNotPromoteGenericFTSNoise(t *testing.T) {
	svc := seededHybridService(t, nil)
	question := "what are the thoughts of the team about the way we should look at the salud mental of the day"
	result := mustHybridSearch(t, svc, question, false)
	if !containsTerm(result.Terms, "salud") && !containsTerm(result.Terms, "mental") {
		t.Fatalf("rarity selection dropped the rare terms: %v", result.Terms)
	}
	if containsTerm(result.Terms, "the") || containsTerm(result.Terms, "of") ||
		containsTerm(result.Terms, "about") {
		t.Fatalf("rarity selection kept generic words: %v", result.Terms)
	}
	if !hybridHitContains(result, "salud mental") {
		t.Fatalf("long question missed the rare document: terms=%v hits=%+v", result.Terms, result.Hits)
	}
}

func TestHybridGoldenHitsAtTenMatchesOrBeatsEachLeg(t *testing.T) {
	cases := []struct {
		query string
		text  string
	}{
		{"why should I never upload a short as private first", "Initial privacy sabotaged distribution; publish the clip unlisted before switching visibility."},
		{"what happened when I changed the title of a dead short", "Renaming an old dormant clip revived impressions and audience discovery."},
		{"does the bridge from shorts to long-form videos actually work", "The bridge from brief vertical clips to full-length films converted viewers."},
		{"what lessons did I learn about making YouTube shorts for my channel", "Lessons from producing vertical clips for the creator channel."},
	}
	vectorTargets := map[string]string{}
	vectorLeg := service.VectorSearchFunc(func(_ context.Context, question string, _ int, _ string) (service.VectorHits, error) {
		id := vectorTargets[question]
		return service.VectorHits{Executed: true, Results: []service.VectorHit{{
			Rank: 1, Score: 0.58, Database: "core", Table: "memories", ID: id,
		}}}, nil
	})
	hybrid := initialized(t, freshPaths(t), func(options *service.Options) {
		options.VectorSearch = vectorLeg
	})
	vectorTargets = seedHybridGoldenCorpus(t, hybrid, cases)
	fts, _ := serviceWithPaths(t)
	ftsTargets := seedHybridGoldenCorpus(t, fts, cases)

	hybridHits, ftsHits, vectorHits := 0, 0, 0
	for _, golden := range cases {
		hybridResult := mustHybridSearch(t, hybrid, golden.query, false)
		ftsResult := mustHybridSearch(t, fts, golden.query, false)
		if searchResultHasSource(hybridResult, "core.memories."+vectorTargets[golden.query]) {
			hybridHits++
		}
		if searchResultHasSource(ftsResult, "core.memories."+ftsTargets[golden.query]) {
			ftsHits++
		}
		vectorResult := mustHybridVectorSearch(t, vectorLeg, golden.query)
		if vectorHitsHaveSource(vectorResult.Results, "core.memories."+vectorTargets[golden.query]) {
			vectorHits++
		}
	}
	t.Logf("golden hits@10: hybrid=%d pure-fts=%d pure-vector=%d",
		hybridHits, ftsHits, vectorHits)
	if hybridHits < ftsHits || hybridHits < vectorHits {
		t.Fatalf("hybrid hits@10 = %d, pure-fts = %d, pure-vector = %d",
			hybridHits, ftsHits, vectorHits)
	}
}

func TestHybridGoldenAgainstReadOnlyLabCopies(t *testing.T) {
	labDir := os.Getenv("ROCA_HYBRID_GOLDEN_LAB_DIR")
	if labDir == "" {
		t.Skip("set ROCA_HYBRID_GOLDEN_LAB_DIR to read-only golden database copies")
	}
	dbPath := filepath.Join(labDir, "roca.db")
	stateDir := filepath.Join(labDir, "vector-state")
	pluginRoot := filepath.Join(labDir, "plugins")
	for _, path := range []string{dbPath, stateDir, pluginRoot} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("golden lab copy %s: %v", path, err)
		}
	}
	t.Setenv("ROCA_VECTOR_STATE_DIR", stateDir)
	t.Setenv("ROCA_VECTOR_PLUGIN_ROOT", pluginRoot)
	pluginVectorLeg := service.PluginVectorSearch(dbPath)
	vectorCache := make(map[string]service.VectorHits)
	vectorLeg := func(ctx context.Context, question string, k int, databases string) (service.VectorHits, error) {
		key := fmt.Sprintf("%s\x00%d\x00%s", question, k, databases)
		if hits, ok := vectorCache[key]; ok {
			return hits, nil
		}
		hits, err := pluginVectorLeg(ctx, question, k, databases)
		if err == nil {
			vectorCache[key] = hits
		}
		return hits, err
	}
	open := func(vector service.VectorSearchFunc) *service.Service {
		t.Helper()
		svc, err := service.Open(service.Options{
			DBPath: dbPath, DataDir: labDir, ReadOnly: true,
			Version: "golden-eval", Commit: "golden-eval", VectorSearch: vector,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = svc.Close() })
		return svc
	}
	hybrid, fts := open(vectorLeg), open(nil)
	cases := []struct {
		query string
		table string
		ids   []string
	}{
		{"why should I never upload a short as private first", "memories", []string{"202"}},
		{"what happened when I changed the title of a dead short", "memories", []string{"199"}},
		{"does the bridge from shorts to long-form videos actually work", "memories", []string{"219"}},
		{"what lessons did I learn about making YouTube shorts for my channel", "exchanges", []string{"111818", "10119"}},
	}

	hybridHits, ftsHits, vectorHits := 0, 0, 0
	for _, golden := range cases {
		hybridResult := mustHybridSearch(t, hybrid, golden.query, false)
		ftsResult := mustHybridSearch(t, fts, golden.query, false)
		vectorResult := mustHybridVectorSearch(t, vectorLeg, golden.query)
		if goldenSearchHit(hybridResult.Hits, golden.table, golden.ids) {
			hybridHits++
		}
		if goldenSearchHit(ftsResult.Hits, golden.table, golden.ids) {
			ftsHits++
		}
		if goldenVectorHit(vectorResult.Results, golden.table, golden.ids) {
			vectorHits++
		}
	}
	t.Logf("lab-copy golden hits@10: hybrid=%d pure-fts=%d pure-vector=%d",
		hybridHits, ftsHits, vectorHits)
	if vectorHits == 0 {
		t.Fatal("pure-vector historical baseline found no golden target; comparison is vacuous")
	}
	if hybridHits < ftsHits || hybridHits < vectorHits {
		t.Fatalf("hybrid hits@10 = %d, pure-fts = %d, pure-vector = %d",
			hybridHits, ftsHits, vectorHits)
	}
}

func goldenSearchHit(hits []service.SearchHit, table string, ids []string) bool {
	for _, hit := range hits {
		if hit.Table == table && containsTerm(ids, hit.ID) {
			return true
		}
	}
	return false
}

func goldenVectorHit(hits []service.VectorHit, table string, ids []string) bool {
	for _, hit := range hits {
		if hit.Rank <= 10 && hit.Table == table && containsTerm(ids, hit.ID) {
			return true
		}
	}
	return false
}

func seededHybridService(t *testing.T, vectorSearch service.VectorSearchFunc) *service.Service {
	t.Helper()
	var svc *service.Service
	if vectorSearch == nil {
		svc, _ = serviceWithPaths(t)
	} else {
		svc = initialized(t, freshPaths(t), func(options *service.Options) {
			options.VectorSearch = vectorSearch
		})
	}
	seedHybridCorpus(t, svc)
	return svc
}

func mustHybridSearch(t *testing.T, svc *service.Service, question string, requireBoth bool) service.SearchResult {
	t.Helper()
	result, err := svc.Search(context.Background(), service.SearchRequest{
		Question: question, Top: 10, RequireBoth: requireBoth,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustHybridVectorSearch(t *testing.T, search service.VectorSearchFunc, question string) service.VectorHits {
	t.Helper()
	result, err := search(context.Background(), question, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Executed {
		t.Fatalf("pure-vector baseline did not execute for %q: notices=%v", question, result.Notices)
	}
	return result
}

func seedHybridGoldenCorpus(t *testing.T, svc *service.Service, cases []struct {
	query string
	text  string
}) map[string]string {
	t.Helper()
	targets := make(map[string]string, len(cases))
	for _, golden := range cases {
		result, err := svc.DB().SQL().Exec(
			"INSERT INTO memories (layer, content, origin) VALUES ('discovery', ?, 'agent')", golden.text)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		targets[golden.query] = fmt.Sprint(id)
	}
	for index := 0; index < 60; index++ {
		seed(t, svc, "discovery", fmt.Sprintf("generic unrelated fixture note %d", index))
	}
	if _, err := svc.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
	return targets
}

func searchResultHasSource(result service.SearchResult, source string) bool {
	for _, hit := range result.Hits {
		if hit.Source == source {
			return true
		}
	}
	return false
}

func vectorHitsHaveSource(hits []service.VectorHit, source string) bool {
	for _, hit := range hits {
		if hit.Database+"."+hit.Table+"."+hit.ID == source {
			return true
		}
	}
	return false
}

func seedHybridCorpus(t *testing.T, svc *service.Service) {
	t.Helper()
	seed(t, svc, "discovery", "a private note about salud mental in therapy")
	for i := 0; i < 40; i++ {
		seed(t, svc, "discovery", "the thoughts of the team about the way we look at the day")
	}
	if _, err := svc.DB().SQL().Exec(
		`INSERT INTO sessions (session_id, project, title) VALUES ('session-salud', 'recovery', 'Therapy notes')`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func hybridHitContains(result service.SearchResult, text string) bool {
	for _, hit := range result.Hits {
		if strings.Contains(strings.ToLower(hit.Snippet), strings.ToLower(text)) {
			return true
		}
	}
	return false
}

func containsTerm(terms []string, want string) bool {
	for _, term := range terms {
		if term == want {
			return true
		}
	}
	return false
}
