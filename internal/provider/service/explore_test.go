package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

const exploreSQL = "SELECT 'memory' AS source, '2026-07-02' AS created_at, 'cedar map orbit' AS text " +
	"UNION ALL SELECT 'exchange', '2026-08-09', 'cedar trail orbit' LIMIT 10"

func TestExplorePinsEachInterpreterMissionAndItsGroundedTerrain(t *testing.T) {
	for _, tc := range []struct {
		name, mode, mission string
		deep                bool
		missionRules        []string
		rejectedRules       []string
	}{
		{
			name: "plain explore gives short investigation trails", mode: "explore",
			mission:       "mission: investigation-light",
			missionRules:  []string{"short trail hints", "one concept per hint"},
			rejectedRules: []string{"2-3 next probes", "covering source counts"},
		},
		{
			name: "deep explore maps the terrain and proposes probes", mode: "explore_deep",
			mission: "mission: investigation-deep", deep: true,
			missionRules: []string{
				"full terrain map", "source counts", "date clusters",
				"co-occurring terms", "negative space", "2-3 next probes",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newTwoInferenceFake([]string{exploreSQL}, "A grounded investigation answer.")
			svc := serviceWithModel(t, fake)

			result, err := svc.Explore(context.Background(), service.ExploreRequest{
				QueryRequest: service.QueryRequest{Question: "orbit"}, Deep: tc.deep,
			})
			if err != nil {
				t.Fatalf("Explore: %v", err)
			}
			if result.Mode != tc.mode || result.Interpretation != "A grounded investigation answer." {
				t.Fatalf("result mode/prose = %q/%q", result.Mode, result.Interpretation)
			}
			if len(fake.sqlRequests) != 1 || len(fake.proseRequests) != 1 {
				t.Fatalf("calls = SQL %d, prose %d; want one of each",
					len(fake.sqlRequests), len(fake.proseRequests))
			}
			prompt := fake.proseRequests[0]
			for _, want := range append([]string{
				tc.mission,
				"row_count: 2",
				"memory: 1", "exchange: 1",
				"2026-07: 1", "2026-08: 1",
				"cedar: 2",
				"no returned rows labelled human or thinking",
				"facts were computed deterministically from the returned rows",
			}, tc.missionRules...) {
				if !strings.Contains(prompt, want) {
					t.Errorf("mission prompt lacks %q:\n%s", want, prompt)
				}
			}
			for _, rejected := range tc.rejectedRules {
				if strings.Contains(prompt, rejected) {
					t.Errorf("mission prompt unexpectedly contains %q:\n%s", rejected, prompt)
				}
			}
			if strings.Contains(fake.sqlRequests[0], "cedar map orbit") {
				t.Fatalf("result rows reached the SQL inference:\n%s", fake.sqlRequests[0])
			}
			if result.Terrain.RowCount != 2 || terrainCount(result.Terrain.Sources, "memory") != 1 ||
				terrainCount(result.Terrain.Terms, "cedar") != 2 {
				t.Fatalf("terrain was not derived from the result: %+v", result.Terrain)
			}
		})
	}
}

func TestDeepExploreRoutesToExploreOrderThenInterpretOrderThenMain(t *testing.T) {
	frontier := newTwoInferenceFake([]string{exploreSQL}, "main answer")
	frontier.name, frontier.model = "codex", "frontier"
	interpreter := newTwoInferenceFake(nil, "interpret answer")
	interpreter.name, interpreter.model = "ollama", "local"
	explorer := newTwoInferenceFake(nil, "deep answer")
	explorer.name, explorer.model = "claude", "strong"

	svc := initialized(t, freshPaths(t), func(options *service.Options) {
		options.Providers = cascadeOf(frontier)
		options.Interpreters = cascadeOf(interpreter)
		options.Explorers = cascadeOf(explorer)
	})
	deep, err := svc.Explore(t.Context(), service.ExploreRequest{
		QueryRequest: service.QueryRequest{Question: "orbit"}, Deep: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if deep.InterpretEngine != "claude" || deep.Interpretation != "deep answer" ||
		len(explorer.proseRequests) != 1 || len(interpreter.proseRequests) != 0 {
		t.Fatalf("deep route = %+v; explore calls %d, interpret calls %d",
			deep, len(explorer.proseRequests), len(interpreter.proseRequests))
	}

	plain, err := svc.Explore(t.Context(), service.ExploreRequest{
		QueryRequest: service.QueryRequest{Question: "orbit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plain.InterpretEngine != "ollama" || plain.Interpretation != "interpret answer" ||
		len(interpreter.proseRequests) != 1 {
		t.Fatalf("plain route = %+v; interpret calls %d", plain, len(interpreter.proseRequests))
	}
}

func TestDeepExploreDeclaresExploreOrderFailureBeforeInterpretFallback(t *testing.T) {
	frontier := newTwoInferenceFake([]string{exploreSQL}, "main answer")
	interpreter := newTwoInferenceFake(nil, "fallback answer")
	interpreter.name = "ollama"
	explorer := newTwoInferenceFake(nil, "unused")
	explorer.name, explorer.notReady = "claude", "session unavailable"
	result := runDeepExplore(t, frontier, []provider.Provider{interpreter}, explorer)
	if result.InterpretEngine != "ollama" || !strings.Contains(result.InterpretNote, "session unavailable") ||
		!strings.Contains(result.InterpretNote, "ollama") {
		t.Fatalf("fallback was not declared: %+v", result)
	}
}

func TestDeepExploreFallsFromExploreOrderToMainWhenNoInterpretOrderExists(t *testing.T) {
	frontier := newTwoInferenceFake([]string{exploreSQL}, "main fallback answer")
	frontier.name = "codex"
	explorer := newTwoInferenceFake(nil, "unused")
	explorer.name, explorer.notReady = "claude", "session unavailable"
	result := runDeepExplore(t, frontier, nil, explorer)
	if result.InterpretEngine != "codex" || result.Interpretation != "main fallback answer" ||
		!strings.Contains(result.InterpretNote, "session unavailable") ||
		!strings.Contains(result.InterpretNote, "rows were read by codex") {
		t.Fatalf("main fallback was not declared: %+v", result)
	}
}

func TestExploreNeverBecomesRowsOnlyWhenNoModelProseArrives(t *testing.T) {
	for _, tc := range []struct {
		name, prose, reason, providerError string
		failure                            error
	}{
		{
			name: "the interpreter never answered", failure: errors.New("interpretation stopped"),
			reason: "The interpretation provider did not answer", providerError: "interpretation stopped",
		},
		{
			name: "the guardian left no prose standing", prose: "...",
			reason: "The interpretation provider returned no usable prose",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frontier := newTwoInferenceFake([]string{exploreSQL}, "unused")
			silent := answering("ollama", tc.prose)
			silent.fail = tc.failure
			result := runDeepExplore(t, frontier, []provider.Provider{silent}, nil)
			for _, want := range []string{
				tc.reason, "2 returned rows",
				"memory: 1", "2026-07: 1", "cedar: 2", "Next probes:",
			} {
				if !strings.Contains(result.Interpretation, want) {
					t.Errorf("deterministic fallback lacks %q:\n%s", want, result.Interpretation)
				}
			}
			if result.ProviderError != tc.providerError {
				t.Fatalf("provider error = %q", result.ProviderError)
			}
		})
	}
}

// The keyword rescue projects a NULL author and a bm25 float, and neither is
// content a row ever carried.
func TestExploreTerrainReadsOnlyStoredTextCells(t *testing.T) {
	fake := newTwoInferenceFake([]string{"SELECT 'exchange' AS source, NULL AS author, " +
		"'2026-08-09' AS created_at, 'cedar trail orbit' AS text, 3.1416 AS rango LIMIT 10"},
		"A grounded investigation answer.")
	svc := serviceWithModel(t, fake)

	result, err := svc.Explore(t.Context(), service.ExploreRequest{
		QueryRequest: service.QueryRequest{Question: "orbit"}, Deep: true,
	})
	if err != nil {
		t.Fatalf("Explore: %v", err)
	}
	for _, unwanted := range []string{"nil", "null", "1416"} {
		if terrainCount(result.Terrain.Terms, unwanted) != 0 {
			t.Errorf("terrain terms invented %q: %+v", unwanted, result.Terrain.Terms)
		}
	}
	if terrainCount(result.Terrain.Terms, "cedar") != 1 {
		t.Fatalf("terrain lost its text terms: %+v", result.Terrain.Terms)
	}
}

func runDeepExplore(t *testing.T, main provider.Provider,
	interpreters []provider.Provider, explorer provider.Provider) service.QueryResult {
	t.Helper()
	svc := initialized(t, freshPaths(t), func(options *service.Options) {
		options.Providers = cascadeOf(main)
		options.Interpreters = cascadeOf(interpreters...)
		if explorer != nil {
			options.Explorers = cascadeOf(explorer)
		}
	})
	result, err := svc.Explore(t.Context(), service.ExploreRequest{
		QueryRequest: service.QueryRequest{Question: "orbit"}, Deep: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func terrainCount(counts []service.TerrainCount, value string) int {
	for _, count := range counts {
		if count.Value == value {
			return count.Count
		}
	}
	return 0
}

var _ provider.Provider = (*twoInferenceFake)(nil)
