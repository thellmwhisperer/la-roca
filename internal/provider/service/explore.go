package service

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	ExploreModePlain = "explore"
	ExploreModeDeep  = "explore_deep"
)

// The three ways an explore can end without model prose are told apart,
// because an installation with no available model and an interpreter that
// failed or said nothing usable are fixed differently.
const (
	exploreNoModel = "No model was available to read the returned rows, " +
		"so no prose claim can be made beyond them."
	exploreNoAnswer = "The interpretation provider did not answer, " +
		"so no prose claim can be made beyond the returned rows."
	exploreNoProse = "The interpretation provider returned no usable prose, " +
		"so no prose claim can be made beyond the returned rows."
)

// ExploreRequest declares the investigation mode around an ordinary query.
// Deep is explicit at the surface; the service never guesses it from wording.
type ExploreRequest struct {
	QueryRequest
	Deep bool
}

// TerrainCount is one deterministic count calculated from returned rows.
type TerrainCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// Terrain is the factual map handed to the investigation interpreter. It is
// derived only from this run's rows and never from model knowledge.
type Terrain struct {
	RowCount      int            `json:"row_count"`
	Sources       []TerrainCount `json:"sources,omitempty"`
	DateClusters  []TerrainCount `json:"date_clusters,omitempty"`
	Terms         []TerrainCount `json:"co_occurring_terms,omitempty"`
	NegativeSpace []string       `json:"negative_space,omitempty"`
}

// Explore runs the existing query and then gives its result rows to the
// investigation mission of the existing interpreter seat.
func (s *Service) Explore(ctx context.Context, req ExploreRequest) (QueryResult, error) {
	req.SQLOnly = false
	result, err := s.Query(ctx, req.QueryRequest)
	if req.Deep {
		result.Mode = ExploreModeDeep
	} else {
		result.Mode = ExploreModePlain
	}
	if err != nil || result.Path == PathAsk || result.Path == PathRefused ||
		result.Path == PathUnresolved {
		return result, err
	}

	terrain := terrainFromRows(result.Question, result.Columns, result.Rows)
	result.Terrain = &terrain
	if result.Engine == "" {
		result.Interpretation = fallbackExploreInterpretation(exploreNoModel, terrain, req.Deep)
		return result, nil
	}
	if req.Progress != nil {
		req.Progress(QueryPhaseInterpretation)
	}
	mission := InterpretationExplore
	if req.Deep {
		mission = InterpretationExploreDeep
	}
	started := time.Now()
	var onStart func(bool)
	if req.InterpretationStart != nil {
		onStart = func(native bool) { req.InterpretationStart(native, result) }
	}
	answer, interpretErr := s.InterpretStream(ctx, result.Question, result.Columns, result.Rows,
		time.Duration(result.SQLInferenceMS)*time.Millisecond, result.Engine,
		InterpretationContext{Mission: mission, Terrain: terrain}, onStart, req.InterpretationDelta)
	result.InterpretationMS = time.Since(started).Milliseconds()
	result.LatencyMS += result.InterpretationMS
	result.Interpretation = answer.Text
	result.InterpretEngine = answer.Engine
	result.InterpretModel = answer.Model
	result.InterpretNote = answer.Note
	switch {
	case interpretErr != nil:
		result.ProviderError = interpretErr.Error()
		result.Interpretation = fallbackExploreInterpretation(exploreNoAnswer, terrain, req.Deep)
	case strings.TrimSpace(result.Interpretation) == "":
		result.Interpretation = fallbackExploreInterpretation(exploreNoProse, terrain, req.Deep)
	}
	return result, nil
}

func fallbackExploreInterpretation(reason string, terrain Terrain, deep bool) string {
	var b strings.Builder
	b.WriteString(reason)
	fmt.Fprintf(&b, "\n\nTerrain from %d returned rows", terrain.RowCount)
	writeTerrainSentence(&b, "source counts", terrain.Sources)
	writeTerrainSentence(&b, "date clusters", terrain.DateClusters)
	writeTerrainSentence(&b, "co-occurring terms", terrain.Terms)
	if len(terrain.NegativeSpace) > 0 {
		b.WriteString("; negative space: ")
		b.WriteString(strings.Join(terrain.NegativeSpace, "; "))
	}
	b.WriteString(".")
	var probes []string
	for _, term := range terrain.Terms {
		probes = append(probes, term.Value)
		if len(probes) == 3 {
			break
		}
	}
	if len(probes) > 0 {
		if deep {
			b.WriteString("\n\nNext probes: ")
		} else {
			b.WriteString("\n\nTrail hints: ")
		}
		b.WriteString(strings.Join(probes, "; "))
		b.WriteString(".")
	}
	return b.String()
}

func writeTerrainSentence(b *strings.Builder, label string, counts []TerrainCount) {
	if len(counts) == 0 {
		return
	}
	b.WriteString("; ")
	b.WriteString(label)
	b.WriteString(": ")
	for i, count := range counts {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%s: %d", count.Value, count.Count)
	}
}

func terrainFromRows(question string, columns []string, rows []map[string]any) Terrain {
	terrain := Terrain{RowCount: len(rows)}
	sourceColumn := terrainColumn(columns, func(name string) bool {
		return name == "source" || name == "database"
	})
	dateColumns := terrainColumns(columns, func(name string) bool {
		return strings.Contains(name, "date") ||
			strings.Contains(name, "time") || strings.HasSuffix(name, "_at")
	})

	sources, dates, terms := map[string]int{}, map[string]int{}, map[string]int{}
	questionTerms := tokenSet(question)
	for _, row := range rows {
		if sourceColumn != "" {
			if value := strings.TrimSpace(terrainText(row[sourceColumn])); value != "" {
				sources[strings.ToLower(value)]++
			}
		}
		seenDates := map[string]bool{}
		for _, column := range dateColumns {
			if cluster := monthCluster(terrainText(row[column])); cluster != "" {
				seenDates[cluster] = true
			}
		}
		for cluster := range seenDates {
			dates[cluster]++
		}

		seenTerms := map[string]bool{}
		for _, column := range columns {
			name := strings.ToLower(column)
			if column == sourceColumn || slices.Contains(dateColumns, column) ||
				name == "id" || strings.HasSuffix(name, "_id") || strings.Contains(name, "rank") ||
				strings.Contains(name, "count") {
				continue
			}
			for term := range tokenSet(terrainText(row[column])) {
				if !questionTerms[term] && !terrainStopWords[term] &&
					len([]rune(term)) >= 3 && strings.IndexFunc(term, unicode.IsLetter) >= 0 {
					seenTerms[term] = true
				}
			}
		}
		for term := range seenTerms {
			terms[term]++
		}
	}
	terrain.Sources = sortedTerrainCounts(sources, 0)
	terrain.DateClusters = sortedTerrainCounts(dates, 0)
	terrain.Terms = sortedTerrainCounts(terms, 8)

	if len(rows) == 0 {
		terrain.NegativeSpace = append(terrain.NegativeSpace, "the generated SQL returned no rows")
	}
	if sourceColumn == "" {
		terrain.NegativeSpace = append(terrain.NegativeSpace,
			"the returned result set exposes no source labels")
	} else {
		var missing []string
		for _, source := range []string{"memory", "exchange", "human", "thinking"} {
			if sources[source] == 0 {
				missing = append(missing, source)
			}
		}
		if len(missing) > 0 {
			terrain.NegativeSpace = append(terrain.NegativeSpace,
				"no returned rows labelled "+joinedWords(missing))
		}
	}
	if len(dates) == 0 {
		terrain.NegativeSpace = append(terrain.NegativeSpace,
			"the returned result set exposes no parseable date clusters")
	}
	if len(terms) == 0 {
		terrain.NegativeSpace = append(terrain.NegativeSpace,
			"the returned result set exposes no adjacent content terms")
	}
	return terrain
}

func terrainColumn(columns []string, accept func(string) bool) string {
	for _, column := range columns {
		if accept(strings.ToLower(column)) {
			return column
		}
	}
	return ""
}

func terrainColumns(columns []string, accept func(string) bool) []string {
	var matches []string
	for _, column := range columns {
		if accept(strings.ToLower(column)) {
			matches = append(matches, column)
		}
	}
	return matches
}

// terrainText reads a cell only when the source itself stored text there. A
// SQL NULL and a computed number are not content, and rendering them would
// hand the interpreter tokens no row ever contained.
func terrainText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	}
	return ""
}

func monthCluster(value string) string {
	for i := 0; i+7 <= len(value); i++ {
		candidate := value[i : i+7]
		if candidate[4] != '-' || candidate[0] < '1' || candidate[0] > '2' {
			continue
		}
		valid := true
		for _, index := range []int{0, 1, 2, 3, 5, 6} {
			if candidate[index] < '0' || candidate[index] > '9' {
				valid = false
			}
		}
		if valid && candidate[5:7] >= "01" && candidate[5:7] <= "12" {
			return candidate
		}
	}
	return ""
}

func tokenSet(value string) map[string]bool {
	terms := map[string]bool{}
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			terms[strings.ToLower(word.String())] = true
			word.Reset()
		}
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return terms
}

func sortedTerrainCounts(values map[string]int, limit int) []TerrainCount {
	counts := make([]TerrainCount, 0, len(values))
	for value, count := range values {
		counts = append(counts, TerrainCount{Value: value, Count: count})
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].Count != counts[j].Count {
			return counts[i].Count > counts[j].Count
		}
		return counts[i].Value < counts[j].Value
	})
	if limit > 0 && len(counts) > limit {
		counts = counts[:limit]
	}
	return counts
}

func joinedWords(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}

var terrainStopWords = map[string]bool{
	"and": true, "are": true, "but": true, "for": true, "from": true, "has": true,
	"have": true, "into": true, "not": true, "that": true, "the": true, "their": true,
	"this": true, "was": true, "were": true, "what": true, "when": true, "where": true,
	"which": true, "with": true, "you": true, "your": true,
	"con": true, "del": true, "desde": true, "donde": true, "el": true, "ella": true,
	"en": true, "es": true, "esta": true, "fue": true, "la": true, "las": true,
	"los": true, "para": true, "por": true, "que": true, "qué": true, "una": true,
	"uno": true,
}
