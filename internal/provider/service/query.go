package service

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

// Paths a question can leave by. v1 is model-only: every question is asked of
// the model (PathLLM), and when the model cannot answer the keyword rescue
// searches the FTS index with the question's own words (PathKeyword). There is
// no compiler path. A model can explicitly refuse an out-of-scope question;
// that is a legitimate result, distinct from an unavailable model or bad SQL.
const (
	PathLLM        = "model"
	PathKeyword    = "keyword"
	PathUnresolved = "unresolved"
	PathRefused    = "refused"
	PathAsk        = "ask"
)

// Match states. Honest zero rows are declared as such instead of dressed up as
// an answer.
const (
	MatchFound = "found"
	MatchEmpty = "empty"
	noMatches  = "no matches in memory for that search"
)

const (
	RetryGateRejection  = "gate_rejection"
	RetryExecutionError = "execution_error"
)

var errQueryTimeout = errors.New("the validated SQL exceeded the time limit")

// QueryRequest is a question and the budget it is answered with.
type QueryRequest struct {
	Question string
	Layer    string
	MaxChars int
	// SQLOnly returns the SQL the model generated without running it.
	SQLOnly bool
	// Databases is the explicit --databases selection: attached names, or "all".
	// Empty means the default scope (corpus plus core when corpus is attached).
	Databases []string
	// Progress and the interpretation hooks are presentation only. They are
	// ignored by machine callers and never enter the result envelope.
	Progress            func(QueryPhase)
	InterpretationStart func(bool, QueryResult)
	InterpretationDelta func(string)
}

type QueryPhase string

const (
	QueryPhaseSQL            QueryPhase = "sql"
	QueryPhaseExecution      QueryPhase = "execution"
	QueryPhaseInterpretation QueryPhase = "interpretation"
)

func progress(req QueryRequest, phase QueryPhase) {
	if req.Progress != nil {
		req.Progress(phase)
	}
}

// QueryResult is the complete answer: which path it left by, with what SQL, and
// from which version of the code.
//
// Provenance is not decoration: a poor result because the provider failed and
// a poor one because
// the model wrote bad SQL are fixed in different ways, and without the
// provenance the operator does not know which of the two they are looking at.
type QueryResult struct {
	Question string `json:"question"`
	Path     string `json:"path"`
	// Mode is set only by Explore. An ordinary query omits it, preserving the
	// query envelope while every investigation declares plain or deep mode.
	Mode     string             `json:"mode,omitempty"`
	SQL      string             `json:"sql,omitempty"`
	Columns  []string           `json:"columns,omitempty"`
	Rows     []map[string]any   `json:"rows,omitempty"`
	RowCount int                `json:"row_count"`
	Match    string             `json:"match,omitempty"`
	Search   *search.Provenance `json:"search,omitempty"`
	Message  string             `json:"message,omitempty"`
	// ClarificationRequired declares that no SQL was generated because the
	// question named a generic slot without supplying its referent.
	ClarificationRequired bool   `json:"clarification_required,omitempty"`
	MissingSlot           string `json:"missing_slot,omitempty"`
	// Databases declares the core and plugin stores made available to this
	// query. OmittedDatabases names relevant plugins beyond SQLite's attachment
	// limit; paths never enter either field.
	Databases        []string `json:"databases,omitempty"`
	OmittedDatabases []string `json:"omitted_databases,omitempty"`
	// UnusedDatabases names attached stores held back from this pass. The SQL
	// seat sees the names only; a later pass can add them when this one is empty
	// or the reading seat replies WIDEN.
	UnusedDatabases []string `json:"unused_databases,omitempty"`
	// Widened is true when this answer used the second SQL pass over the rest
	// of the attached inventory.
	Widened bool `json:"widened,omitempty"`

	// Engine and Model are the model path's provenance: which provider answered
	// and with which model. Without them a poor answer cannot be attributed, and
	// changing provider becomes a bet.
	Engine string `json:"sql_provider,omitempty"`
	Model  string `json:"sql_model,omitempty"`
	// ProviderNote says the providers ahead of the one that served were not
	// available. It is kept apart from Message on purpose: falling to the floor
	// is a fact about WHO WAS ASKED and the message is a fact about WHAT
	// ANSWERED, and writing one over the other produced an answer that said the
	// provider was unavailable while reporting that same provider as the engine.
	ProviderNote string `json:"sql_provider_note,omitempty"`
	// Degraded says what went wrong down the model path, with one of the
	// declared reasons. Absent means nothing went wrong.
	Degraded string `json:"degraded,omitempty"`
	// Retried says the keyword rescue is what answered.
	Retried bool `json:"retried,omitempty"`
	// RetriedSQL says the first model-authored candidate failed and the model
	// received one correction attempt with that verdict in hand. RetryType says
	// whether the strict gate or execution produced it.
	RetriedSQL bool   `json:"retried_sql,omitempty"`
	RetryType  string `json:"retry_type,omitempty"`
	// ModelSQL is what the model generated, whether or not it ran. It survives
	// the rescue answering over it, because without it a model that writes badly
	// cannot be told from a rescue that fired for another reason. Whether it ran
	// is what Degraded says.
	ModelSQL string `json:"model_sql,omitempty"`
	// FirstModelSQL preserves the untouched first answer when ModelSQL is the
	// corrected answer. RetryReason is the exact failure that bought the retry.
	FirstModelSQL string `json:"first_model_sql,omitempty"`
	RetryReason   string `json:"retry_reason,omitempty"`
	// Repaired names every deterministic repair applied before the strict gate.
	// ModelSQL remains the untouched output so the forgiveness is auditable.
	Repaired []string `json:"repaired,omitempty"`
	// FirstRepaired does the same for the rejected first answer.
	FirstRepaired []string `json:"first_repaired,omitempty"`
	// CleanedSQL is the repaired candidate the gate judged, kept even when the
	// gate rejected it. It is audit-only: the durable trace writes it as the
	// statement the model meant, and keeps ModelSQL beside it when they differ.
	CleanedSQL string `json:"-"`
	// QueryPlan is the rescue's plan: the term it searched for, which the
	// renderer uses to keep the match inside the excerpt.
	QueryPlan *query.Plan `json:"queryplan,omitempty"`
	// Providers is every provider tried, with why each one did or did not
	// serve.
	Providers []provider.Attempt `json:"providers,omitempty"`
	// Warnings are what the configuration said that this build did not
	// understand. They never take down a query.
	Warnings []string `json:"warnings,omitempty"`
	// LLMLatencyMS is what the model alone cost, apart from the total. It is
	// declared whenever its retry subset is, so that the share the correction
	// took is never a numerator without its denominator.
	LLMLatencyMS int64 `json:"sql_provider_latency_ms"`
	// SQLRetryProviderLatencyMS is the provider-reported subset spent on the
	// correction call, so retry cost is distinguishable from a first shot.
	SQLRetryProviderLatencyMS int64 `json:"sql_retry_provider_latency_ms"`
	// SQLInferenceMS, ExecutionMS and InterpretationMS are the three query
	// phases. Interpretation is populated by query --full and every explore.
	SQLInferenceMS      int64    `json:"sql_inference_ms"`
	SQLRetryInferenceMS int64    `json:"sql_retry_inference_ms"`
	ExecutionMS         int64    `json:"execution_ms"`
	InterpretationMS    int64    `json:"interpretation_ms"`
	Interpretation      string   `json:"interpretation,omitempty"`
	Terrain             *Terrain `json:"terrain,omitempty"`
	// InterpretEngine and InterpretModel are the second inference's own
	// provenance: which provider read the result rows. They differ from Engine
	// and Model on an installation that splits the two inferences, and that
	// difference is the claim "the rows never left this machine" made checkable.
	InterpretEngine string `json:"interpretation_provider,omitempty"`
	InterpretModel  string `json:"interpretation_model,omitempty"`
	// InterpretNote says the configured interpretation provider was not
	// available and the rows went to the provider that wrote the SQL instead. It
	// is kept apart from ProviderNote for the same reason that one is kept apart
	// from Message: they answer different questions about the same answer.
	InterpretNote string `json:"interpretation_provider_note,omitempty"`
	// ProviderError preserves the provider's own failure text. File logging
	// applies credential redaction before it reaches disk.
	ProviderError string `json:"provider_error,omitempty"`

	LatencyMS int64  `json:"latency_ms"`
	Version   string `json:"version"`
	SourceSHA string `json:"source_sha"`
}

// found records the rows an answer came back with. Zero of them are declared as
// zero and never dressed up as an answer, and a message the caller has
// already written is not written over: down the model path that message is what
// says why the rescue is the one answering.
//
// Rows are recorded exactly as SQL returned them. Model-authored SELECTs own
// their row count and order; silently rewriting either would make the displayed
// SQL disagree with its result.
func (r *QueryResult) found(columns []string, rows []map[string]any) {
	r.Columns, r.Rows, r.RowCount = columns, rows, len(rows)
	r.Match = MatchFound
	if len(rows) == 0 {
		r.Match = MatchEmpty
		r.Message = cmp.Or(r.Message, noMatches)
	}
}

// foundSearch applies the lexical search presentation policy before recording
// rows. That policy never touches model-authored SQL results.
func (r *QueryResult) foundSearch(columns []string, rows []map[string]any) {
	r.found(columns, dedupRows(r.Question, columns, rows))
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
// schema; that SQL always passes the SQLite-backed read-only gate. Whatever
// fails from there degrades to the keyword rescue instead of failing, and it
// says which of the declared things went wrong. The fragility of a provider
// never takes down a query.
func (s *Service) Query(ctx context.Context, req QueryRequest) (res QueryResult, err error) {
	start := time.Now()
	if err := query.ValidateQuestion(req.Question, !s.opts.DisableStrictInput); err != nil {
		return res, err
	}
	res = QueryResult{
		Question:  req.Question,
		Version:   s.opts.Version,
		SourceSHA: s.opts.Commit,
		// What the configuration said that this build did not understand travels
		// with every answer: a question is exactly where an operator would
		// otherwise never find out that half their [models] section is being
		// ignored.
		Warnings: slices.Clone(s.opts.Providers.Warnings),
	}
	defer func() { res.LatencyMS = time.Since(start).Milliseconds() }()
	if !s.opts.DisableMissingReferentAsk {
		if missing, found := query.DetectMissingReferent(req.Question); found {
			res.Path = PathAsk
			res.Message = missing.Ask
			res.ClarificationRequired = true
			res.MissingSlot = missing.Slot
			return res, nil
		}
	}
	if _, err := s.ensureSchema(ctx); err != nil {
		return res, err
	}
	inventory := s.inventoryRoute(ctx)
	defer inventory.closeOnDemand()
	route, err := questionRoute(req.Databases, inventory)
	if err != nil {
		return res, err
	}
	if s.pluginsActive() {
		res.Databases = route.consulted()
	}
	res.OmittedDatabases = route.omittedSources()
	res.UnusedDatabases = route.unusedNames(inventory)
	res.Warnings = append(res.Warnings, route.warnings...)
	res, err = s.llmStage(ctx, req, res, route)
	if err != nil {
		return res, err
	}
	if !req.SQLOnly && insufficientAnswer(res) && route.canWiden(inventory) {
		widened := inventory
		res = beginWidenedPass(res, widened)
		res, err = s.llmStage(ctx, req, res, widened)
		if err != nil {
			return res, err
		}
		res.Widened = true
		res.Databases = widened.consulted()
		res.UnusedDatabases = nil
	}
	if len(route.databases) > 0 && !res.Widened && res.Path == PathKeyword &&
		!slices.Contains(res.Columns, plugin.ProvenanceColumn) && route.includeCore {
		res.Columns, res.Rows = ensureDatabaseColumn(res.Columns, res.Rows, "core")
	}
	return res, err
}

func beginWidenedPass(first QueryResult, route pluginRoute) QueryResult {
	return QueryResult{
		Question:                  first.Question,
		Databases:                 route.consulted(),
		OmittedDatabases:          route.omittedSources(),
		Widened:                   true,
		RetriedSQL:                first.RetriedSQL,
		RetryType:                 first.RetryType,
		FirstModelSQL:             first.FirstModelSQL,
		RetryReason:               first.RetryReason,
		FirstRepaired:             slices.Clone(first.FirstRepaired),
		Providers:                 slices.Clone(first.Providers),
		Warnings:                  slices.Clone(first.Warnings),
		LLMLatencyMS:              first.LLMLatencyMS,
		SQLRetryProviderLatencyMS: first.SQLRetryProviderLatencyMS,
		SQLInferenceMS:            first.SQLInferenceMS,
		SQLRetryInferenceMS:       first.SQLRetryInferenceMS,
		ExecutionMS:               first.ExecutionMS,
		Version:                   first.Version,
		SourceSHA:                 first.SourceSHA,
	}
}

func MergeWidenedResult(first, widened QueryResult) QueryResult {
	widened.Widened = true
	widened.Providers = append(slices.Clone(first.Providers), widened.Providers...)
	widened.LLMLatencyMS += first.LLMLatencyMS
	widened.SQLRetryProviderLatencyMS += first.SQLRetryProviderLatencyMS
	widened.SQLInferenceMS += first.SQLInferenceMS
	widened.SQLRetryInferenceMS += first.SQLRetryInferenceMS
	widened.ExecutionMS += first.ExecutionMS
	widened.LatencyMS += first.LatencyMS
	if first.RetriedSQL && !widened.RetriedSQL {
		widened.RetriedSQL = true
		widened.RetryType = first.RetryType
		widened.FirstModelSQL = first.FirstModelSQL
		widened.RetryReason = first.RetryReason
		widened.FirstRepaired = slices.Clone(first.FirstRepaired)
	}
	return widened
}

// ExecRequest is a SELECT the caller wants to run as it is. It is the natural
// companion of `playground --sql-only`.
type ExecRequest struct {
	SQL      string
	MaxChars int
}

// ExecResult is what that SELECT returned, with the SQL that actually ran.
type ExecResult struct {
	SQL              string           `json:"sql"`
	Columns          []string         `json:"columns,omitempty"`
	Rows             []map[string]any `json:"rows,omitempty"`
	RowCount         int              `json:"row_count"`
	Databases        []string         `json:"databases,omitempty"`
	OmittedDatabases []string         `json:"omitted_databases,omitempty"`
	LatencyMS        int64            `json:"latency_ms"`
	Version          string           `json:"version"`
	SourceSHA        string           `json:"source_sha"`
}

// Exec validates and runs a SELECT. What does not pass the gate does not touch
// the database, and what does pass runs over a connection on which the engine
// itself rejects any write.
func (s *Service) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	start := time.Now()
	if _, err := s.ensureSchema(ctx); err != nil {
		return ExecResult{}, err
	}
	route := s.pluginsForSQL(ctx, req.SQL)
	defer route.closeOnDemand()
	if len(route.omitted) > 0 {
		return ExecResult{}, logfile.Typed(fmt.Errorf(
			"the SELECT references more than SQLite's %d attached databases; split the query (omitted: %s)",
			plugin.MaxAttached, strings.Join(route.omittedSources(), ", ")), DegradedInvalidSQL)
	}
	gate, closeGate, err := s.gateFor(route.includeCore, route.databases)
	if err != nil {
		return ExecResult{}, err
	}
	defer closeGate()
	// The stage that failed is only knowable here, and it is the same
	// distinction the degraded answers already declare: what the gate refused
	// and what the engine could not run are two different fixes.
	validated, err := gate.Validate(req.SQL)
	if err != nil {
		return ExecResult{}, logfile.Typed(err, DegradedInvalidSQL)
	}
	columns, rows, err := s.executeWithPlugins(ctx, validated, "", req.MaxChars, route.databases)
	if err != nil {
		degraded := DegradedExecution
		if errors.Is(err, errQueryTimeout) {
			degraded = DegradedTimeout
		}
		return ExecResult{}, logfile.Typed(err, degraded)
	}
	result := ExecResult{
		SQL:       validated,
		Columns:   columns,
		Rows:      rows,
		RowCount:  len(rows),
		LatencyMS: time.Since(start).Milliseconds(),
		Version:   s.opts.Version,
		SourceSHA: s.opts.Commit,
	}
	if s.pluginsActive() {
		result.Databases = route.consulted()
	}
	return result, nil
}

// execute runs the validated SELECT and normalizes the rows into maps keyed by
// column name, which is what both surfaces render.
func (s *Service) execute(ctx context.Context, stmt, term string, maxChars int) ([]string, []map[string]any, error) {
	return s.executeWithPlugins(ctx, stmt, term, maxChars, nil)
}

// queryExecutionBudget is how long validated SQL may run, and whether it is
// bounded at all. A positive budget is the one that was asked for; an explicit
// zero is the operator removing the bound on purpose; anything else, including
// a value no statement could meet, falls back to the shipped default.
func (s *Service) queryExecutionBudget() (time.Duration, bool) {
	if s.opts.QueryTimeout > 0 {
		return s.opts.QueryTimeout, true
	}
	if s.opts.QueryTimeoutSet && s.opts.QueryTimeout == 0 {
		return 0, false
	}
	return DefaultQueryTimeout, true
}

func executionError(parent, queryCtx context.Context, timeout time.Duration, err error) error {
	if parent.Err() == nil && errors.Is(queryCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w after %s", errQueryTimeout, timeout)
	}
	return fmt.Errorf("run the validated query: %w", err)
}

// ScanRows turns any result set into its column names and its rows of named
// values under the text budget. The query cascade, health diagnosis and
// in-memory cross queries share it, so unexpected column types are handled in
// one place.
func ScanRows(rows *sql.Rows, maxChars int, term string) ([]string, []map[string]any, error) {
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
