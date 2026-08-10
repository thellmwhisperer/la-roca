/*
@overview Contract tests for default-row and opt-in-full CLI query modes. ~175 lines, no public symbols.

	READING GUIDE
	-------------
	1. Start at TestQueryDefaultsToOneInferenceAndRows
	2. Read TestQueryFullAddsOneInterpretationAndKeepsEvidence
	3. Read the failure and help contracts last

	MAIN FLOW
	---------
	fake provider -> real Service query -> answerQuery mode -> AXI output assertions

	PUBLIC API
	----------
	None; this file tests package-private CLI behavior.

	INTERNALS
	---------
	queryModeProvider, queryModeService, runQueryMode, the four query-mode contract tests

@exports
@deps standard context/errors/path/strings/testing, internal provider/service
*/
package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// -- 1/3 HELPER · queryModeProvider --

type queryModeProvider struct {
	answers []string
	calls   int
	failAt  int
}

const (
	queryModeSQL      = "SELECT 'memory' AS source, 1 AS id, 'raw evidence' AS text LIMIT 1"
	queryModeQuestion = "what decisions were made about the format"
	queryModeProse    = "The evidence says the format is rows."
)

func (p *queryModeProvider) Name() string    { return "fake" }
func (p *queryModeProvider) ModelID() string { return "fake-model" }
func (p *queryModeProvider) Ready(context.Context) provider.Readiness {
	return provider.Readiness{Ready: true, ModelID: p.ModelID()}
}
func (p *queryModeProvider) Models(context.Context) provider.ModelReport {
	return provider.ModelReport{Ready: true, Models: []string{p.ModelID()}}
}
func (p *queryModeProvider) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	p.calls++
	if p.calls == p.failAt {
		return provider.ChatResponse{}, errors.New("interpretation unavailable")
	}
	answer := p.answers[p.calls-1]
	return provider.ChatResponse{Content: answer, Provider: p.Name(), ModelID: p.ModelID()}, nil
}

// -/ 1/3

// -- 2/3 HELPER · queryModeService --

func queryModeService(t *testing.T, model *queryModeProvider) *service.Service {
	t.Helper()
	svc, err := service.Open(service.Options{
		DBPath: filepath.Join(t.TempDir(), "roca.db"),
		Providers: provider.Cascade{
			Providers: []provider.Provider{model},
		},
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
	answer, err := answerQuery(t.Context(), queryModeService(t, model), service.QueryRequest{
		Question: queryModeQuestion,
	}, full)
	if err != nil {
		t.Fatal(err)
	}
	return model, answer, axiQuery(answer)
}

// -/ 2/3

// -- 3/3 CORE · query mode contracts -- <- START HERE

func TestQueryDefaultsToOneInferenceAndRows(t *testing.T) {
	model, _, got := runQueryMode(t, false, 0)
	if model.calls != 1 {
		t.Fatalf("default query made %d provider calls, want one", model.calls)
	}
	if !strings.Contains(got, "rows[1]{source,id,text}") || !strings.Contains(got, "raw evidence") {
		t.Fatalf("default query did not render its evidence rows:\n%s", got)
	}
}

func TestQueryFullAddsOneInterpretationAndKeepsEvidence(t *testing.T) {
	model, _, got := runQueryMode(t, true, 0)
	if model.calls != 2 {
		t.Fatalf("full query made %d provider calls, want two", model.calls)
	}
	for _, want := range []string{
		"route llm_fallback · provider fake · model fake-model",
		"The evidence says the format is rows.",
		"rows[1]{source,id,text}",
		"raw evidence",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("full query output does not contain %q:\n%s", want, got)
		}
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

// -/ 3/3
