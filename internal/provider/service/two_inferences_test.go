package service_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestTwoInferenceModelPath(t *testing.T) {
	t.Run("malformed SQL is retried before interpretation", func(t *testing.T) {
		fake := newTwoInferenceFake(
			[]string{"this is not SQL", "SELECT content FROM memories LIMIT 2"},
			"The memories describe the format decision.",
		)
		got := runFullInference(t, fake, theFreeQuestion)

		if got.result.Degraded != "" || got.result.Path != service.PathLLM {
			t.Fatalf("retry did not recover: %+v", got.result)
		}
		if len(fake.sqlRequests) != 2 || len(fake.proseRequests) != 1 {
			t.Fatalf("calls = SQL %d, prose %d; want 2 and 1",
				len(fake.sqlRequests), len(fake.proseRequests))
		}
		if !strings.Contains(fake.sqlRequests[1], "rejected before running") {
			t.Fatalf("retry omitted the gate verdict:\n%s", fake.sqlRequests[1])
		}
	})

	t.Run("SQL rejected by the gate returns an honest degraded answer", func(t *testing.T) {
		fake := newTwoInferenceFake([]string{"DELETE FROM memories", "DROP TABLE memories"}, "unused")
		svc := serviceWithModel(t, fake)

		got, err := svc.Query(context.Background(), service.QueryRequest{
			Question: "unfindablezebra",
		})
		if err != nil {
			t.Fatalf("Query crashed instead of degrading: %v", err)
		}
		if got.Degraded != service.DegradedInvalidSQL || got.Match != service.MatchEmpty {
			t.Fatalf("rejection was not declared honestly: %+v", got)
		}
		if len(fake.sqlRequests) != 2 || len(fake.proseRequests) != 0 {
			t.Fatalf("calls = SQL %d, prose %d; want 2 and 0",
				len(fake.sqlRequests), len(fake.proseRequests))
		}
	})

	t.Run("zero rows remain an honest empty answer", func(t *testing.T) {
		fake := newTwoInferenceFake(
			[]string{"SELECT content FROM memories WHERE 0 LIMIT 10"}, "unused")
		svc := serviceWithModel(t, fake)

		got, err := svc.Query(context.Background(), service.QueryRequest{
			Question: "unfindableplatypus",
		})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if got.Degraded != "" || got.Match != service.MatchEmpty || got.RowCount != 0 {
			t.Fatalf("empty result was dressed up as an answer: %+v", got)
		}
		if len(fake.proseRequests) != 0 {
			t.Fatalf("empty rows reached interpretation %d times", len(fake.proseRequests))
		}
	})

	t.Run("thousands of rows are capped and the interpretation marks the truncation", func(t *testing.T) {
		fake := newTwoInferenceFake(
			[]string{"SELECT id, content FROM memories ORDER BY id LIMIT 5000"},
			"The result is a truncated summary.",
		)
		svc := serviceWithModel(t, fake)
		if _, err := svc.DB().SQL().Exec(`
			WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 1500)
			INSERT INTO memories (layer, content, origin)
			SELECT 'project', printf('bulk row %d', n), 'agent' FROM seq`); err != nil {
			t.Fatalf("seed thousands of rows: %v", err)
		}

		got := runFullInferenceWithService(t, svc, "summarize every bulk row")
		if got.result.RowCount != 1000 || !strings.Contains(got.result.SQL, "LIMIT 1000") {
			t.Fatalf("gate did not cap the model result: rows=%d sql=%q",
				got.result.RowCount, got.result.SQL)
		}
		prompt := fake.proseRequests[0]
		if !strings.Contains(prompt, "Showing 10 of 1000 rows") {
			t.Fatalf("interpretation did not mark its truncation:\n%s", prompt)
		}
		if strings.Contains(prompt, "bulk row 1000") {
			t.Fatalf("interpretation received rows beyond its cap:\n%s", prompt)
		}
	})

	t.Run("answer language follows the question language", func(t *testing.T) {
		fake := newTwoInferenceFake(
			[]string{"SELECT content FROM memories LIMIT 1"},
			"La decisión está guardada en una memoria.",
		)
		got := runFullInference(t, fake, "¿Qué decisión se tomó sobre el formato?")

		if got.prose != "La decisión está guardada en una memoria." {
			t.Fatalf("prose = %q", got.prose)
		}
		prompt := fake.proseRequests[0]
		if !strings.Contains(prompt, "¿Qué decisión se tomó sobre el formato?") ||
			!strings.Contains(prompt, "same language as the question") {
			t.Fatalf("language contract did not reach inference 2:\n%s", prompt)
		}
	})

	t.Run("clean SQL executes and rows reach interpretation", func(t *testing.T) {
		fake := newTwoInferenceFake(
			[]string{"SELECT layer, content FROM memories ORDER BY id LIMIT 2"},
			"Two memories answer the question.",
		)
		got := runFullInference(t, fake, theFreeQuestion)

		if got.result.Path != service.PathLLM || got.result.RowCount != 2 {
			t.Fatalf("clean model path did not return rows: %+v", got.result)
		}
		if got.prose != "Two memories answer the question." ||
			len(fake.sqlRequests) != 1 || len(fake.proseRequests) != 1 {
			t.Fatalf("two inferences did not complete: prose=%q SQL=%d prose calls=%d",
				got.prose, len(fake.sqlRequests), len(fake.proseRequests))
		}
		for _, row := range got.result.Rows {
			if !strings.Contains(fake.proseRequests[0], fmt.Sprint(row["content"])) {
				t.Fatalf("interpretation omitted query row %v", row)
			}
		}
	})
}

type fullInference struct {
	result service.QueryResult
	prose  string
}

func runFullInference(t *testing.T, fake *twoInferenceFake, question string) fullInference {
	t.Helper()
	return runFullInferenceWithService(t, serviceWithModel(t, fake), question)
}

func runFullInferenceWithService(t *testing.T, svc *service.Service, question string) fullInference {
	t.Helper()
	result, err := svc.Query(context.Background(), service.QueryRequest{Question: question})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	prose, err := svc.Interpret(context.Background(), question, result.Columns, result.Rows)
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	return fullInference{result: result, prose: prose}
}

// twoInferenceFake is the provider-domain seam for the model's two jobs. SQL
// answers only requests with a system prompt; prose answers only the subsequent
// row-interpretation request. It never opens a socket or invokes a real model.
type twoInferenceFake struct {
	sql           []string
	prose         string
	sqlRequests   []string
	proseRequests []string
}

func newTwoInferenceFake(sql []string, prose string) *twoInferenceFake {
	return &twoInferenceFake{sql: sql, prose: prose}
}

func (*twoInferenceFake) Name() string    { return "fake" }
func (*twoInferenceFake) ModelID() string { return "canned-two-inference" }
func (*twoInferenceFake) Ready(context.Context) provider.Readiness {
	return provider.Readiness{Ready: true, ModelID: "canned-two-inference"}
}
func (*twoInferenceFake) Models(context.Context) provider.ModelReport {
	return provider.ModelReport{Ready: true, Models: []string{"canned-two-inference"}}
}

func (f *twoInferenceFake) Chat(_ context.Context,
	req provider.ChatRequest) (provider.ChatResponse, error) {
	var prompt strings.Builder
	isSQL := false
	for _, message := range req.Messages {
		prompt.WriteString(message.Content)
		prompt.WriteByte('\n')
		isSQL = isSQL || message.Role == provider.RoleSystem
	}
	if !isSQL {
		f.proseRequests = append(f.proseRequests, prompt.String())
		return provider.ChatResponse{Content: f.prose, Provider: f.Name(), ModelID: f.ModelID()}, nil
	}
	f.sqlRequests = append(f.sqlRequests, prompt.String())
	index := min(len(f.sqlRequests)-1, len(f.sql)-1)
	return provider.ChatResponse{Content: f.sql[index], Provider: f.Name(), ModelID: f.ModelID()}, nil
}
