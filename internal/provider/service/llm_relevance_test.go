package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestLLMExchangeTextKeepsTheSubjectAndQuestionTerm(t *testing.T) {
	svc := serviceWithModel(t, answering("codex",
		"SELECT 'exchange' AS source, id, agent_text AS text FROM exchanges WHERE id = 1 LIMIT 1"))
	if _, err := svc.DB().SQL().Exec(
		"INSERT INTO sessions (session_id) VALUES ('health');"+
			"INSERT INTO exchanges (id, session_id, exchange_number, agent_text) VALUES (1, 'health', 1, ?)",
		"Alex, SDE: "+strings.Repeat("preamble ", 80)+"met Morgan in the synthetic health cluster"); err != nil {
		t.Fatalf("seed exchange: %v", err)
	}

	res, err := svc.Query(context.Background(), service.QueryRequest{
		Question: "Morgan", MaxChars: 140,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	text, _ := res.Rows[0]["text"].(string)
	if !strings.HasPrefix(text, "Alex, SDE") || !strings.Contains(text, "Morgan") || strings.HasPrefix(text, "…") {
		t.Fatalf("LLM exchange text lost its subject or match: %q", text)
	}
}

func TestLLMCountSaysItCountsRowsNotEvents(t *testing.T) {
	svc := serviceWithModel(t, answering("codex",
		"SELECT COUNT(*) AS times FROM memories LIMIT 1"))
	res, err := svc.Query(context.Background(), service.QueryRequest{
		Question: "how many times Dana gets angry",
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, want := range []string{"rows", "not distinct events"} {
		if !strings.Contains(strings.ToLower(res.Message), want) {
			t.Errorf("count qualification does not contain %q: %q", want, res.Message)
		}
	}

	distinct := serviceWithModel(t, answering("codex",
		"SELECT COUNT(DISTINCT layer) AS times FROM memories LIMIT 1"))
	distinctResult, err := distinct.Query(context.Background(), service.QueryRequest{
		Question: "how many distinct classes are there",
	})
	if err != nil {
		t.Fatalf("distinct Query: %v", err)
	}
	if distinctResult.Message != "" {
		t.Fatalf("distinct-value count is described as a row count: %q", distinctResult.Message)
	}

	literal := serviceWithModel(t, answering("codex",
		"SELECT 'COUNT(*)' AS label FROM memories LIMIT 1"))
	literalResult, err := literal.Query(context.Background(), service.QueryRequest{
		Question: "Show the label",
	})
	if err != nil {
		t.Fatalf("literal Query: %v", err)
	}
	if literalResult.Message != "" {
		t.Fatalf("a COUNT(*) string literal is described as a row count: %q", literalResult.Message)
	}
}
