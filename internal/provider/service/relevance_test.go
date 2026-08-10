/*
@overview Model-empty fallback honesty contract. ~55 lines, no public symbols.

	READING GUIDE
	-------------
	1. Start at TestAnEmptyModelPlanLabelsTheLiteralFallback
	2. Read TestUnavailableModelStillLabelsTheLiteralFallback

	MAIN FLOW
	---------
	model SQL -> zero rows -> labeled literal fallback -> QueryResult

	PUBLIC API
	----------
	None; this file tests Service.Query.

	INTERNALS
	---------
	TestAnEmptyModelPlanLabelsTheLiteralFallback, TestUnavailableModelStillLabelsTheLiteralFallback

@exports
@deps context/strings/testing, internal query/service
*/
package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// -- 1/2 CORE · TestAnEmptyModelPlanLabelsTheLiteralFallback -- <- START HERE

func TestAnEmptyModelPlanLabelsTheLiteralFallback(t *testing.T) {
	// Equality (not substring LIKE): the LIKE form is rejected before it runs,
	// so zero-row honesty has to be measured on SQL the gate still accepts.
	modelSQL := "SELECT content FROM memories WHERE content = 'nothing matches this' LIMIT 5"
	svc := serviceWithModel(t, answering("codex", modelSQL))

	res, err := svc.Query(context.Background(), service.QueryRequest{Question: theQuestionWithAMatch})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Path != service.PathKeyword || res.RowCount == 0 {
		t.Fatalf("the labeled literal fallback did not answer: %+v", res)
	}
	for _, want := range []string{
		"nothing relevant was found by the plan",
		"falling back to literal term search:",
		"guiones",
	} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("fallback message does not contain %q: %q", want, res.Message)
		}
	}
	if res.ModelSQL != modelSQL {
		t.Errorf("what the empty plan tried was lost: %q", res.ModelSQL)
	}
}

// -/ 1/2

// -- 2/2 CORE · TestUnavailableModelStillLabelsTheLiteralFallback --

func TestUnavailableModelStillLabelsTheLiteralFallback(t *testing.T) {
	svc := serviceWithModel(t, unavailable("codex", "not signed in", "roca login codex"))

	res, err := svc.Query(context.Background(), service.QueryRequest{Question: theQuestionWithAMatch})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.RowCount == 0 {
		t.Fatalf("the literal fallback did not return the seeded match: %+v", res)
	}
	if !strings.Contains(res.Message, "falling back to literal term search:") ||
		!strings.Contains(res.Message, "guiones") {
		t.Fatalf("literal rows were shown without their label: %q", res.Message)
	}
}

// -/ 2/2
