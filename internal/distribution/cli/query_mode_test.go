package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

type queryModeProvider struct {
	answers []string
	// name and model are the provenance a split test needs: two of these under
	// different names is an installation with the inferences on two providers.
	name    string
	model   string
	calls   int
	failAt  int
	delays  []time.Duration
	budgets []time.Duration
}

type streamingQueryModeProvider struct{ *queryModeProvider }

func (p *streamingQueryModeProvider) ChatStream(ctx context.Context, _ provider.ChatRequest,
	onDelta func(string)) (provider.ChatResponse, error) {
	p.calls++
	for _, delta := range []string{"The evidence ", "says the format is rows."} {
		onDelta(delta)
	}
	return provider.ChatResponse{
		Content: queryModeProse, Provider: p.Name(), ModelID: p.ModelID(),
	}, nil
}

const (
	queryModeSQL      = "SELECT 'memory' AS source, 1 AS id, 'raw evidence' AS text LIMIT 1"
	queryModeQuestion = "what decisions were made about the format"
	queryModeProse    = "The evidence says the format is rows."
)

func (p *queryModeProvider) Name() string    { return cmp.Or(p.name, "fake") }
func (p *queryModeProvider) ModelID() string { return cmp.Or(p.model, "fake-model") }
func (p *queryModeProvider) Ready(context.Context) provider.Readiness {
	return provider.Readiness{Ready: true, ModelID: p.ModelID()}
}
func (p *queryModeProvider) Models(context.Context) provider.ModelReport {
	return provider.ModelReport{Ready: true, Models: []string{p.ModelID()}}
}
func (p *queryModeProvider) Chat(ctx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
	p.calls++
	if deadline, ok := ctx.Deadline(); ok {
		p.budgets = append(p.budgets, time.Until(deadline))
	}
	if p.calls <= len(p.delays) && p.delays[p.calls-1] > 0 {
		select {
		case <-time.After(p.delays[p.calls-1]):
		case <-ctx.Done():
			return provider.ChatResponse{}, ctx.Err()
		}
	}
	if p.calls == p.failAt {
		return provider.ChatResponse{}, errors.New("interpretation unavailable")
	}
	answer := p.answers[p.calls-1]
	return provider.ChatResponse{Content: answer, Provider: p.Name(), ModelID: p.ModelID()}, nil
}

func queryModeService(t *testing.T, model *queryModeProvider) *service.Service {
	return queryModeServiceWithTimeout(t, model, 0)
}

// queryModeServiceWithTimeout is the installation the query-mode tests ask.
// Naming interpreters is the installation that splits the two inferences: the
// result rows go to them and to nobody else.
func queryModeServiceWithTimeout(t *testing.T, model *queryModeProvider,
	timeout time.Duration, interpreters ...provider.Provider) *service.Service {
	return queryModeServiceWithProvider(t, model, timeout, interpreters...)
}

func queryModeServiceWithProvider(t *testing.T, model provider.Provider,
	timeout time.Duration, interpreters ...provider.Provider) *service.Service {
	t.Helper()
	svc, err := service.Open(service.Options{
		DBPath: filepath.Join(t.TempDir(), "roca.db"),
		Providers: provider.Cascade{
			Providers: []provider.Provider{model},
			Timeout:   timeout,
		},
		Interpreters: provider.Cascade{Providers: interpreters, Timeout: timeout},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if _, err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	return svc
}

func runQueryMode(t *testing.T, full bool, failAt int) (*queryModeProvider, queryAnswer, string) {
	t.Helper()
	model := &queryModeProvider{answers: []string{queryModeSQL, queryModeProse}, failAt: failAt}
	answer, err := answerQuery(t.Context(),
		queryModeServiceWithProvider(t, model, 0), service.QueryRequest{
			Question: queryModeQuestion,
		}, full)
	if err != nil {
		t.Fatal(err)
	}
	return model, answer, axiQuery(answer)
}

func TestQueryDefaultsToOneInferenceAndRows(t *testing.T) {
	model, _, got := runQueryMode(t, false, 0)
	if model.calls != 1 {
		t.Fatalf("default query made %d provider calls, want one", model.calls)
	}
	if !strings.Contains(got, "rows[1]{source,id,text}") || !strings.Contains(got, "raw evidence") {
		t.Fatalf("default query did not render its evidence rows:\n%s", got)
	}
}

func TestQueryFullAddsOneInterpretationWithoutEvidence(t *testing.T) {
	model, answer, got := runQueryMode(t, true, 0)
	if model.calls != 2 {
		t.Fatalf("full query made %d provider calls, want two", model.calls)
	}
	for _, want := range []string{
		"route model",
		"SQL · provider fake · model fake-model",
		"answer · provider fake · model fake-model",
		"The evidence says the format is rows.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("full query output does not contain %q:\n%s", want, got)
		}
	}
	if answer.result.Interpretation != queryModeProse {
		t.Errorf("structured interpretation = %q, want %q", answer.result.Interpretation, queryModeProse)
	}
	if strings.Contains(got, "rows[") || strings.Contains(got, "raw evidence") ||
		strings.Contains(got, "evidence:") || strings.Contains(got, "rows total") {
		t.Errorf("full query printed evidence after its prose:\n%s", got)
	}
}

func TestQueryFullJSONKeepsTheCompleteRowsEnvelope(t *testing.T) {
	_, answer, _ := runQueryMode(t, true, 0)
	answer.result.RowCount = 5
	answer.result.Rows = append(answer.result.Rows,
		map[string]any{"source": "memory", "id": int64(2), "text": "second"},
		map[string]any{"source": "memory", "id": int64(3), "text": "third"},
		map[string]any{"source": "memory", "id": int64(4), "text": "fourth"},
		map[string]any{"source": "memory", "id": int64(5), "text": "fifth"})
	var output strings.Builder
	if err := (&cliEnv{out: &output}).printJSON(answer.result); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Rows           []map[string]any `json:"rows"`
		RowCount       int              `json:"row_count"`
		Interpretation string           `json:"interpretation"`
	}
	if err := json.Unmarshal([]byte(output.String()), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.RowCount != 5 || len(envelope.Rows) != 5 || envelope.Interpretation != queryModeProse {
		t.Fatalf("full JSON envelope was compacted: %+v", envelope)
	}
}

func TestQueryFullStreamsOnlyNativeProvidersAndReportsEveryPhase(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "buffered", true: "native stream"}[native], func(t *testing.T) {
			answers := []string{queryModeSQL, queryModeProse}
			base := &queryModeProvider{answers: answers}
			var model provider.Provider = base
			if native {
				base.answers = answers[:1]
				model = &streamingQueryModeProvider{base}
			}
			var phases []service.QueryPhase
			var deltas []string
			var announced bool
			answer, err := answerQuery(t.Context(),
				queryModeServiceWithProvider(t, model, 0), service.QueryRequest{
					Question: queryModeQuestion,
					Progress: func(phase service.QueryPhase) {
						phases = append(phases, phase)
					},
					InterpretationStart: func(stream bool, _ service.QueryResult) { announced = stream },
					InterpretationDelta: func(delta string) { deltas = append(deltas, delta) },
				}, true)
			if err != nil {
				t.Fatal(err)
			}
			wantPhases := []service.QueryPhase{
				service.QueryPhaseSQL, service.QueryPhaseExecution, service.QueryPhaseInterpretation,
			}
			if !slices.Equal(phases, wantPhases) || announced != native {
				t.Fatalf("phases = %v, native = %v; want %v, %v",
					phases, announced, wantPhases, native)
			}
			wantDeltas := ""
			if native {
				wantDeltas = queryModeProse
			}
			if got := strings.Join(deltas, ""); got != wantDeltas {
				t.Fatalf("streamed prose = %q, want %q", got, wantDeltas)
			}
			if answer.prose != queryModeProse || base.calls != 2 ||
				answer.result.LatencyMS < answer.result.InterpretationMS {
				t.Fatalf("answer = %q, calls = %d, latency = %d/%d",
					answer.prose, base.calls, answer.result.LatencyMS, answer.result.InterpretationMS)
			}
		})
	}
}

func TestQueryFullFallsBackToRowsWhenInterpretationFails(t *testing.T) {
	model, answer, got := runQueryMode(t, true, 2)
	if model.calls != 2 || answer.interpretErr == nil {
		t.Fatalf("failed interpretation = calls %d, error %v", model.calls, answer.interpretErr)
	}
	if !strings.Contains(got, "rows[1]{source,id,text}") || !strings.Contains(got, "raw evidence") {
		t.Fatalf("failed interpretation took the rows away:\n%s", got)
	}
}

func TestQueryFullAdaptsTheInterpretationDeadlineAndReportsItsTimeout(t *testing.T) {
	model := &queryModeProvider{
		answers: []string{queryModeSQL, queryModeProse},
		delays:  []time.Duration{20 * time.Millisecond, 200 * time.Millisecond},
	}
	answer, err := answerQuery(t.Context(),
		queryModeServiceWithTimeout(t, model, 40*time.Millisecond),
		service.QueryRequest{Question: queryModeQuestion}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(model.budgets) != 2 || model.budgets[1] < 50*time.Millisecond ||
		model.budgets[1] <= model.budgets[0] {
		t.Fatalf("provider budgets = %v; want interpretation scaled above the 40ms SQL budget", model.budgets)
	}
	got := interpretationFallback(answer.interpretErr) + "\n" + axiQuery(answer)
	for _, want := range []string{
		"summary timed out; showing rows instead.", "rows[1]{source,id,text}", "raw evidence",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("timeout fallback does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, context.DeadlineExceeded.Error()) {
		t.Errorf("timeout fallback exposed the provider error:\n%s", got)
	}
}

func TestQueryHelpTeachesDataHumanAndSQLModes(t *testing.T) {
	var output strings.Builder
	root := rootCommand(&cliEnv{})
	root.SetOut(&output)
	root.SetArgs([]string{"query", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Data: query; human reading: query --full; raw SQL: exec.") {
		t.Fatalf("query help does not teach the three modes:\n%s", output.String())
	}
}

// An installation that splits the two inferences answers with two provenances:
// the SQL provider and the one that read the rows. Both travel in the envelope
// and both are printed, because "the rows never left this machine" is a claim
// an operator has to be able to check.
func TestQueryFullNamesTheProviderThatReadTheRows(t *testing.T) {
	frontier := &queryModeProvider{answers: []string{queryModeSQL},
		name: "codex", model: "gpt-frontier"}
	local := &queryModeProvider{answers: []string{queryModeProse},
		name: "ollama", model: "qwen-local"}
	answer, err := answerQuery(t.Context(),
		queryModeServiceWithTimeout(t, frontier, 0, local),
		service.QueryRequest{Question: queryModeQuestion}, true)
	if err != nil {
		t.Fatal(err)
	}
	if frontier.calls != 1 || local.calls != 1 {
		t.Fatalf("calls = SQL %d, interpretation %d; want one each", frontier.calls, local.calls)
	}
	if answer.result.Engine != "codex" || answer.result.InterpretEngine != "ollama" ||
		answer.result.InterpretModel != "qwen-local" || answer.result.InterpretNote != "" {
		t.Fatalf("the envelope does not carry both provenances: %+v", answer.result)
	}
	if got := axiQuery(answer); !strings.Contains(got,
		"answer · provider ollama · model qwen-local") {
		t.Fatalf("the rendered answer does not name who read the rows:\n%s", got)
	}
}
