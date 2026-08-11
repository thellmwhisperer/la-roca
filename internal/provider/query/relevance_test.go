package query_test

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"
)

func TestSearchTermKeepsTheSubstantialWordsAndDropsTheRest(t *testing.T) {
	benchCases := []struct{ question, term string }{
		// Short tokens and bare numbers are dropped; diacritics fold; up to four
		// words survive, AND-joined for the FTS rescue.
		{"naïve Müller façade review 2024?", "naive+muller+facade+review"},
		{"what do we know about ffmpeg", "what+know+about+ffmpeg"},
	}
	for _, c := range benchCases {
		if got := query.SearchTerm(c.question); got != c.term {
			t.Errorf("SearchTerm(%q) = %q, want %q", c.question, got, c.term)
		}
	}
}

func TestFTSPrefersCuratedMemoriesAndDemotesThinking(t *testing.T) {
	stmt, err := query.RenderSQLFTS(query.Plan{
		Template: query.TemplateSearchByTerm, Term: "ana", Limit: 10,
	}, nil, 50)
	if err != nil {
		t.Fatalf("RenderSQLFTS: %v", err)
	}
	for _, want := range []string{
		"0 AS source_priority",
		"1 AS source_priority",
		"2 AS source_priority",
		"ORDER BY source_priority, rango",
	} {
		if !strings.Contains(stmt, want) {
			t.Errorf("source-aware ordering is missing %q:\n%s", want, stmt)
		}
	}
	if got := strings.Count(stmt, "LIMIT 50"); got != 5 {
		t.Errorf("candidate branches and final result have %d limits, want five:\n%s", got, stmt)
	}
	if filter := strings.Index(stmt, "human_text NOT LIKE"); filter < 0 || filter > strings.Index(stmt[filter:], "LIMIT 50")+filter {
		t.Errorf("task notifications are filtered after the result cap:\n%s", stmt)
	}
}

func TestTheModelPromptKeepsSearchTextAndSourceRankAligned(t *testing.T) {
	schema := query.ReadSchema(data.Schema+"\n"+data.SearchSchema, sqlgate.HiddenTables())
	prompt := query.SQLSystemPrompt(schema, nil, nil)
	for _, want := range []string{
		"source_priority",
		"memory 0",
		"thinking 2",
		"{agent_text}",
		"{human_text}",
		"SELECT 'human'",
		"SELECT 'exchange', rowid, agent_text",
		"human_text NOT LIKE '<task-notification%'",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the model prompt does not contain %q:\n%s", want, prompt)
		}
	}
}

// The two renderers of one Plan agree about what a usable term is. RenderSQLFTS
// refuses a term that yields no clause; RenderSQLLike built its SQL first and
// interpolated an empty clause list, so a punctuation-only term produced
// `WHERE  AND ...`: broken SQL handed to the gate instead of a refusal.
func TestBothRenderersRefuseATermThatYieldsNoClause(t *testing.T) {
	for _, term := range []string{"+", " + ", "++"} {
		plan := query.Plan{Template: query.TemplateSearchByTerm, Term: term, Limit: 10}
		if _, err := query.RenderSQLFTS(plan, nil, 50); err == nil {
			t.Errorf("RenderSQLFTS(%q) accepted a term with no word in it", term)
		}
		stmt, err := query.RenderSQLLike(plan, nil)
		if err == nil {
			t.Errorf("RenderSQLLike(%q) accepted a term with no word in it: %s", term, stmt)
		}
	}
}
