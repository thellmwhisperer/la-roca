package query

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/internal/query/sqlgate"
)

const someDDL = `
-- a comment that is not schema
CREATE TABLE IF NOT EXISTS sessions (
  session_id    TEXT PRIMARY KEY,
  source_agent  TEXT DEFAULT 'claude-code',
  project       TEXT
);

CREATE TABLE memories (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  layer    TEXT NOT NULL,
  content  TEXT NOT NULL,
  supersedes INTEGER REFERENCES memories(id)
);

CREATE TABLE ingest_file_state (
  path TEXT PRIMARY KEY
);

CREATE INDEX idx_memories_layer ON memories(layer);
`

func TestSemanticLayerListsTheVisibleTablesWithTheirColumns(t *testing.T) {
	layer := ReadSchema(someDDL, []string{"ingest_file_state"}).Describe(nil)

	for _, wanted := range []string{"sessions", "memories", "session_id", "source_agent", "content", "supersedes"} {
		if !strings.Contains(layer, wanted) {
			t.Errorf("the semantic layer does not name %q:\n%s", wanted, layer)
		}
	}
}

// What the gate cannot see does not exist for the model either: offering a
// table the gate is going to reject is offering an answer that never runs.
func TestSemanticLayerHidesWhatTheGateHides(t *testing.T) {
	layer := ReadSchema(someDDL, []string{"ingest_file_state"}).Describe(nil)
	if strings.Contains(layer, "ingest_file_state") {
		t.Fatalf("it offers a table the gate hides:\n%s", layer)
	}
}

func TestSemanticLayerIsNotFooledByIndexesOrComments(t *testing.T) {
	layer := ReadSchema(someDDL, nil).Describe(nil)
	if strings.Contains(layer, "idx_memories_layer") {
		t.Fatalf("an index is not a table:\n%s", layer)
	}
	if strings.Contains(layer, "a comment that is not schema") {
		t.Fatalf("a comment is not schema:\n%s", layer)
	}
}

// The layer registry is what gives a bare name a meaning: without it the model
// does not know what `layer = 'handoff'` is.
func TestSemanticLayerCarriesTheLayersAndWhatTheyAreFor(t *testing.T) {
	layer := ReadSchema(someDDL, nil).Describe([]LayerHint{
		{Name: "handoff", Description: "Session continuity context for future agents."},
	})
	if !strings.Contains(layer, "handoff") || !strings.Contains(layer, "continuity") {
		t.Fatalf("the layers do not travel:\n%s", layer)
	}
}

func TestTheSystemPromptCarriesTheRulesThatKeepTheAnswerRunnable(t *testing.T) {
	prompt := SQLSystemPrompt(ReadSchema(someDDL, nil), nil, nil)

	for _, rule := range []string{"SELECT", "supersedes IS NULL", "LIMIT"} {
		if !strings.Contains(prompt, rule) {
			t.Errorf("the prompt does not impose %q:\n%s", rule, prompt)
		}
	}
	if !strings.Contains(prompt, "memories(") {
		t.Error("the prompt does not carry the schema")
	}
	if !strings.Contains(strings.ToLower(prompt), "no markdown") &&
		!strings.Contains(strings.ToLower(prompt), "no code fences") {
		t.Errorf("the prompt does not forbid the fence:\n%s", prompt)
	}
}

// THE DEFECT THIS TEST EXISTS FOR.
//
// The prompt used to impose `WHERE supersedes IS NULL` on every query, and
// `supersedes` only exists on `memories`. Every question the model answered from
// `sessions` came back with a column that is not there, the gate rejected it,
// and two different questions produced the same rejection. That is the shape of
// a systematic defect: the prompt was announcing a schema the gate does not
// have.
//
// The rule is now derived from the same DDL the gate prepares itself with, so a
// rule about a column can only name the tables that really carry it.
func TestTheSupersedesRuleNamesOnlyTheTablesThatCarryTheColumn(t *testing.T) {
	prompt := SQLSystemPrompt(ReadSchema(someDDL, nil), nil, nil)

	line := ruleAbout(rulesOf(t, prompt), "supersedes IS NULL")
	if line == "" {
		t.Fatalf("the prompt imposes no rule about supersedes:\n%s", prompt)
	}
	if !strings.Contains(line, "memories") {
		t.Errorf("the rule does not name the table that does carry it: %q", line)
	}
	if strings.Contains(line, "sessions") {
		t.Errorf("the rule names a table with no such column: %q", line)
	}
	if !strings.Contains(strings.ToLower(line), "only") && !strings.Contains(line, "never") {
		t.Errorf("the rule does not say the column is exclusive to that table: %q", line)
	}
}

// A schema with no such column earns no rule at all. A rule about a column that
// does not exist is the same defect written the other way round.
func TestARuleAboutAColumnNobodyHasIsNotWritten(t *testing.T) {
	ddl := "CREATE TABLE sessions (\n  session_id TEXT PRIMARY KEY\n);\n"
	prompt := SQLSystemPrompt(ReadSchema(ddl, nil), nil, nil)
	if strings.Contains(prompt, "supersedes") {
		t.Fatalf("it imposes a rule about a column no table has:\n%s", prompt)
	}
}

// The layer filter is the same class of rule and had the same hole: `layer`
// only exists on `memories` too.
func TestTheLayerFilterNamesTheTableThatCarriesTheColumn(t *testing.T) {
	prompt := SQLSystemPrompt(ReadSchema(someDDL, nil), nil, []string{"handoff"})
	if !strings.Contains(prompt, "layer = 'handoff'") {
		t.Fatalf("the filter is not imposed:\n%s", prompt)
	}
	instruction := ruleAbout(prompt, "layer = 'handoff'")
	if !strings.Contains(instruction, "memories") {
		t.Fatalf("the filter does not say which table it applies to: %q", instruction)
	}

	several := SQLSystemPrompt(ReadSchema(someDDL, nil), nil, []string{"handoff", "question"})
	if !strings.Contains(several, "layer IN ('handoff', 'question')") {
		t.Fatalf("the filter over several layers is not imposed:\n%s", several)
	}
}

// A layer name comes from the registry, but the escaping is not conditional on
// trusting it: a name with a quote inside closes the literal, and that is an
// injection into the very prompt that generates the SQL.
func TestTheLayerFilterEscapesTheQuote(t *testing.T) {
	prompt := SQLSystemPrompt(ReadSchema(someDDL, nil), nil, []string{"it's"})
	if !strings.Contains(prompt, "'it''s'") {
		t.Fatalf("it did not escape the quote:\n%s", prompt)
	}
}

// The property that makes the defect impossible to reintroduce, measured over
// the schema the product really ships and the tables the gate really hides.
//
// Every identifier a rule names is written in backticks, which is what makes
// this checkable: each one of them has to be a table or a column of the schema
// the same prompt announces. The old `WHERE supersedes IS NULL` rule passed the
// eye and failed this, because it named a column while naming no table that
// carries it.
func TestThePromptNeverNamesAnIdentifierTheSchemaDoesNotHave(t *testing.T) {
	schema := productSchema()
	prompt := SQLSystemPrompt(schema, nil, []string{"handoff"})

	named := backticked(prompt[strings.Index(prompt, "<rules>"):])
	if len(named) == 0 {
		t.Fatal("the rules name no identifier at all: this test would measure nothing")
	}
	for _, identifier := range named {
		identifier = strings.Fields(identifier)[0] // `supersedes IS NULL` -> supersedes
		if !schema.HasColumn(identifier) && !hasTable(schema, identifier) {
			t.Errorf("the rules name %q, which is neither a table nor a column of the announced schema:\n%s",
				identifier, prompt)
		}
	}
}

// And the same property the other way round, which is the one that actually
// caught this: a rule that names a column must name the tables that carry it,
// so the model knows where it may apply it.
func TestEveryRuleAboutAColumnNamesTheTablesThatCarryIt(t *testing.T) {
	schema := productSchema()
	rules := rulesOf(t, SQLSystemPrompt(schema, nil, nil))

	for _, line := range strings.Split(rules, "\n") {
		for _, identifier := range backticked(line) {
			column := strings.Fields(identifier)[0]
			if !schema.HasColumn(column) {
				continue
			}
			for _, carrier := range schema.TablesWith(column) {
				if !strings.Contains(line, carrier) {
					t.Errorf("the rule about %q does not name %q, which is where that column lives: %q",
						column, carrier, line)
				}
			}
		}
	}
}

func TestReadSchemaOverTheRealDDLFindsSupersedesOnlyInMemories(t *testing.T) {
	schema := ReadSchema(data.Schema, sqlgate.HiddenTables())

	carriers := schema.TablesWith("supersedes")
	if len(carriers) != 1 || carriers[0] != "memories" {
		t.Fatalf("supersedes lives in %v: the rule the prompt writes depends on this", carriers)
	}
	if !hasTable(schema, "sessions") {
		t.Fatal("the real schema has sessions and the reader did not see it")
	}
	if hasTable(schema, "ingest_file_state") {
		t.Fatal("the reader offers a table the gate hides")
	}
}

// columnsOf is what the property tests ask the schema; production uses
// TablesWith/HasColumn (and hasTable, which the prompt builder shares).
func columnsOf(s Schema, table string) []string {
	for _, declared := range s.Tables {
		if declared.Name == table {
			return declared.Columns
		}
	}
	return nil
}

// ruleAbout returns the line that talks about something.
func ruleAbout(text, needle string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// rulesOf is the <rules> block alone: the schema block names every column there
// is by definition, so a property about what the rules name has to be measured
// on the rules.
func rulesOf(t *testing.T, prompt string) string {
	t.Helper()
	_, rest, found := strings.Cut(prompt, "<rules>")
	if !found {
		t.Fatalf("the prompt has no rules block:\n%s", prompt)
	}
	rules, _, found := strings.Cut(rest, "</rules>")
	if !found {
		t.Fatalf("the rules block is not closed:\n%s", prompt)
	}
	return rules
}

var inBackticks = regexp.MustCompile("`([^`]+)`")

// backticked are the identifiers a piece of prompt names. Backticks are how the
// prompt writes an identifier, which is what makes the property checkable.
func backticked(text string) []string {
	var found []string
	for _, match := range inBackticks.FindAllStringSubmatch(text, -1) {
		found = append(found, match[1])
	}
	return found
}

// THE SECOND DEFECT OF THE SAME FAMILY.
//
// `what tools does the pi agent use the most` came back rejected with
// `no such column: source_agent` over
// `SELECT tool_name ... FROM tool_uses WHERE source_agent = 'pi'`. The gate was
// right: `tool_uses` carries `session_id` and nothing about who ran it, and
// `source_agent` lives on `sessions`. The gate resolves that join perfectly
// (internal/query/sqlgate/join_test.go proves it over nine real shapes).
//
// What was missing was upstream again: the prompt listed the tables and their
// columns and never said HOW THEY CONNECT, so a model asked about tools by
// agent had no way to know it has to cross `session_id`. It guessed.
//
// The connections are declared in the DDL already, as REFERENCES clauses. They
// come from the same single read as everything else.
func TestTheSchemaDeclaresHowTheTablesJoin(t *testing.T) {
	schema := ReadSchema(data.Schema, sqlgate.HiddenTables())
	described := schema.Describe(nil)

	for _, join := range []string{
		"tool_uses.session_id = sessions.session_id",
		"exchanges.session_id = sessions.session_id",
		"thinking_blocks.session_id = sessions.session_id",
	} {
		if !strings.Contains(described, join) {
			t.Errorf("the schema does not declare the join %q:\n%s", join, described)
		}
	}
}

// Every join it declares has to be real on both sides, or it is the same defect
// as before wearing another hat.
func TestEveryDeclaredJoinExistsOnBothSides(t *testing.T) {
	schema := ReadSchema(data.Schema, sqlgate.HiddenTables())

	if len(schema.Joins) == 0 {
		t.Fatal("no join was read out of the DDL: this test would measure nothing")
	}
	for _, join := range schema.Joins {
		if !hasTable(schema, join.From.Table) || !hasTable(schema, join.To.Table) {
			t.Errorf("the join %s names a table that is not visible", join)
			continue
		}
		if !slices.Contains(columnsOf(schema, join.From.Table), join.From.Column) {
			t.Errorf("the join %s names a column %q that %q does not have",
				join, join.From.Column, join.From.Table)
		}
		if !slices.Contains(columnsOf(schema, join.To.Table), join.To.Column) {
			t.Errorf("the join %s names a column %q that %q does not have",
				join, join.To.Column, join.To.Table)
		}
	}
}

// A join towards a table the gate hides is not offered: it would be a route to
// an answer that never runs.
func TestAJoinTowardsAHiddenTableIsNotDeclared(t *testing.T) {
	ddl := `
CREATE TABLE sessions (
  session_id TEXT PRIMARY KEY
);

CREATE TABLE ingest_file_state (
  path TEXT PRIMARY KEY,
  session_id TEXT REFERENCES sessions(session_id)
);

CREATE TABLE tool_uses (
  id INTEGER PRIMARY KEY,
  session_id TEXT REFERENCES sessions(session_id),
  state_path TEXT REFERENCES ingest_file_state(path)
);
`
	schema := ReadSchema(ddl, []string{"ingest_file_state"})
	described := schema.Describe(nil)

	if strings.Contains(described, "ingest_file_state") {
		t.Fatalf("it offers a join towards a table the gate hides:\n%s", described)
	}
	if !strings.Contains(described, "tool_uses.session_id = sessions.session_id") {
		t.Fatalf("it dropped the legitimate join too:\n%s", described)
	}
}

// And the rule that makes the model use them instead of inventing a column.
func TestTheRulesForbidAssumingAColumnThatIsNotListed(t *testing.T) {
	rules := rulesOf(t, SQLSystemPrompt(ReadSchema(data.Schema, sqlgate.HiddenTables()), nil, nil))
	if !strings.Contains(strings.ToLower(rules), "join") {
		t.Fatalf("nothing tells the model to reach a foreign column with a join:\n%s", rules)
	}
}

// THE THIRD FAILURE, AND IT IS NOT THE SCHEMA'S.
//
// `which project had the longest sessions last month` produced SQL the gate
// ACCEPTED: `... WHERE started_at >= datetime('last month') ...`. SQLite has no
// such modifier, so that call is NULL, the comparison is never true and the
// query returns nothing. Valid SQL that cannot match anything is worse than
// rejected SQL, because nothing complains.
//
// The engine cannot catch this and the schema cannot either: it is the dialect.
// So the prompt states it, once, with the form that works.
func TestTheRulesTeachTheRelativeDateThatSQLiteActuallyUnderstands(t *testing.T) {
	rules := rulesOf(t, SQLSystemPrompt(ReadSchema(data.Schema, sqlgate.HiddenTables()), nil, nil))

	if !strings.Contains(rules, "datetime('now'") {
		t.Fatalf("the rules do not teach the modifier that works:\n%s", rules)
	}
	if !strings.Contains(strings.ToLower(rules), "last month") &&
		!strings.Contains(strings.ToLower(rules), "relative date") {
		t.Errorf("the rule does not say what it is for:\n%s", rules)
	}
}

// THE SUBSTRING-LIKE DISEASE.
//
// Verified on the real corpus: the model wrote
//
//	content LIKE '%Edu%' OR metadata LIKE '%Edu%'
//
// and every hit was noise ("redundante", "deduplication", "extractViewedUserId").
// The lab documented the same failure in March. The prompt has to put the FTS
// tables in front of the model, forbid bare %term% LIKE on text columns, and
// show the MATCH + bm25 shape that actually works — including the multi-source
// breadth the compiler templates already use.
func productSchema() Schema {
	return ReadSchema(data.Schema+"\n"+data.SearchSchema, sqlgate.HiddenTables())
}

func TestReadSchemaOffersTheFTSTablesFromTheSearchDDL(t *testing.T) {
	schema := productSchema()
	for _, table := range []string{"memories_fts", "exchanges_fts", "thinking_fts", "sessions_fts"} {
		if !hasTable(schema, table) {
			t.Errorf("the product schema does not offer %q", table)
		}
	}
	if hasTable(schema, "search_state") {
		t.Fatal("the search internals are not for the model")
	}
	if !slices.Contains(columnsOf(schema, "exchanges_fts"), "human_text") ||
		!slices.Contains(columnsOf(schema, "exchanges_fts"), "agent_text") {
		t.Fatalf("exchanges_fts columns: %v", columnsOf(schema, "exchanges_fts"))
	}
}

func TestThePromptSteersTermSearchToFTSNotSubstringLike(t *testing.T) {
	prompt := SQLSystemPrompt(productSchema(), nil, nil)
	lower := strings.ToLower(prompt)

	for _, needle := range []string{
		"memories_fts", "exchanges_fts", "thinking_fts",
		"match", "bm25",
	} {
		if !strings.Contains(lower, needle) {
			t.Errorf("the prompt does not teach %q:\n%s", needle, prompt)
		}
	}
	if !strings.Contains(lower, "like") || !strings.Contains(lower, "%") {
		t.Errorf("the prompt does not warn against substring LIKE:\n%s", prompt)
	}
	// Worked example: quoted token MATCH, not a bare word the FTS parser owns.
	if !strings.Contains(prompt, `MATCH '"edu"'`) && !strings.Contains(prompt, `MATCH '"Edu"'`) {
		t.Errorf("the prompt carries no worked MATCH example:\n%s", prompt)
	}
	// Breadth: memories + exchanges (+ thinking), the compiler's own surface.
	if !strings.Contains(lower, "union") {
		t.Errorf("the prompt does not show multi-source UNION search:\n%s", prompt)
	}
	// Rank by relevance unless the question is temporal.
	if !strings.Contains(lower, "created_at") {
		t.Errorf("the prompt does not contrast bm25 with created_at ordering:\n%s", prompt)
	}
}

func TestSubstringLikeRejectionCatchesTheEduDisease(t *testing.T) {
	cases := []struct {
		sql    string
		reject bool
	}{
		{`SELECT content FROM memories WHERE content LIKE '%Edu%' ORDER BY created_at DESC LIMIT 20`, true},
		{`SELECT * FROM memories WHERE content LIKE '%Edu%' OR metadata LIKE '%Edu%' LIMIT 10`, true},
		{`SELECT human_text FROM exchanges WHERE human_text LIKE '%edu%' LIMIT 5`, true},
		// Prefix-only LIKE is not the disease (task notifications, project filters).
		{`SELECT * FROM exchanges WHERE human_text NOT LIKE '<task-notification%' LIMIT 5`, false},
		{`SELECT * FROM sessions WHERE project LIKE 'la-roca%' LIMIT 5`, false},
		// FTS is what we want.
		{`SELECT rowid FROM memories_fts WHERE memories_fts MATCH '"edu"' LIMIT 10`, false},
		// Counts and plain filters are fine.
		{`SELECT COUNT(*) FROM exchanges LIMIT 1`, false},
	}
	for _, c := range cases {
		got := SubstringLikeRejection(c.sql)
		if c.reject && got == "" {
			t.Errorf("missed the disease:\n%s", c.sql)
		}
		if !c.reject && got != "" {
			t.Errorf("false positive (%s):\n%s", got, c.sql)
		}
	}
	hint := SubstringLikeRejection(`SELECT content FROM memories WHERE content LIKE '%Edu%' LIMIT 5`)
	for _, needle := range []string{"MATCH", "memories_fts", "bm25"} {
		if !strings.Contains(hint, needle) {
			t.Errorf("the rejection hint does not steer to %q: %s", needle, hint)
		}
	}
}
