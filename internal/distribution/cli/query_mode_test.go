package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

type queryModeProvider struct {
	answers []string
	calls   int
}

func (p *queryModeProvider) Name() string    { return "fake" }
func (p *queryModeProvider) ModelID() string { return "fake-model" }
func (p *queryModeProvider) Ready(context.Context) provider.Readiness {
	return provider.Readiness{Ready: true, ModelID: p.ModelID()}
}
func (p *queryModeProvider) Models(context.Context) provider.ModelReport {
	return provider.ModelReport{Ready: true, Models: []string{p.ModelID()}}
}
func (p *queryModeProvider) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	answer := p.answers[p.calls]
	p.calls++
	return provider.ChatResponse{Content: answer, Provider: p.Name(), ModelID: p.ModelID()}, nil
}

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

func TestQueryDefaultsToOneInferenceAndRows(t *testing.T) {
	model := &queryModeProvider{answers: []string{
		"SELECT 'raw evidence' AS text LIMIT 1",
		"The evidence says the format is rows.",
	}}
	answer, err := answerQuery(t.Context(), queryModeService(t, model), service.QueryRequest{
		Question: "what decisions were made about the format",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 {
		t.Fatalf("default query made %d provider calls, want one", model.calls)
	}
	got := axiQuery(answer)
	if !strings.Contains(got, "rows[1]{text}") || !strings.Contains(got, "raw evidence") {
		t.Fatalf("default query did not render its evidence rows:\n%s", got)
	}
}

func TestQueryFullAddsOneInterpretationAndKeepsEvidence(t *testing.T) {
	model := &queryModeProvider{answers: []string{
		"SELECT 'raw evidence' AS text LIMIT 1",
		"The evidence says the format is rows.",
	}}
	answer, err := answerQuery(t.Context(), queryModeService(t, model), service.QueryRequest{
		Question: "what decisions were made about the format",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 {
		t.Fatalf("full query made %d provider calls, want two", model.calls)
	}
	got := axiQuery(answer)
	for _, want := range []string{
		"route llm_fallback · provider fake · model fake-model",
		"The evidence says the format is rows.",
		"rows[1]{text}",
		"raw evidence",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("full query output does not contain %q:\n%s", want, got)
		}
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
