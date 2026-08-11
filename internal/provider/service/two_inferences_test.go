package service_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

		if got.answer.Text != "La decisión está guardada en una memoria." {
			t.Fatalf("prose = %q", got.answer.Text)
		}
		prompt := fake.proseRequests[0]
		if !strings.Contains(prompt, "¿Qué decisión se tomó sobre el formato?") ||
			!strings.Contains(prompt, "same language as the question") ||
			!strings.Contains(prompt, "simple dashes") ||
			!strings.Contains(prompt, "Do not use headings or tables") {
			t.Fatalf("language contract did not reach inference 2:\n%s", prompt)
		}
	})

	t.Run("thin evidence is declared before style and never filled with general knowledge", func(t *testing.T) {
		fake := newTwoInferenceFake(
			[]string{"SELECT content FROM memories LIMIT 1"},
			"The rows do not support a revenue target. No target can be stated from this memory.",
		)
		got := runFullInference(t, fake, "In an epic tone, what revenue target was set?")

		if !strings.HasPrefix(got.answer.Text, "The rows do not support") {
			t.Fatalf("thin evidence was not declared first: %q", got.answer.Text)
		}
		prompt := fake.proseRequests[0]
		for _, rule := range []string{
			"Use only these results, never general knowledge",
			"say so plainly before anything else",
			"A requested style changes delivery only and never licenses invention",
		} {
			if !strings.Contains(prompt, rule) {
				t.Errorf("interpretation prompt lacks %q:\n%s", rule, prompt)
			}
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
		if got.answer.Text != theLocalProse ||
			len(fake.sqlRequests) != 1 || len(fake.proseRequests) != 1 {
			t.Fatalf("two inferences did not complete: prose=%q SQL=%d prose calls=%d",
				got.answer.Text, len(fake.sqlRequests), len(fake.proseRequests))
		}
		for _, row := range got.result.Rows {
			if !strings.Contains(fake.proseRequests[0], fmt.Sprint(row["content"])) {
				t.Fatalf("interpretation omitted query row %v", row)
			}
		}
	})
}

// TestTheTwoInferencesSplitAcrossProviders is the privacy shape: the question
// and the schema go to the provider that writes the SQL, and the rows that SQL
// returned go only to the provider configured to read them.
func TestTheTwoInferencesSplitAcrossProviders(t *testing.T) {
	t.Run("the rows are read by the configured interpretation provider", func(t *testing.T) {
		frontier, local := theSplitPair("The frontier read the rows.")
		got := runFullInferenceWithService(t, splitService(t, frontier, local), theFreeQuestion)

		if got.result.Engine != "codex" || got.result.Model != "gpt-frontier" {
			t.Fatalf("the SQL provenance changed: %+v", got.result)
		}
		if got.answer.Engine != "ollama" || got.answer.Model != "qwen-local" ||
			got.answer.Note != "" || got.answer.Text != theLocalProse {
			t.Fatalf("the rows were not read by the interpretation provider: %+v", got.answer)
		}
		if len(frontier.proseRequests) != 0 || len(local.proseRequests) != 1 ||
			len(local.sqlRequests) != 0 {
			t.Fatalf("the inferences did not split: frontier prose %d, local prose %d, local SQL %d",
				len(frontier.proseRequests), len(local.proseRequests), len(local.sqlRequests))
		}
	})

	t.Run("an interpretation provider that is down falls back and says so", func(t *testing.T) {
		frontier, local := theSplitPair("The frontier read the rows.")
		local.notReady = "ollama is not running"
		got := runFullInferenceWithService(t, splitService(t, frontier, local), theFreeQuestion)

		if got.answer.Engine != "codex" || got.answer.Text != "The frontier read the rows." {
			t.Fatalf("the fallback did not answer: %+v", got.answer)
		}
		if !strings.Contains(got.answer.Note, "ollama is not running") ||
			!strings.Contains(got.answer.Note, "codex") {
			t.Fatalf("the fallback is not declared honestly: %q", got.answer.Note)
		}
		if len(local.proseRequests) != 0 {
			t.Fatalf("a provider that is down was asked anyway")
		}
	})

	t.Run("no result row ever reaches the provider that only writes SQL", func(t *testing.T) {
		frontier, local := theSplitPair("unused")
		runFullInferenceWithService(t, splitService(t, frontier, local), theFreeQuestion)

		asked := strings.Join(append(frontier.sqlRequests, frontier.proseRequests...), "\n")
		if !strings.Contains(asked, theFreeQuestion) || !strings.Contains(asked, "<schema>") {
			t.Fatalf("the SQL provider was not given the question and the schema:\n%s", asked)
		}
		if strings.Contains(asked, theSeededRow) {
			t.Fatalf("a result row reached the provider that only writes SQL:\n%s", asked)
		}
		if !strings.Contains(local.proseRequests[0], theSeededRow) {
			t.Fatalf("the interpretation provider did not receive the rows:\n%s",
				local.proseRequests[0])
		}
	})
}

// The second inference is where a local reasoning model costs the most: the
// prompt is long and the answer is prose. Thinking is off on that request, and
// the switch is the API field, because on qwen3.5 an in-prompt /no_think does
// nothing and the difference measured on a real machine was minutes against
// seconds.
func TestTheInterpretationAsksTheLocalModelNotToThink(t *testing.T) {
	var asked map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{{"name": "qwen3.5:4b", "model": "qwen3.5:4b"}}})
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			t.Errorf("decode the interpretation request: %v", err)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"content": theLocalProse}})
	}))
	defer server.Close()

	frontier, _ := theSplitPair("unused")
	local := provider.NewOllama(provider.OllamaConfig{
		BaseURL: server.URL, Model: "qwen3.5:4b"})
	got := runFullInferenceWithService(t, splitService(t, frontier, local), theFreeQuestion)

	if got.answer.Engine != provider.NameOllama || got.answer.Text != theLocalProse {
		t.Fatalf("the local model did not read the rows: %+v", got.answer)
	}
	if think, declared := asked["think"].(bool); !declared || think {
		t.Fatalf("the interpretation let the model think: think=%v declared=%v", think, declared)
	}
}

// theSeededRow is content this installation holds and the split SQL returns, so
// a test can tell "the rows travelled here" from "the schema travelled here".
const theSeededRow = "the team hates long dashes in the generated text"

const theLocalProse = "Two memories answer the question."

// theSplitPair is the pair the split tests ask: a frontier model that writes
// SQL and a local model that reads the rows it returned.
func theSplitPair(frontierProse string) (frontier, local *twoInferenceFake) {
	frontier = newTwoInferenceFake(
		[]string{"SELECT content FROM memories ORDER BY id LIMIT 2"}, frontierProse)
	frontier.name, frontier.model = "codex", "gpt-frontier"
	local = newTwoInferenceFake(nil, theLocalProse)
	local.name, local.model = "ollama", "qwen-local"
	return frontier, local
}

func splitService(t *testing.T, sql, interpret provider.Provider) *service.Service {
	t.Helper()
	return seededServiceWith(t, cascadeOf(sql), cascadeOf(interpret))
}

type fullInference struct {
	result service.QueryResult
	answer service.Interpretation
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
	answer, err := svc.Interpret(context.Background(), question, result.Columns, result.Rows, 0)
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	return fullInference{result: result, answer: answer}
}

// twoInferenceFake is the provider-domain seam for the model's two jobs. SQL
// answers only requests with a system prompt; prose answers only the subsequent
// row-interpretation request. It never opens a socket or invokes a real model.
// name, model and notReady are what the split tests set: two of these under
// different names is an installation with the inferences on two providers.
type twoInferenceFake struct {
	sql           []string
	prose         string
	name          string
	model         string
	notReady      string
	sqlRequests   []string
	proseRequests []string
}

func newTwoInferenceFake(sql []string, prose string) *twoInferenceFake {
	return &twoInferenceFake{sql: sql, prose: prose}
}

func (f *twoInferenceFake) Name() string    { return cmp.Or(f.name, "fake") }
func (f *twoInferenceFake) ModelID() string { return cmp.Or(f.model, "canned-two-inference") }
func (f *twoInferenceFake) Ready(context.Context) provider.Readiness {
	if f.notReady != "" {
		return provider.Readiness{Reason: f.notReady}
	}
	return provider.Readiness{Ready: true, ModelID: f.ModelID()}
}
func (f *twoInferenceFake) Models(context.Context) provider.ModelReport {
	return provider.ModelReport{Ready: true, Models: []string{f.ModelID()}}
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
