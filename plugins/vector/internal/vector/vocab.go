package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	// vocabTopK is the maximum k the binary's query path accepts: vocabulary
	// discovery always spends the whole discovery budget.
	vocabTopK = 100
	// vocabMinLocalDocs keeps terms that surfaced in a single hit out of the
	// ranking: one document is an anecdote, not an avenue.
	vocabMinLocalDocs = 2
	// vocabSmoothing is the Laplace prior of the log-odds score, so a term the
	// census never saw is rare, not infinite.
	vocabSmoothing = 0.5
	// vocabAvenueLimit and vocabAvenueCapacity bound the report a human reads;
	// the ranking stays reproducible behind the same constants.
	vocabAvenueLimit    = 40
	vocabAvenueCapacity = 8
	// vocabJoinOverlap is the document-set Jaccard at which two terms belong to
	// the same research avenue.
	vocabJoinOverlap = 0.25
	// vocabCensusBatch sizes the term lookups against the census table.
	vocabCensusBatch = 400
)

// VocabTerm is one ranked term: how many discovery documents carry it, how many
// census documents do, and the log-odds of that contrast.
type VocabTerm struct {
	Term       string  `json:"term"`
	LocalDocs  int     `json:"local_docs"`
	GlobalDocs int64   `json:"global_docs"`
	Score      float64 `json:"score"`
}

// VocabAvenue is one research avenue: terms that share the same hit documents.
type VocabAvenue struct {
	Rank  int         `json:"rank"`
	Terms []VocabTerm `json:"terms"`
}

// VocabReport is the reproducible answer of `vocab <concept>`: no inference
// participated in any part of it.
type VocabReport struct {
	Concept         string         `json:"concept"`
	TopK            int            `json:"top_k"`
	Hits            int            `json:"hits"`
	HitsByKind      map[string]int `json:"hits_by_kind"`
	CensusDocuments int64          `json:"census_documents"`
	Avenues         []VocabAvenue  `json:"avenues"`
	ElapsedMS       int64          `json:"elapsed_ms"`
}

// Vocab discovers the vocabulary around a concept. The vector index nominates
// the top-100 exchanges and thinking blocks; the global census decides which
// of their terms are discriminative and which are only workshop noise.
func (i Index) Vocab(ctx context.Context, concept string) (VocabReport, error) {
	if err := i.validate(); err != nil {
		return VocabReport{}, err
	}
	concept = strings.TrimSpace(concept)
	if concept == "" {
		return VocabReport{}, fmt.Errorf("concept is empty")
	}
	if _, err := os.Stat(i.VectorPath); os.IsNotExist(err) {
		return VocabReport{}, fmt.Errorf("vector search is not installed; run `roca vector install`")
	} else if err != nil {
		return VocabReport{}, fmt.Errorf("inspect vector database: %w", err)
	}
	store, err := openSQLite(i.VectorPath, true)
	if err != nil {
		return VocabReport{}, fmt.Errorf("open vector database: %w", err)
	}
	defer store.Close()
	_, model, dimensions, err := readIndexState(store)
	if err != nil {
		return VocabReport{}, fmt.Errorf("read vector index: %w; run `roca vector install`", err)
	}
	if model == "" || dimensions == 0 {
		return VocabReport{}, fmt.Errorf("vector index is not ready; run `roca vector install`")
	}
	censusDocuments, built := readCensusDocuments(store)
	if !built || censusDocuments == 0 {
		return VocabReport{}, fmt.Errorf("vector census is not built; run `roca vector ingest --delta`")
	}
	vectors, err := i.Embedder.Embed(ctx, model, []string{QueryPrefix + concept})
	if err != nil {
		return VocabReport{}, err
	}
	if len(vectors) != 1 || len(vectors[0]) != dimensions {
		return VocabReport{}, fmt.Errorf("query embedding has the wrong dimensions")
	}
	candidates, err := nearestVocab(ctx, store, vectorBlob(vectors[0]),
		vocabTopK+maxUnresolvedCandidates)
	if err != nil {
		return VocabReport{}, err
	}
	documents, err := i.vocabDocuments(ctx, candidates)
	if err != nil {
		return VocabReport{}, err
	}
	report := VocabReport{Concept: concept, TopK: vocabTopK, Hits: len(documents),
		HitsByKind: map[string]int{}, CensusDocuments: censusDocuments}
	for _, document := range documents {
		report.HitsByKind[document.kind]++
	}
	terms, err := rankVocabulary(documents, store, censusDocuments)
	if err != nil {
		return VocabReport{}, err
	}
	// An ingest invalidates the census before its first index mutation, so a
	// discovery that started beside one ranks a moving index against a stale
	// baseline. The totals it re-reads here are only unchanged when no ingest
	// intervened, and a refusal is cheaper than a quietly mixed report.
	if current, built := readCensusDocuments(store); !built || current != censusDocuments {
		return VocabReport{}, fmt.Errorf("vector census changed during discovery; run vocab again once ingest finishes")
	}
	report.Avenues = groupAvenues(terms)
	return report, nil
}

type vocabDocument struct {
	kind string
	text string
}

// vocabDocuments keeps the exchanges and thinking blocks among the nearest
// candidates, one per source, live-resolved, bounded by the same stale-index
// budget as Query.
func (i Index) vocabDocuments(ctx context.Context, candidates []neighbor) ([]vocabDocument, error) {
	var documents []vocabDocument
	seen := map[string]bool{}
	misses := 0
	for _, candidate := range candidates {
		if candidate.kind != "exchanges" && candidate.kind != "thinking_blocks" {
			continue
		}
		if seen[candidate.sourceID] {
			continue
		}
		seen[candidate.sourceID] = true
		body, err := i.Corpus.ResolveSource(ctx, candidate.kind, candidate.where)
		if err != nil {
			return nil, err
		}
		if body == "" {
			misses++
			if misses == maxUnresolvedCandidates {
				break
			}
			continue
		}
		documents = append(documents, vocabDocument{kind: candidate.kind, text: body})
		if len(documents) == vocabTopK {
			break
		}
	}
	return documents, nil
}

func nearestVocab(ctx context.Context, db *sql.DB, vector []byte, k int) ([]neighbor, error) {
	rows, err := db.QueryContext(ctx, `WITH eligible AS (
			SELECT c.source_kind,c.source_id,c.chunk_index,c.locator,
				vec_distance_cosine(e.embedding,?) AS distance
			FROM embeddings e
			JOIN chunks c ON c.id=e.rowid
			WHERE c.source_kind IN ('exchanges','thinking_blocks')
		), ranked AS (
			SELECT *,row_number() OVER (
				PARTITION BY source_kind,source_id
				ORDER BY distance,chunk_index
			) AS source_rank
			FROM eligible
		)
		SELECT source_kind,source_id,locator,distance
		FROM ranked
		WHERE source_rank=1
		ORDER BY distance,source_kind,source_id
		LIMIT ?`, vector, k)
	if err != nil {
		return nil, fmt.Errorf("search eligible vocabulary sources: %w", err)
	}
	defer rows.Close()
	var result []neighbor
	for rows.Next() {
		var item neighbor
		var raw string
		if err := rows.Scan(&item.kind, &item.sourceID, &raw, &item.distance); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &item.where); err != nil {
			return nil, fmt.Errorf("decode source locator: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// rankedTerm carries everything the grouping needs beside the score.
type rankedTerm struct {
	term       string
	localDocs  int
	globalDocs int64
	score      float64
	members    []bool
}

// rankVocabulary tokenizes the discovery set, censuses every candidate term
// against the global census, and orders by discriminative power.
func rankVocabulary(documents []vocabDocument, store *sql.DB, censusDocuments int64) ([]rankedTerm, error) {
	membership := map[string][]bool{}
	for index, document := range documents {
		for term := range termSet(vocabTerms(document.text)) {
			if membership[term] == nil {
				membership[term] = make([]bool, len(documents))
			}
			membership[term][index] = true
		}
	}
	var candidates []rankedTerm
	for term, members := range membership {
		localDocs := 0
		for _, present := range members {
			if present {
				localDocs++
			}
		}
		if localDocs >= vocabMinLocalDocs {
			candidates = append(candidates, rankedTerm{term: term, localDocs: localDocs, members: members})
		}
	}
	slices.SortFunc(candidates, func(a, b rankedTerm) int {
		return strings.Compare(a.term, b.term)
	})
	global, err := censusDocs(store, termsOf(candidates))
	if err != nil {
		return nil, err
	}
	for index := range candidates {
		candidates[index].globalDocs = global[candidates[index].term]
		candidates[index].score = logOdds(candidates[index].localDocs, len(documents),
			candidates[index].globalDocs, censusDocuments)
	}
	slices.SortFunc(candidates, func(a, b rankedTerm) int {
		if a.score != b.score {
			return compareDescending(a.score, b.score)
		}
		return strings.Compare(a.term, b.term)
	})
	// Only terms the discovery set concentrates survive: a negative log-odds is
	// the baseline reporting workshop floor, not an avenue.
	candidates = slices.DeleteFunc(candidates, func(term rankedTerm) bool { return term.score <= 0 })
	if len(candidates) > vocabAvenueLimit {
		candidates = candidates[:vocabAvenueLimit]
	}
	return candidates, nil
}

// groupAvenues clusters ranked terms by shared discovery documents. The walk
// order is the rank order, so the grouping is as reproducible as the ranking.
func groupAvenues(terms []rankedTerm) []VocabAvenue {
	var groups [][]int
	for index := range terms {
		best, affinity := -1, 0.0
		for avenue, members := range groups {
			if len(members) >= vocabAvenueCapacity {
				continue
			}
			for _, member := range members {
				overlap := jaccard(terms[index].members, terms[member].members)
				if overlap > affinity {
					best, affinity = avenue, overlap
				}
			}
		}
		if best >= 0 && affinity >= vocabJoinOverlap {
			groups[best] = append(groups[best], index)
			continue
		}
		groups = append(groups, []int{index})
	}
	avenues := make([]VocabAvenue, 0, len(groups))
	for _, members := range groups {
		avenue := VocabAvenue{Rank: len(avenues) + 1, Terms: make([]VocabTerm, 0, len(members))}
		for _, member := range members {
			avenue.Terms = append(avenue.Terms, VocabTerm{Term: terms[member].term,
				LocalDocs: terms[member].localDocs, GlobalDocs: terms[member].globalDocs,
				Score: terms[member].score})
		}
		avenues = append(avenues, avenue)
	}
	return avenues
}

func termsOf(terms []rankedTerm) []string {
	names := make([]string, 0, len(terms))
	for _, term := range terms {
		names = append(names, term.term)
	}
	return names
}

func compareDescending(a, b float64) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	default:
		return 0
	}
}

func jaccard(a, b []bool) float64 {
	intersection, union := 0, 0
	for index := range a {
		switch {
		case a[index] && b[index]:
			intersection++
			union++
		case a[index] || b[index]:
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// logOdds is the smoothed log-odds ratio of a term's local document share
// against its census share: positive when the discovery set concentrates the
// term, negative when the term is only the corpus's workshop floor.
func logOdds(localDocs, allLocal int, globalDocs, allGlobal int64) float64 {
	return math.Log((float64(localDocs)+vocabSmoothing)/(float64(allLocal-localDocs)+vocabSmoothing)) -
		math.Log((float64(globalDocs)+vocabSmoothing)/(float64(allGlobal-globalDocs)+vocabSmoothing))
}

// vocabTerms tokenizes with accent folding: lowercase, NFKD-decomposed, marks
// dropped, letters and digits kept as one alphabet, everything else a
// separator. Serialized keys and opaque identifiers are evidence plumbing,
// not vocabulary; single runes are not vocabulary either.
func vocabTerms(text string) []string {
	text = sessionJSONKeyFragment.ReplaceAllString(text, " ")
	text = structuralSessionToken.ReplaceAllString(text, " ")
	var terms []string
	var current []rune
	flush := func() {
		if len(current) > 1 {
			term := string(current)
			if !opaqueVocabTerm(term) {
				terms = append(terms, term)
			}
		}
		current = current[:0]
	}
	for _, symbol := range norm.NFKD.String(strings.ToLower(text)) {
		if unicode.Is(unicode.Mn, symbol) {
			continue
		}
		if unicode.IsLetter(symbol) || unicode.IsDigit(symbol) {
			current = append(current, symbol)
			continue
		}
		flush()
	}
	flush()
	return terms
}

func opaqueVocabTerm(term string) bool {
	runes := []rune(term)
	allDigits, allHex := true, len(runes) >= 8
	for _, symbol := range runes {
		if !unicode.IsDigit(symbol) {
			allDigits = false
		}
		if !(symbol >= '0' && symbol <= '9') && !(symbol >= 'a' && symbol <= 'f') {
			allHex = false
		}
	}
	return allHex || (allDigits && len(runes) >= 5)
}

func termSet(terms []string) map[string]struct{} {
	set := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		set[term] = struct{}{}
	}
	return set
}

// vocabCensus is the global term-frequency baseline, rebuilt from the same
// full walk that maintains the index.
type vocabCensus struct {
	documents int64
	terms     map[string]int64
}

// add counts one source row's distinct terms. Sessions are skipped on purpose:
// their walk projection carries serialized metadata, and that blob is exactly
// the contamination the embedding-text fix is removing, not a baseline.
func (c *vocabCensus) add(kind, text string) {
	if kind == "sessions" {
		return
	}
	distinct := termSet(vocabTerms(text))
	if len(distinct) == 0 {
		return
	}
	c.documents++
	for term := range distinct {
		c.terms[term]++
	}
}

func newVocabCensus() *vocabCensus {
	return &vocabCensus{terms: map[string]int64{}}
}

func invalidateCensus(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `DELETE FROM census_totals`)
	return err
}

// writeCensus replaces the stored census in one transaction, so a reader sees
// either the previous baseline or the new one, never a mix.
func writeCensus(ctx context.Context, db *sql.DB, census *vocabCensus) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM census`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM census_totals`); err != nil {
		return err
	}
	names := make([]string, 0, len(census.terms))
	for term := range census.terms {
		names = append(names, term)
	}
	slices.Sort(names)
	for start := 0; start < len(names); start += vocabCensusBatch {
		end := min(start+vocabCensusBatch, len(names))
		var statement strings.Builder
		statement.WriteString(`INSERT INTO census(term,docs) VALUES `)
		for index := start; index < end; index++ {
			if index > start {
				statement.WriteByte(',')
			}
			statement.WriteString(fmt.Sprintf("('%s',%d)", escapeSQLLiteral(names[index]), census.terms[names[index]]))
		}
		if _, err := tx.ExecContext(ctx, statement.String()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO census_totals(key,documents) VALUES ('documents',?)`,
		census.documents); err != nil {
		return err
	}
	return tx.Commit()
}

func escapeSQLLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// readCensusDocuments reports the census total, and whether a census exists
// at all. An index installed before the census arrived owes one delta ingest,
// and anything else that makes the row unreadable owes the same repair, so
// every failure is reported the same way.
func readCensusDocuments(db *sql.DB) (int64, bool) {
	var documents int64
	err := db.QueryRow(`SELECT documents FROM census_totals WHERE key='documents'`).Scan(&documents)
	if err != nil {
		return 0, false
	}
	return documents, true
}

// censusDocs looks up document frequencies for the terms the discovery set
// offered, in batches that keep one statement short.
func censusDocs(db *sql.DB, terms []string) (map[string]int64, error) {
	result := make(map[string]int64, len(terms))
	for start := 0; start < len(terms); start += vocabCensusBatch {
		end := min(start+vocabCensusBatch, len(terms))
		batch := terms[start:end]
		placeholders := strings.TrimRight(strings.Repeat("?,", len(batch)), ",")
		rows, err := db.Query(`SELECT term,docs FROM census WHERE term IN (`+placeholders+`)`,
			anyOf(batch)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var term string
			var docs int64
			if err := rows.Scan(&term, &docs); err != nil {
				rows.Close()
				return nil, err
			}
			result[term] = docs
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func anyOf(values []string) []any {
	arguments := make([]any, 0, len(values))
	for _, value := range values {
		arguments = append(arguments, value)
	}
	return arguments
}
