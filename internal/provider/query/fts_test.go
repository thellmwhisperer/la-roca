package query_test

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

func termPlan(term string) query.Plan {
	return query.Plan{Template: query.TemplateSearchByTerm, Term: term, Limit: 10}
}

func TestRenderSQLFTSSearchesTheFourSources(t *testing.T) {
	stmt, err := query.RenderSQLFTS(termPlan("guiones+largos"), nil, 10)
	if err != nil {
		t.Fatalf("RenderSQLFTS: %v", err)
	}
	for _, source := range []string{"memories_fts", "exchanges_fts", "thinking_fts"} {
		if !strings.Contains(stmt, source) {
			t.Errorf("the SQL does not query %s:\n%s", source, stmt)
		}
	}
	// The two columns of exchanges are asked for separately, which is what
	// allows telling what the human said from what the agent said.
	for _, filter := range []string{"{human_text}", "{agent_text}"} {
		if !strings.Contains(stmt, filter) {
			t.Errorf("the SQL does not filter by the column %s:\n%s", filter, stmt)
		}
	}
	if !strings.Contains(stmt, "bm25") {
		t.Errorf("the SQL does not order by bm25:\n%s", stmt)
	}
}

func TestRenderSQLFTSRequiresEveryWord(t *testing.T) {
	stmt, err := query.RenderSQLFTS(termPlan("guiones+largos"), nil, 10)
	if err != nil {
		t.Fatalf("RenderSQLFTS: %v", err)
	}
	if !strings.Contains(stmt, `'"guiones" AND "largos"'`) {
		t.Errorf("the SQL does not require both words:\n%s", stmt)
	}
}

// A layer constraint is always respected (F04-13 contract). With a layer, the
// other three sources are not queried: they have no layer, and returning them
// would be failing to respect it.
func TestRenderSQLFTSWithALayerLooksOnlyAtMemories(t *testing.T) {
	plan := termPlan("binario")
	plan.Layer = "fact"
	stmt, err := query.RenderSQLFTS(plan, nil, 10)
	if err != nil {
		t.Fatalf("RenderSQLFTS: %v", err)
	}
	if strings.Contains(stmt, "exchanges_fts") || strings.Contains(stmt, "thinking_fts") {
		t.Errorf("with a layer, the SQL still looks at sources with no layer:\n%s", stmt)
	}
	if !strings.Contains(stmt, "layer = 'fact'") {
		t.Errorf("the SQL does not filter by the requested layer:\n%s", stmt)
	}
}

func TestRenderSQLFTSExcludesTheSearchExcludedLayers(t *testing.T) {
	stmt, err := query.RenderSQLFTS(termPlan("binario"),
		[]string{"question", "review"}, 10)
	if err != nil {
		t.Fatalf("RenderSQLFTS: %v", err)
	}
	if !strings.Contains(stmt, "layer NOT IN ('question', 'review')") {
		t.Errorf("the SQL does not exclude the search-excluded layers:\n%s", stmt)
	}
}

func TestRenderSQLFTSWithoutATermRefuses(t *testing.T) {
	for _, term := range []string{"", "+", "   "} {
		if _, err := query.RenderSQLFTS(termPlan(term), nil, 10); err == nil {
			t.Errorf("RenderSQLFTS accepted the term %q", term)
		}
	}
}

// O'Brien's apostrophe must not break the SQL, and the double quote must not
// break the MATCH expression, which is a language inside the literal.
func TestRenderSQLFTSEscapesWhatComesFromTheQuestion(t *testing.T) {
	stmt, err := query.RenderSQLFTS(termPlan("o'brien"), nil, 10)
	if err != nil {
		t.Fatalf("RenderSQLFTS: %v", err)
	}
	if strings.Contains(stmt, "'o'brien'") {
		t.Errorf("the apostrophe is not escaped:\n%s", stmt)
	}
	if !strings.Contains(stmt, `'"o" AND "brien"'`) {
		t.Errorf("the term did not arrive tokenized and quoted:\n%s", stmt)
	}
}
