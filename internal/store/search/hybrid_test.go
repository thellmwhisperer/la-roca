package search_test

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

func TestSelectRareTermsDropsCommonTokensAndKeepsTheRarest(t *testing.T) {
	got := search.SelectRareTerms([]search.TermStat{
		{Term: "the", Docs: 80},
		{Term: "a", Docs: 70},
		{Term: "salud", Docs: 1},
		{Term: "mental", Docs: 1},
		{Term: "and", Docs: 60},
		{Term: "of", Docs: 55},
	}, 100, 0.02, 5)
	if len(got) != 2 || got[0] != "mental" || got[1] != "salud" {
		t.Fatalf("rare terms = %v, want mental salud", got)
	}
}

func TestSelectRareTermsFallsBackToTheLeastCommonWhenEverythingIsCommon(t *testing.T) {
	got := search.SelectRareTerms([]search.TermStat{
		{Term: "what", Docs: 90},
		{Term: "should", Docs: 40},
		{Term: "never", Docs: 30},
		{Term: "upload", Docs: 25},
		{Term: "a", Docs: 95},
		{Term: "short", Docs: 20},
		{Term: "as", Docs: 70},
		{Term: "private", Docs: 8},
		{Term: "first", Docs: 50},
	}, 100, 0.02, 5)
	if len(got) != 1 || got[0] != "private" {
		t.Fatalf("fallback terms = %v, want the single least-common token", got)
	}
}

func TestFuseRRFRewardsConsensusWithoutNormalizingLegScores(t *testing.T) {
	vector := []search.RankedDoc{
		{Key: "corpus.memories.202", Rank: 2, Score: 0.60, Database: "corpus", Table: "memories", ID: "202"},
		{Key: "corpus.exchanges.1", Rank: 1, Score: 0.90, Database: "corpus", Table: "exchanges", ID: "1"},
	}
	fts := []search.RankedDoc{
		{Key: "corpus.memories.202", Rank: 1, Database: "corpus", Table: "memories", ID: "202"},
		{Key: "corpus.memories.9", Rank: 2, Database: "corpus", Table: "memories", ID: "9"},
	}
	got := search.FuseRRF(vector, fts, 60)
	if len(got) != 3 {
		t.Fatalf("fused = %d, want 3: %+v", len(got), got)
	}
	if got[0].Key != "corpus.memories.202" || !got[0].Consensus {
		t.Fatalf("consensus should win: %+v", got[0])
	}
	dual := 1.0/(60+2) + 1.0/(60+1)
	if diff := got[0].Score - dual; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("rrf score = %v, want %v (no normalization)", got[0].Score, dual)
	}
	if got[1].Score != 1.0/(60+1) {
		t.Fatalf("vector-only score = %v", got[1].Score)
	}
}

func TestCollapseBestRankKeepsTheBestChunkOfOneSource(t *testing.T) {
	got := search.CollapseBestRank([]search.RankedDoc{
		{Database: "corpus", Table: "exchanges", ID: "10", Rank: 4, Score: 0.40, Snippet: "later chunk"},
		{Database: "corpus", Table: "exchanges", ID: "10", Rank: 1, Score: 0.55, Snippet: "best chunk"},
		{Database: "corpus", Table: "exchanges", ID: "11", Rank: 2, Score: 0.50, Snippet: "other"},
	})
	if len(got) != 2 || got[0].ID != "10" || got[0].Rank != 1 || got[0].Snippet != "best chunk" {
		t.Fatalf("collapsed = %+v", got)
	}
}

func TestApplyVectorFloorDropsWeakNeighbors(t *testing.T) {
	got := search.ApplyVectorFloor([]search.RankedDoc{
		{Key: "corpus.exchanges.122300", Rank: 1, Score: 0.47},
		{Key: "corpus.exchanges.99", Rank: 2, Score: 0.22},
	}, 0.35)
	if len(got) != 1 || got[0].Key != "corpus.exchanges.122300" {
		t.Fatalf("floor = %+v", got)
	}
}
