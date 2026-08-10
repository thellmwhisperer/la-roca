/*
@overview LLM-route presentation contracts for centered exchange text and honest counts. ~100 lines, no public symbols.

	READING GUIDE
	-------------
	1. Start at TestLLMExchangeTextIsCenteredOnTheQuestionTerm
	2. Read TestLLMCountSaysItCountsRowsNotEvents, including non-count SQL safeguards
	3. Shared provider and service fixtures live in llm_test.go

	MAIN FLOW
	---------
	model SQL -> validated execution -> centered/qualified natural answer

	PUBLIC API
	----------
	None; this file tests Service.Query through the fake provider.

	INTERNALS
	---------
	TestLLMExchangeTextIsCenteredOnTheQuestionTerm, TestLLMCountSaysItCountsRowsNotEvents

@exports
@deps context/strings/testing, internal service
*/
package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// -- 1/2 CORE · TestLLMExchangeTextIsCenteredOnTheQuestionTerm -- <- START HERE

func TestLLMExchangeTextIsCenteredOnTheQuestionTerm(t *testing.T) {
	svc := serviceWithModel(t, answering("codex",
		"SELECT 'exchange' AS source, id, agent_text AS text FROM exchanges WHERE id = 1 LIMIT 1"))
	if _, err := svc.DB().SQL().Exec(
		"INSERT INTO sessions (session_id) VALUES ('health');"+
			"INSERT INTO exchanges (id, session_id, exchange_number, agent_text) VALUES (1, 'health', 1, ?)",
		strings.Repeat("preamble ", 80)+"Qwen and Gemma discussed resonancia in the health cluster"); err != nil {
		t.Fatalf("seed exchange: %v", err)
	}

	res, err := svc.Query(context.Background(), service.QueryRequest{
		Question: "resonancia", MaxChars: 140,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	text, _ := res.Rows[0]["text"].(string)
	if !strings.Contains(text, "resonancia") || !strings.HasPrefix(text, "…") {
		t.Fatalf("LLM exchange text is not centered on the match: %q", text)
	}
}

// -/ 1/2

// -- 2/2 CORE · TestLLMCountSaysItCountsRowsNotEvents --

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

// -/ 2/2
