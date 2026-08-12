package service

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"
	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

// Why an answer down the model path is degraded. They are declared reasons and
// they travel in the answer, because a poor result with a provider that failed
// and one with a provider that answered nonsense are fixed in different ways.
const (
	// DegradedUnavailable: no provider of the order was available.
	DegradedUnavailable = "model_unavailable"
	// DegradedLLMError: a provider said it was available and then failed.
	DegradedLLMError = "model_error"
	// DegradedInvalidSQL: the model answered and the gate rejected it.
	DegradedInvalidSQL = "invalid_sql"
	// DegradedExecution: the SQL passed the gate and blew up when it ran.
	DegradedExecution = "sql_execution_error"
)

// IsDegradedFailure is the one success contract shared by CLI exit codes and
// MCP tool results. A rescue may still carry useful rows, but these modes mean
// the model-backed operation failed.
func IsDegradedFailure(mode string) bool {
	return mode == DegradedUnavailable || mode == DegradedLLMError ||
		mode == DegradedInvalidSQL || mode == DegradedExecution
}

// retriesOnRejection is how many extra attempts a rejected query buys.
//
// One, and the number is the whole design. Measured against real qwen3.5:4b the
// first SQL is often invalid in a way the engine describes exactly ("no such
// column: source_agent", "misuse of aggregate: MAX()"), and a model that is
// shown that error usually fixes it at once. A model that does not fix it with
// the error in front of it will not fix it on the fifth try either, and every
// try costs seconds of the operator's time.
const retriesOnRejection = 1

// correction is what is handed back to the model after a rejection: the
// engine's own verdict and the order to answer with SQL again.
func correction(rejection error) string {
	return "That query was rejected before running, by the same SQLite engine that " +
		"would have run it:\n\n" + rejection.Error() + "\n\n" +
		"Fix it against the schema you were given. Remember that a table has only the " +
		"columns listed under its own name, and that a column of another table has to be " +
		"reached with a JOIN. Respond ONLY with the corrected SQL query."
}

// llmStage is stage 4 of the cascade, with stage 5 behind it.
//
// The order of what happens here is the contract and not an implementation
// detail:
//
//  1. A provider is chosen by availability. What is not available does not get
//     asked, and why it was not is recorded. In the factory order only, a local
//     CLI whose first real request disproves its session fails forward.
//  2. The model generates SQL and that SQL ALWAYS goes through the two-halved
//     gate. A model is not above the gate: if it were, "everything that runs has
//     been validated" would stop being true.
//  3. Whatever fails from here on degrades to the keyword rescue instead of
//     failing, and it says which of the four things went wrong. The fragility of
//     a provider never takes down a query.
//
// Configured orders never retry a provider failure with the next provider. The
// factory local-CLI exception is declared in the attempts and applies only to
// the first request, before any SQL answer exists.
func (s *Service) llmStage(ctx context.Context, req QueryRequest, res QueryResult) (QueryResult, error) {
	progress(req, QueryPhaseSQL)
	cascade := s.opts.Providers

	if cascade.Disabled || len(cascade.Providers) == 0 {
		// The operator turned the model off, or this installation has none
		// configured. It is not a failure and it is not dressed up as one.
		res.unresolved(", and there is no model configured to try")
		return res, nil
	}

	chosen, attempts := cascade.Pick(ctx)
	res.Providers = attempts

	if chosen == nil {
		// The failure names which providers were tried, why each one
		// failed and the exact command that fixes it. The rescue still runs,
		// because rows the operator can use are worth more than a bare error;
		// but the exit is a failure all the same, because the question needed a
		// model and there was none. Answering 0 with a code of success would be
		// saying the machine did what was asked of it.
		return s.rescue(ctx, req, res, DegradedUnavailable,
			"no model is available and this question needs one.\n"+tried(attempts)), nil
	}
	res.Engine = chosen.Name()
	res.Model = chosen.ModelID()
	// The fall is declared and nothing is asked of the operator. It goes
	// in its own field so that whatever happens to the answer afterwards cannot
	// overwrite it, nor be mistaken for it.
	res.ProviderNote = noteAboutTheFall(chosen, attempts)

	gate, err := s.theGate()
	if err != nil {
		return res, err
	}

	prompt := s.sqlPrompt(req.Layer)
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: prompt},
		{Role: provider.RoleUser, Content: req.Question},
	}

	var validated string
	var rejection error
	for attempt := 0; attempt <= retriesOnRejection; attempt++ {
		var answer provider.ChatResponse
		for {
			inferenceStart := time.Now()
			answer, err = cascade.Chat(ctx, chosen, provider.ChatRequest{Messages: messages})
			res.SQLInferenceMS += time.Since(inferenceStart).Milliseconds()
			if err == nil {
				break
			}
			res.ProviderError = err.Error()
			credential, localCLI := chosen.(interface{ ExternalCredential() bool })
			if attempt != 0 || !cascade.FactoryDefault || !localCLI || !credential.ExternalCredential() {
				res.Providers = cascade.CompleteDiagnostics(res.Providers)
				return s.rescue(ctx, req, res, DegradedLLMError,
					fmt.Sprintf("%s could not answer: %v\n%s", chosen.Name(), err, tried(res.Providers))), nil
			}
			res.Providers[len(res.Providers)-1].Ready = false
			res.Providers[len(res.Providers)-1].Reason = err.Error()
			res.Providers[len(res.Providers)-1].Action =
				"verify the existing local CLI session with `roca login " + chosen.Name() + "`"
			next, further := cascade.PickAfter(ctx, chosen.Name())
			res.Providers = append(res.Providers, further...)
			if next == nil {
				return s.rescue(ctx, req, res, DegradedLLMError,
					"no factory-default model could answer.\n"+tried(res.Providers)), nil
			}
			chosen = next
			res.Engine, res.Model = chosen.Name(), chosen.ModelID()
			res.ProviderNote = noteAboutTheFall(chosen, res.Providers)
		}
		res.LLMLatencyMS += answer.LatencyMS
		// The adapter hands back raw model output; this stage asked for SQL, so
		// this stage shapes it: fence extraction, deloop, reasoning strip.
		sql := provider.Clean(answer.Content)
		// What the model wrote travels whether or not it runs, and it is not
		// lost when the rescue answers over it.
		res.ModelSQL = sql

		validated, rejection = gate.Validate(sql)
		if rejection == nil {
			// Defense in depth behind the prompt: bare LIKE '%term%' on a text
			// column is the substring disease (Ana → ganancia). Reject with a
			// retry hint that points at FTS; do not rewrite the SQL.
			if hint := query.SubstringLikeRejection(validated); hint != "" {
				rejection = fmt.Errorf("%s", hint)
			} else {
				break
			}
		}
		if attempt == retriesOnRejection {
			return s.rescue(ctx, req, res, DegradedInvalidSQL,
				fmt.Sprintf("the SQL %s generated does not pass the gate: %v",
					chosen.Name(), rejection)), nil
		}
		// The engine said exactly what is wrong. Handing that back is not a
		// repair invented here: it is the verdict of the same engine that would
		// have run the query, and it is the one piece of information that fixes
		// it.
		messages = append(messages,
			provider.Message{Role: provider.RoleAssistant, Content: sql},
			provider.Message{Role: provider.RoleUser, Content: correction(rejection)})
	}
	res.SQL = validated

	if req.SQLOnly {
		return res, nil
	}

	term := query.SearchTerm(req.Question)
	progress(req, QueryPhaseExecution)
	executionStart := time.Now()
	columns, rows, err := s.execute(ctx, validated, term, req.MaxChars)
	res.ExecutionMS += time.Since(executionStart).Milliseconds()
	if err != nil {
		return s.rescue(ctx, req, res, DegradedExecution,
			fmt.Sprintf("the validated SQL failed when it ran: %v", err)), nil
	}
	if len(rows) == 0 {
		// Zero rows down the model path is not an answer yet: the rescue looks
		// with the operator's own words before declaring there is nothing. It is
		// not a degradation, so it carries no degraded reason; but it IS a
		// different answer from the one asked for, and it says so.
		return s.rescue(ctx, req, res, "",
			fmt.Sprintf("nothing relevant was found by the plan from %s (tried: %s)",
				chosen.Name(), validated)), nil
	}

	if sqlgate.IsRowCount(validated) {
		res.Message = "Counted database rows matching the question's terms, not distinct events."
	}
	res.Path = PathLLM
	res.found(columns, rows)
	return res, nil
}

// Interpretation is what the second inference answered and who answered it.
//
// The provenance is not decoration here either: an installation that splits the
// two inferences does it so the rows stay on one machine, and an answer that
// does not say which provider read them cannot be checked against that claim.
type Interpretation struct {
	Text string
	// Engine and Model are the provider that read the rows and the model it
	// read them with.
	Engine string
	Model  string
	// Note is the declared fall: the configured interpretation provider was not
	// available and the rows went to the provider that wrote the SQL instead.
	// Empty means the rows went where the configuration said they would.
	Note string
}

// Interpret is the second inference call of a query: the first turned the
// question into SQL, this one turns that SQL's rows into a natural-language
// answer in the question's language. The rows, capped at ten, travel in the
// prompt. Whatever goes wrong is an error the caller falls back from, never a
// query that fails: the row renderer is the floor, and the prose is what sits
// on top of it when a model answers.
//
// Who is asked is the privacy decision of the whole product. With an
// interpretation order configured and available, the rows go there and nowhere
// else, so the machine that wrote the SQL never sees the data it selected. With
// none configured, a caller carrying SQL provenance reuses that provider; other
// callers ask the same order again.
func (s *Service) Interpret(ctx context.Context, question string,
	columns []string, rows []map[string]any,
	sqlInference time.Duration) (Interpretation, error) {
	return s.InterpretStream(ctx, question, columns, rows, sqlInference, "", nil, nil)
}

// InterpretStream is Interpret with live prose callbacks. Streaming is used
// only when the caller asks for deltas and the chosen provider supports it;
// machine callers and buffered providers keep the ordinary complete response.
func (s *Service) InterpretStream(ctx context.Context, question string,
	columns []string, rows []map[string]any, sqlInference time.Duration,
	sqlProvider string, onStart func(bool), onDelta func(string)) (Interpretation, error) {

	cascade, chosen, note, err := s.interpreter(ctx, sqlProvider)
	if err != nil {
		return Interpretation{}, err
	}
	answered := Interpretation{Engine: chosen.Name(), Model: chosen.ModelID(), Note: note}
	var b strings.Builder
	b.WriteString("You are La Roca. Question: ")
	b.WriteString(question)
	b.WriteString(". Results:\n")
	b.WriteString(strings.Join(columns, ", "))
	b.WriteByte('\n')
	limited := rows
	if len(rows) > maxRowsToInterpret {
		limited = rows[:maxRowsToInterpret]
		fmt.Fprintf(&b, "Showing %d of %d rows; the remaining rows were omitted.\n",
			len(limited), len(rows))
	}
	for _, row := range limited {
		values := make([]string, len(columns))
		for i, column := range columns {
			values[i] = truncate(fmt.Sprint(row[column]), interpretationFieldBudget, "")
		}
		b.WriteString(strings.Join(values, ", "))
		b.WriteByte('\n')
	}
	b.WriteString("Use only these results, never general knowledge. If the results do not support the question, say so plainly before anything else. A requested style changes delivery only and never licenses invention. Answer in the same language as the question. Write calm, terminal-friendly prose: paragraphs and simple dashes only. Do not use headings or tables.")
	if cascade.Timeout <= 0 {
		if timed, ok := chosen.(interface{ RequestTimeout() time.Duration }); ok {
			cascade.Timeout = timed.RequestTimeout()
		}
	}
	cascade.Timeout = interpretationTimeout(cascade.Timeout, sqlInference)
	request := provider.ChatRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: b.String()}},
	}
	_, nativeStream := chosen.(provider.StreamingProvider)
	stream := onDelta != nil && nativeStream
	if onStart != nil {
		onStart(stream)
	}
	var answer provider.ChatResponse
	if stream {
		answer, err = cascade.ChatStream(ctx, chosen, request, onDelta)
	} else {
		answer, err = cascade.Chat(ctx, chosen, request)
	}
	if err != nil {
		return Interpretation{}, err
	}
	// Prose keeps its fences and its punctuation; only the reasoning goes.
	answered.Text = provider.CleanProse(answer.Content)
	return answered, nil
}

// interpreter decides who reads the rows: the configured interpretation
// provider when it is available, and the provider that wrote the SQL otherwise,
// with the fall declared. The cascade comes back with the chosen provider
// because the budget travels in it, and asking one provider under another's
// budget is how a local model gets a frontier model's timeout.
func (s *Service) interpreter(ctx context.Context, sqlProvider string) (provider.Cascade, provider.Provider, string, error) {
	main := s.opts.Providers
	var note string
	if split := s.opts.Interpreters; len(split.Providers) > 0 {
		chosen, attempts := split.Pick(ctx)
		if chosen != nil {
			return split, chosen, "", nil
		}
		note = "the interpretation provider was not available (" + reasonsOf(attempts) + ")"
	} else if main.FactoryDefault && sqlProvider != "" {
		for _, chosen := range main.Providers {
			if chosen.Name() == sqlProvider {
				return main, chosen, "", nil
			}
		}
	}
	chosen, err := pickOrFail(ctx, main)
	if err != nil {
		return main, nil, "", err
	}
	if note != "" {
		note += ": the rows were read by " + chosen.Name()
	}
	return main, chosen, note, nil
}

// pickOrFail is the main order asked for someone to read the rows, with the two
// refusals told apart: an installation with no model configured and one whose
// models are all down are fixed differently.
func pickOrFail(ctx context.Context, cascade provider.Cascade) (provider.Provider, error) {
	if cascade.Disabled || len(cascade.Providers) == 0 {
		return nil, fmt.Errorf("no model is configured to interpret the rows")
	}
	chosen, _ := cascade.Pick(ctx)
	if chosen == nil {
		return nil, fmt.Errorf("no model is available to interpret the rows")
	}
	return chosen, nil
}

// maxRowsToInterpret caps how many rows the second call hands the model, so a
// large result set does not blow the context for an answer that summarizes it.
const maxRowsToInterpret = 10

// interpretationFieldBudget keeps the prose prompt materially smaller than
// the evidence returned to the caller. The complete, caller-selected row
// budget remains available in the rows printed below the summary.
const interpretationFieldBudget = 240

// interpretationTimeout gives a cold local model proportionate time for its
// second, prose-heavy answer. The configured provider timeout remains the
// floor, while the cap still guarantees that a request eventually returns.
func interpretationTimeout(base, sqlInference time.Duration) time.Duration {
	if base <= 0 {
		base = provider.DefaultTimeout
	}
	adaptive := 3 * sqlInference
	return min(max(base, adaptive), 3*base)
}

// tried renders the diagnosis: every provider, its reason and its remedy.
func tried(attempts []provider.Attempt) string {
	var out strings.Builder
	for _, attempt := range attempts {
		out.WriteString("  · " + attempt.Name + ": " + cmp.Or(attempt.Reason, "not available"))
		if attempt.Action != "" {
			out.WriteString("\n    remedy: " + attempt.Action)
		}
		out.WriteString("\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

// reasonsOf is the one-line roll call of providers that did not serve, with
// each one's own reason. Both notes that name a fall are built from it.
func reasonsOf(attempts []provider.Attempt) string {
	reasons := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		reasons = append(reasons, attempt.Name+": "+cmp.Or(attempt.Reason, "not available"))
	}
	return strings.Join(reasons, "; ")
}

func noteAboutTheFall(chosen provider.Provider, attempts []provider.Attempt) string {
	if len(attempts) < 2 {
		return ""
	}
	prefix := "the providers ahead of it were not available (" +
		reasonsOf(attempts[:len(attempts)-1]) + "): "
	if chosen.Name() == provider.NameOllama {
		return prefix + fmt.Sprintf("degraded to the local floor (%s)", chosen.Name())
	}
	return prefix + fmt.Sprintf("answered by %s", chosen.Name())
}

// rescue is stage 5: the direct search with the operator's own words. It is
// what makes the model path degrade instead of failing.
//
// It reuses the term-search route whole, which is the one that already knows
// how to fold text and build the FTS5 expression, and honours the
// search-excluded layers. A rescue with a different search would return different
// rows from the same question depending on which stage answered.
// It cannot fail: whatever goes wrong with the search itself is one more way of
// having nothing to answer with, and the query already carries its own declared
// reason.
func (s *Service) rescue(ctx context.Context, req QueryRequest, res QueryResult,
	degraded, message string) QueryResult {

	// The message describes the answer, never who was asked: that is what
	// ProviderNote is for, and mixing them is what produced an answer claiming a
	// provider was unavailable while naming that same provider as the engine.
	res.Message = message
	res.Degraded = degraded

	term := query.SearchTerm(req.Question)
	if term == "" {
		// Nothing to search for with: the honest answer is zero rows.
		res.found(nil, nil)
		return res
	}
	plan := query.Plan{Template: query.TemplateSearchByTerm, Term: term}
	if req.Layer != "" {
		plan.Layer = req.Layer
	}
	label := "falling back to literal term search: " + strings.ReplaceAll(plan.Term, "+", " ")
	res.Message = strings.TrimSpace(strings.TrimSpace(res.Message) + "\n" + label)
	if req.SQLOnly {
		return s.rescueSQL(plan, res)
	}

	progress(req, QueryPhaseExecution)
	executionStart := time.Now()
	columns, rows, stmt, provenance, err := s.searchByTerm(ctx, plan, "", req.MaxChars, true)
	res.ExecutionMS += time.Since(executionStart).Milliseconds()
	if err != nil {
		// A rescue that fails is not a second failure to report: the query
		// already has its declared reason and adding this one buries it.
		res.Match = MatchEmpty
		return res
	}
	if len(rows) == 0 {
		res.found(nil, nil)
		return res
	}

	res.Path = PathKeyword
	res.QueryPlan = &plan
	res.SQL = stmt
	res.Search = provenance
	res.Retried = true
	res.foundSearch(columns, rows)
	return res
}

// searchByTerm resolves the term-search template by the best available route,
// and also returns the provenance of that decision.
func (s *Service) searchByTerm(ctx context.Context, plan query.Plan, method string,
	maxChars int, matchAny bool) (columns []string, rows []map[string]any, stmt string,
	provenance *search.Provenance, err error) {

	gate, err := s.theGate()
	if err != nil {
		return nil, nil, "", nil, err
	}
	engine := &search.Engine{DB: s.db, Validate: gate.Validate}

	limit := plan.Limit
	if limit <= 0 {
		limit = 10
	}

	var sqlLexical string
	if method != search.MethodLike {
		if matchAny {
			sqlLexical, err = query.RenderSQLFTSAny(plan, s.registry.SearchExcluded(), limit)
		} else {
			sqlLexical, err = query.RenderSQLFTS(plan, s.registry.SearchExcluded(), limit)
		}
		if err != nil {
			return nil, nil, "", nil, err
		}
	}

	result, err := engine.Search(ctx, search.Request{
		Term:       plan.Term,
		SQLLexical: sqlLexical,
		Method:     method,
		Limit:      limit,
	})
	if err != nil {
		return nil, nil, "", nil, err
	}

	if result.Provenance.Method == search.MethodLike {
		like, err := query.RenderSQLLike(plan, s.registry.SearchExcluded())
		if err != nil {
			return nil, nil, "", nil, err
		}
		validated, err := gate.Validate(like)
		if err != nil {
			return nil, nil, "", nil, err
		}
		columns, rows, err := s.execute(ctx, validated, plan.Term, maxChars)
		if err != nil {
			return nil, nil, "", nil, err
		}
		return columns, rows, validated, &result.Provenance, nil
	}

	columns = []string{"source", "id", "author", "text", "created_at"}
	rows = make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		var date any
		if row.Date.Valid {
			date = row.Date.String
		}
		var author any
		if row.Author.Valid {
			author = row.Author.String
		}
		rows = append(rows, map[string]any{
			"source":     row.Source,
			"id":         row.ID,
			"author":     author,
			"text":       truncate(row.Text, maxChars, plan.Term),
			"created_at": date,
		})
	}
	return columns, rows, result.SQL, &result.Provenance, nil
}

// rescueSQL compiles the deterministic literal fallback without executing it.
// SQL-only is an inspection boundary: provider selection and the gate may run,
// but the operator's database must not be queried for result rows.
func (s *Service) rescueSQL(plan query.Plan, res QueryResult) QueryResult {
	const limit = 10
	stmt, err := query.RenderSQLFTSAny(plan, s.registry.SearchExcluded(), limit)
	if err != nil {
		return res
	}
	gate, err := s.theGate()
	if err != nil {
		return res
	}
	validated, err := gate.Validate(stmt)
	if err != nil {
		return res
	}
	res.SQL = validated
	res.QueryPlan = &plan
	return res
}

// sqlPrompt builds what the model receives: the schema it may query and the
// rules that keep the answer runnable.
//
// Both halves come from ONE read of the SAME DDL the gate prepares its
// validation database with, minus the SAME tables the gate hides. That is not
// tidiness: a prompt that announces a schema the gate does not have produces
// SQL that is born rejected, and it did. See internal/query/prompt.go.
func (s *Service) sqlPrompt(layer string) string {
	hints := make([]query.LayerHint, 0, len(s.registry.Layers))
	for _, declared := range s.registry.Layers {
		if declared.Deprecated || declared.AliasOf != "" {
			continue
		}
		hints = append(hints, query.LayerHint{
			Name: declared.Name, Description: declared.Description,
		})
	}

	var filter []string
	if layer != "" {
		filter = []string{layer}
	}
	return query.SQLSystemPrompt(theModelsSchema(), query.SortedLayerHints(hints), filter)
}

// theModelsSchema is read once: it never changes for a given build, and parsing
// the DDL on every question would be paying for the same answer over and over.
// Schema and SearchSchema travel together so the model sees the FTS tables the
// gate already prepares — without them it invents content LIKE '%term%'.
var theModelsSchema = sync.OnceValue(func() query.Schema {
	return query.ReadSchema(data.Schema+"\n"+data.SearchSchema, sqlgate.HiddenTables())
})
