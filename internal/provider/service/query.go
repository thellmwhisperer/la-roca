package service

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

// Paths a question can leave by. v1 is model-only: every question is asked of
// the model (PathLLM), and when the model cannot answer the keyword rescue
// searches the FTS index with the question's own words (PathKeyword). There is
// no compiler path and no refusal: a question the model cannot answer is
// declared unresolved, not turned away at a gate.
const (
	PathLLM        = "llm_fallback"
	PathKeyword    = "keyword_fallback"
	PathUnresolved = "unresolved"
)

// Match states. Honest zero rows are declared as such instead of dressed up as
// an answer.
const (
	MatchFound = "found"
	MatchEmpty = "empty"
	noMatches  = "no matches in memory for that search"
)

// QueryRequest is a question and the budget it is answered with.
type QueryRequest struct {
	Question string
	Layer    string
	MaxChars int
	// SQLOnly returns the SQL the model generated without running it.
	SQLOnly bool
}

// SearchRequest runs the search layer directly, without the model. It is what
// the golden bench measures the index against, and it is not a path a question
// normally takes: the model is always asked first.
type SearchRequest struct {
	Question string
	// Method forces one search method (like or fts); empty lets the engine
	// choose the index and fall to the LIKE floor only when there is none.
	Method   string
	Layer    string
	MaxChars int
}

// QueryResult is the complete answer: which path it left by, with what SQL, and
// from which version of the code.
//
// That provenance is a product requirement (PRD, requirement C2) and not
// decoration: a poor result because the provider failed and a poor one because
// the model wrote bad SQL are fixed in different ways, and without the
// provenance the operator does not know which of the two they are looking at.
type QueryResult struct {
	Question string             `json:"question"`
	Path     string             `json:"path"`
	SQL      string             `json:"sql,omitempty"`
	Columns  []string           `json:"columns,omitempty"`
	Rows     []map[string]any   `json:"rows,omitempty"`
	RowCount int                `json:"row_count"`
	Match    string             `json:"match,omitempty"`
	Search   *search.Provenance `json:"search,omitempty"`
	Message  string             `json:"message,omitempty"`

	// Engine and Model are the model path's provenance: which provider answered
	// and with which model. Without them a poor answer cannot be attributed, and
	// changing provider becomes a bet.
	Engine string `json:"engine,omitempty"`
	Model  string `json:"model,omitempty"`
	// ProviderNote says the providers ahead of the one that served were not
	// available. It is kept apart from Message on purpose: falling to the floor
	// is a fact about WHO WAS ASKED and the message is a fact about WHAT
	// ANSWERED, and writing one over the other produced an answer that said the
	// provider was unavailable while reporting that same provider as the engine.
	ProviderNote string `json:"provider_note,omitempty"`
	// Degraded says what went wrong down the model path, with one of the
	// declared reasons. Absent means nothing went wrong.
	Degraded string `json:"degraded,omitempty"`
	// Retried says the keyword rescue is what answered.
	Retried bool `json:"retried,omitempty"`
	// ModelSQL is what the model generated, whether or not it ran. It survives
	// the rescue answering over it, because without it a model that writes badly
	// cannot be told from a rescue that fired for another reason. Whether it ran
	// is what Degraded says.
	ModelSQL string `json:"model_sql,omitempty"`
	// QueryPlan is the rescue's plan: the term it searched for, which the
	// renderer uses to keep the match inside the excerpt.
	QueryPlan *query.Plan `json:"queryplan,omitempty"`
	// Providers is every provider tried, with why each one did or did not
	// serve.
	Providers []provider.Attempt `json:"providers,omitempty"`
	// Warnings are what the configuration said that this build did not
	// understand. They never take down a query.
	Warnings []string `json:"warnings,omitempty"`
	// LLMLatencyMS is what the model alone cost, apart from the total.
	LLMLatencyMS int64 `json:"llm_latency_ms,omitempty"`

	LatencyMS int64  `json:"latency_ms"`
	Version   string `json:"version"`
	SourceSHA string `json:"source_sha"`
}

// found records the rows an answer came back with. Zero of them are declared as
// zero and never dressed up as an answer (F04-10), and a message the caller has
// already written is not written over: down the model path that message is what
// says why the rescue is the one answering.
//
// Search results are deduplicated here, at the point rows are recorded: the
// adopted corpus carries genuine duplicate rows (identical content, identical
// timestamp, consecutive ids) and the term search returns each one. Only a
// result set that carries a source and a text is touched — a count of five is
// not five copies of one thing — and the database is never mutated.
func (r *QueryResult) found(columns []string, rows []map[string]any) {
	rows = dedupRows(r.Question, columns, rows)
	r.Columns, r.Rows, r.RowCount = columns, rows, len(rows)
	r.Match = MatchFound
	if len(rows) == 0 {
		r.Match = MatchEmpty
		r.Message = cmp.Or(r.Message, noMatches)
	}
}

// dedupRows collapses search-result rows whose source and text are identical,
// keeping the best-ranked: results arrive ordered by rank (FTS) or by date (the
// LIKE floor), so the first of a set of twins is the one to keep.
func dedupRows(question string, columns []string, rows []map[string]any) []map[string]any {
	if !slices.Contains(columns, "source") || !slices.Contains(columns, "text") {
		return rows
	}
	seen := make(map[string]bool, len(rows))
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		text, ok := row["text"].(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		key := fmt.Sprintf("%v\x00%v", row["source"], row["text"])
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, row)
	}
	slices.SortStableFunc(out, func(a, b map[string]any) int {
		return relevanceRank(question, a) - relevanceRank(question, b)
	})
	return out
}

func relevanceRank(question string, row map[string]any) int {
	source := fmt.Sprint(row["source"])
	rank := 10
	if source == "memory" {
		rank = 0
	} else if strings.Contains(source, "thinking") {
		rank = 20
	}
	text := strings.ToLower(query.Fold(fmt.Sprint(row["text"])))
	wanted := query.Normalize(question)
	if wanted != "" && (strings.Contains(text, `"`+wanted+`"`) ||
		strings.Contains(text, "“"+wanted+"”")) {
		rank++
	}
	return rank
}

// unresolved declares a question no model is going to be asked about. It is not
// an error: with no provider configured there is nobody to ask, and the honest
// answer is to say it is not known.
func (r *QueryResult) unresolved(andAlso string) {
	r.Path = PathUnresolved
	r.Match = MatchEmpty
	r.Message = "I cannot answer this: there is no model to ask" + andAlso
}

// Query answers a question through the model.
//
// Every question goes to the model, which generates SQL over the SQLite + FTS5
// schema; that SQL always passes the two-halved gate. Whatever fails from there
// degrades to the keyword rescue instead of failing, and it says which of the
// declared things went wrong. The fragility of a provider never takes down a
// query.
func (s *Service) Query(ctx context.Context, req QueryRequest) (res QueryResult, err error) {
	start := time.Now()
	res = QueryResult{
		Question:  req.Question,
		Version:   s.opts.Version,
		SourceSHA: s.opts.Commit,
		// What the configuration said that this build did not understand travels
		// with every answer: a question is exactly where an operator would
		// otherwise never find out that half their [models] section is being
		// ignored (F07-07).
		Warnings: s.opts.Providers.Warnings,
	}
	defer func() { res.LatencyMS = time.Since(start).Milliseconds() }()
	return s.llmStage(ctx, req, res)
}

// Search runs the search layer directly over the question's own words, with no
// model in the path. It is the golden bench's measure of the index and not a
// path a question normally takes.
func (s *Service) Search(ctx context.Context, req SearchRequest) (QueryResult, error) {
	start := time.Now()
	res := QueryResult{
		Question: req.Question, Version: s.opts.Version, SourceSHA: s.opts.Commit,
	}
	defer func() { res.LatencyMS = time.Since(start).Milliseconds() }()

	plan := query.Plan{
		Template: query.TemplateSearchByTerm,
		Term:     query.SearchTerm(req.Question),
		Layer:    req.Layer,
		Limit:    10,
	}
	res.QueryPlan = &plan
	columns, rows, stmt, provenance, err := s.searchByTerm(ctx, plan, req.Method, req.MaxChars, false)
	if err != nil {
		return res, err
	}
	res.SQL, res.Search = stmt, provenance
	res.found(columns, rows)
	return res, nil
}

// ExecRequest is a SELECT the caller wants to run as it is. It is the natural
// companion of `query --sql-only`.
type ExecRequest struct {
	SQL      string
	MaxChars int
}

// ExecResult is what that SELECT returned, with the SQL that actually ran.
type ExecResult struct {
	SQL       string           `json:"sql"`
	Columns   []string         `json:"columns,omitempty"`
	Rows      []map[string]any `json:"rows,omitempty"`
	RowCount  int              `json:"row_count"`
	LatencyMS int64            `json:"latency_ms"`
	Version   string           `json:"version"`
	SourceSHA string           `json:"source_sha"`
}

// Exec validates and runs a SELECT. What does not pass the gate does not touch
// the database, and what does pass runs over a connection on which the engine
// itself rejects any write.
func (s *Service) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	start := time.Now()
	gate, err := s.theGate()
	if err != nil {
		return ExecResult{}, err
	}
	validated, err := gate.Validate(req.SQL)
	if err != nil {
		return ExecResult{}, err
	}
	columns, rows, err := s.execute(ctx, validated, "", req.MaxChars)
	if err != nil {
		return ExecResult{}, err
	}
	return ExecResult{
		SQL:       validated,
		Columns:   columns,
		Rows:      rows,
		RowCount:  len(rows),
		LatencyMS: time.Since(start).Milliseconds(),
		Version:   s.opts.Version,
		SourceSHA: s.opts.Commit,
	}, nil
}

// execute runs the validated SELECT and normalizes the rows into maps keyed by
// column name, which is what both surfaces render.
func (s *Service) execute(ctx context.Context, stmt, term string, maxChars int) ([]string, []map[string]any, error) {
	reader, err := s.db.ReadOnly()
	if err != nil {
		return nil, nil, err
	}
	rows, err := reader.QueryContext(ctx, stmt)
	if err != nil {
		return nil, nil, fmt.Errorf("run the validated query: %w", err)
	}
	defer rows.Close()
	return scanRows(rows, maxChars, term)
}

// scanRows turns any result set into its column names and its rows of named
// values under the text budget. The query cascade and the health diagnosis both
// read through it, so a column of an unexpected type is handled in one place.
func scanRows(rows *sql.Rows, maxChars int, term string) ([]string, []map[string]any, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var result []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, nil, err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = values[i]
			switch text := values[i].(type) {
			case []byte:
				row[column] = truncate(string(text), maxChars, term)
			case string:
				row[column] = truncate(text, maxChars, term)
			}
		}
		result = append(result, row)
	}
	return columns, result, rows.Err()
}
