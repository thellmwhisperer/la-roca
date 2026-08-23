package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestSearchRunsFTSAloneWhenVectorIsAbsent(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	seedHybridCorpus(t, svc)

	result, err := svc.Search(context.Background(), service.SearchRequest{
		Question: "salud mental", Top: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
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
	svc := initialized(t, freshPaths(t), func(options *service.Options) {
		options.VectorSearch = func(_ context.Context, _ string, _ int, _ string) (service.VectorHits, error) {
			return service.VectorHits{Results: []service.VectorHit{
				{Rank: 1, Score: 0.51, Database: "core", Table: "memories", ID: "1",
					Text: "a private note about salud mental in therapy"},
				{Rank: 2, Score: 0.44, Database: "core", Table: "sessions", ID: "session-salud",
					Text: "Therapy notes\n\nrecovery"},
			}}, nil
		}
	})
	seedHybridCorpus(t, svc)

	result, err := svc.Search(context.Background(), service.SearchRequest{
		Question: "salud mental", Top: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
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

	precise, err := svc.Search(context.Background(), service.SearchRequest{
		Question: "salud mental", Top: 10, RequireBoth: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(precise.Hits) == 0 {
		t.Fatal("require-both dropped every hit")
	}
	for _, hit := range precise.Hits {
		if !hit.Consensus {
			t.Fatalf("require-both leaked a single-leg hit: %+v", hit)
		}
	}
}

func TestSearchResolvesSessionSnippetsFromDeclaredColumns(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	seedHybridCorpus(t, svc)
	result, err := svc.Search(context.Background(), service.SearchRequest{
		Question: "recovery", Top: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
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
	svc, _ := serviceWithPaths(t)
	seedHybridCorpus(t, svc)
	question := "what are the thoughts of the team about the way we should look at the salud mental of the day"
	result, err := svc.Search(context.Background(), service.SearchRequest{
		Question: question, Top: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
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
