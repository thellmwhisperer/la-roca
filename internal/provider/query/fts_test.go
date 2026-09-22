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
	stmt, err := query.RenderSQLFTS(termPlan("long+dashes"), nil, 10)
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
	if !strings.Contains(stmt, " AS author") || !strings.Contains(stmt, "source_model") || !strings.Contains(stmt, "source_surface") {
		t.Errorf("memory results do not surface their author:\n%s", stmt)
	}
}

func TestRenderSQLAttachedCorpusLikeSearchesEveryCorpusSource(t *testing.T) {
	stmt, err := query.RenderSQLAttachedCorpusLike(termPlan("cobalt+atlas"),
		[]string{"question"}, "plugin_roca_corpus", 10, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`"plugin_roca_corpus".memories`,
		`"plugin_roca_corpus".exchanges`,
		`"plugin_roca_corpus".thinking_blocks`,
		"OR m.content LIKE '%atlas%'",
		"m.layer NOT IN ('question')",
	} {
		if !strings.Contains(stmt, fragment) {
			t.Errorf("attached corpus SQL lacks %q:\n%s", fragment, stmt)
		}
	}
}

func TestRenderSearchUnionPartsDeclaresEveryDatabase(t *testing.T) {
	stmt, err := query.RenderSearchUnionParts([]string{
		"SELECT 'memory' source, 1 id, NULL author, 'core' text, NULL created_at",
		"SELECT 'memory' source, 2 id, NULL author, 'ops' text, NULL created_at",
		"SELECT 'exchange' source, 3 id, NULL author, 'corpus' text, NULL created_at",
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(stmt, "SELECT source, id, author, text, created_at FROM"); got != 3 {
		t.Fatalf("projected parts = %d, want 3:\n%s", got, stmt)
	}
}

func TestRenderSQLFTSRequiresEveryWord(t *testing.T) {
	stmt, err := query.RenderSQLFTS(termPlan("long+dashes"), nil, 10)
	if err != nil {
		t.Fatalf("RenderSQLFTS: %v", err)
	}
	if !strings.Contains(stmt, `'"long" AND "dashes"'`) {
		t.Errorf("the SQL does not require both words:\n%s", stmt)
	}
}

// A layer constraint is always respected. With a layer, the
// other three sources are not queried: they have no layer, and returning them
// would be failing to respect it.
func TestRenderSQLFTSWithALayerLooksOnlyAtMemories(t *testing.T) {
	plan := termPlan("binary")
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
	stmt, err := query.RenderSQLFTS(termPlan("binary"),
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
