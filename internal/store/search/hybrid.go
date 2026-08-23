package search

import (
	"sort"
	"strings"
)

// Hybrid retrieval constants measured against the live corpus: rarity drops
// terms that appear in more than two percent of documents, RRF uses k=60, and
// each leg oversamples about 100 candidates before fusion.
const (
	RRFK             = 60
	HybridOversample = 100
	MaxRareTerms     = 5
	MaxDFRatio       = 0.02
	MinVectorScore   = 0.35
	DefaultTop       = 10
)

const (
	LegVector = "vector"
	LegFTS    = "fts"
)

// TermStat is one candidate token and how many documents in the searched
// indexes actually contain it.
type TermStat struct {
	Term string
	Docs int
}

// RankedDoc is one source as it appears in a single retrieval list. Rank is
// 1-based. Score is the leg's native score (cosine for vector, unused for FTS).
type RankedDoc struct {
	Key      string
	Rank     int
	Score    float64
	Database string
	Table    string
	ID       string
	Snippet  string
}

// FusedDoc is one source after Reciprocal Rank Fusion. Legs names every list
// that contributed; Consensus is true when both did.
type FusedDoc struct {
	Key         string
	Score       float64
	Database    string
	Table       string
	ID          string
	Snippet     string
	Legs        []string
	Consensus   bool
	VectorRank  int
	VectorScore float64
	HasVector   bool
	FTSRank     int
	HasFTS      bool
}

// SourceKey is the stable identity used to collapse chunks of one source
// before fusion: database.table.id.
func SourceKey(database, table, id string) string {
	return database + "." + table + "." + id
}

// SelectRareTerms keeps the rarest tokens for an FTS MATCH. Terms that appear
// in more than maxRatio of the corpus are dropped; of what remains, at most
// keep of the rarest survive. When every token is common, the least-common
// tokens are kept instead so a long question still searches something.
func SelectRareTerms(stats []TermStat, corpusDocs int, maxRatio float64, keep int) []string {
	if keep <= 0 {
		keep = MaxRareTerms
	}
	if maxRatio <= 0 {
		maxRatio = MaxDFRatio
	}
	var rare, common []TermStat
	for _, stat := range stats {
		if strings.TrimSpace(stat.Term) == "" {
			continue
		}
		if stat.Docs <= 0 {
			continue
		}
		if corpusDocs > 0 && float64(stat.Docs)/float64(corpusDocs) > maxRatio {
			common = append(common, stat)
			continue
		}
		rare = append(rare, stat)
	}
	fallback := false
	pool := rare
	if len(pool) == 0 {
		pool = common
		fallback = len(pool) > 0
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].Docs != pool[j].Docs {
			return pool[i].Docs < pool[j].Docs
		}
		return pool[i].Term < pool[j].Term
	})
	if fallback && len(pool) > 0 {
		least := pool[0].Docs
		cut := 0
		for cut < len(pool) && pool[cut].Docs == least {
			cut++
		}
		pool = pool[:cut]
	}
	if len(pool) > keep {
		pool = pool[:keep]
	}
	terms := make([]string, len(pool))
	for i, stat := range pool {
		terms[i] = stat.Term
	}
	return terms
}

// CollapseBestRank keeps one entry per source, the one with the lowest rank
// (and, on a tie, the highest score). Vector KNN can return several chunks of
// the same source; fusion must see that source once.
func CollapseBestRank(docs []RankedDoc) []RankedDoc {
	best := make(map[string]RankedDoc, len(docs))
	order := make([]string, 0, len(docs))
	for _, doc := range docs {
		if doc.Key == "" {
			doc.Key = SourceKey(doc.Database, doc.Table, doc.ID)
		}
		previous, seen := best[doc.Key]
		if !seen {
			best[doc.Key] = doc
			order = append(order, doc.Key)
			continue
		}
		if doc.Rank < previous.Rank || (doc.Rank == previous.Rank && doc.Score > previous.Score) {
			best[doc.Key] = doc
		}
	}
	out := make([]RankedDoc, 0, len(order))
	for _, key := range order {
		out = append(out, best[key])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Key < out[j].Key
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

// FuseRRF merges independent ranked lists with Reciprocal Rank Fusion.
// score(doc) = sum 1/(k+rank) across the lists it appears in. The lists are
// never score-normalized against each other.
func FuseRRF(vector, fts []RankedDoc, k int) []FusedDoc {
	if k <= 0 {
		k = RRFK
	}
	vector = CollapseBestRank(vector)
	fts = CollapseBestRank(fts)
	fused := map[string]*FusedDoc{}
	add := func(doc RankedDoc, leg string) {
		item, ok := fused[doc.Key]
		if !ok {
			item = &FusedDoc{
				Key:      doc.Key,
				Database: doc.Database,
				Table:    doc.Table,
				ID:       doc.ID,
				Snippet:  doc.Snippet,
			}
			fused[doc.Key] = item
		}
		item.Score += 1 / float64(k+doc.Rank)
		if item.Snippet == "" {
			item.Snippet = doc.Snippet
		}
		switch leg {
		case LegVector:
			item.HasVector = true
			item.VectorRank = doc.Rank
			item.VectorScore = doc.Score
			if doc.Snippet != "" {
				item.Snippet = doc.Snippet
			}
		case LegFTS:
			item.HasFTS = true
			item.FTSRank = doc.Rank
			if item.Snippet == "" {
				item.Snippet = doc.Snippet
			}
		}
	}
	for _, doc := range vector {
		add(doc, LegVector)
	}
	for _, doc := range fts {
		add(doc, LegFTS)
	}
	out := make([]FusedDoc, 0, len(fused))
	for _, item := range fused {
		if item.HasVector {
			item.Legs = append(item.Legs, LegVector)
		}
		if item.HasFTS {
			item.Legs = append(item.Legs, LegFTS)
		}
		item.Consensus = item.HasVector && item.HasFTS
		out = append(out, *item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Consensus != out[j].Consensus {
			return out[i].Consensus
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// ApplyVectorFloor drops vector candidates whose cosine is below minScore.
func ApplyVectorFloor(docs []RankedDoc, minScore float64) []RankedDoc {
	if minScore <= 0 {
		minScore = MinVectorScore
	}
	out := make([]RankedDoc, 0, len(docs))
	for _, doc := range docs {
		if doc.Score >= minScore {
			out = append(out, doc)
		}
	}
	return CollapseBestRank(out)
}
