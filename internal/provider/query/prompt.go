package query

import (
	"regexp"
	"slices"
	"strings"
)

// The schema and rules handed to the model come
// from ONE read of ONE DDL: the same `data.Schema` the gate prepares its
// validation database with, minus the same tables the gate hides. A rule about
// a column can then only name the tables that really carry it, and a column
// nobody carries earns no rule at all. `TestThePromptNeverNamesAColumnTheSchemaDoesNotHave`
// enforces that invariant over the real schema.

// LayerHint is one semantic layer as the model sees it: what it is called and
// what goes in it.
type LayerHint struct {
	Name        string
	Description string
}

// Table is a table as the model may query it.
type Table struct {
	Name    string
	Columns []string
}

// Column is one side of a join.
type Column struct {
	Table  string
	Column string
}

func (c Column) String() string { return c.Table + "." + c.Column }

// Join is a way to get from one table to another. It is read from the DDL's own
// REFERENCES clauses, so it cannot claim a relation the database does not
// declare.
type Join struct {
	From Column
	To   Column
}

func (j Join) String() string { return j.From.String() + " = " + j.To.String() }

// Schema is what the model is allowed to see: the gate's visible tables with
// their real columns, and how those tables connect. It is the single source of
// truth shared by the <schema> block and by the rules, which is what keeps the
// two from drifting apart.
type Schema struct {
	Tables []Table
	Joins  []Join
}

var createTable = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["'` + "`" + `]?(\w+)["'` + "`" + `]?\s*\((.*?)\n\)\s*;`)

// createVirtualFTS reads the FTS5 lexical index tables out of search.sql. They
// are CREATE VIRTUAL TABLE, not CREATE TABLE, so the ordinary reader misses them
// and the model never learns MATCH exists — which is how content LIKE '%Ana%'
// became the default term search.
var createVirtualFTS = regexp.MustCompile(`(?is)CREATE\s+VIRTUAL\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["'` + "`" + `]?(\w+)["'` + "`" + `]?\s+USING\s+fts5\s*\((.*?)\)\s*;`)

// ReadSchema reads the DDL and drops what the gate hides.
//
// hidden must be the gate's own list (`sqlgate.HiddenTables`): offering the
// model a table the gate is going to reject is offering an answer that never
// runs. Pass schema.sql and search.sql concatenated when the model must see the
// FTS tables; the gate already prepares both.
func ReadSchema(ddl string, hidden []string) Schema {
	invisible := set(hidden...)

	var schema Schema
	for _, match := range createTable.FindAllStringSubmatch(ddl, -1) {
		name := match[1]
		if invisible[name] {
			continue
		}
		columns, joins := readBody(name, match[2])
		schema.Tables = append(schema.Tables, Table{Name: name, Columns: columns})
		for _, join := range joins {
			// A join towards a table the gate hides is a route to an answer that
			// never runs, so it is not offered either.
			if invisible[join.To.Table] {
				continue
			}
			schema.Joins = append(schema.Joins, join)
		}
	}
	for _, match := range createVirtualFTS.FindAllStringSubmatch(ddl, -1) {
		name := match[1]
		if invisible[name] {
			continue
		}
		schema.Tables = append(schema.Tables, Table{Name: name, Columns: ftsColumns(match[2])})
	}
	return schema
}

// ftsColumns keeps the indexed text columns and drops the fts5 options
// (content=, content_rowid=, tokenize=).
func ftsColumns(body string) []string {
	var columns []string
	for _, part := range strings.Split(body, ",") {
		part = strings.TrimSpace(part)
		if part == "" || strings.Contains(part, "=") {
			continue
		}
		name := strings.Trim(strings.Fields(part)[0], `"'`+"`")
		if name != "" {
			columns = append(columns, name)
		}
	}
	return columns
}

// TablesWith are the tables that carry a column, in schema order.
func (s Schema) TablesWith(column string) []string {
	var carriers []string
	for _, table := range s.Tables {
		if slices.Contains(table.Columns, column) {
			carriers = append(carriers, table.Name)
		}
	}
	return carriers
}

// HasColumn says some visible table carries that column.
func (s Schema) HasColumn(column string) bool { return len(s.TablesWith(column)) > 0 }

// Describe is the <schema> block: what there is and what the layers mean.
func (s Schema) Describe(layers []LayerHint) string {
	var out strings.Builder
	out.WriteString("Tables you can query, with their columns:\n\n")
	for _, table := range s.Tables {
		out.WriteString("- " + table.Name + "(" + strings.Join(table.Columns, ", ") + ")\n")
	}

	// How the tables connect. Without this a question about tools by agent has
	// no answer the model can write: `tool_uses` carries `session_id` and
	// nothing about who ran it, and a model that is not told the way across
	// invents `tool_uses.source_agent` instead of joining.
	if len(s.Joins) > 0 {
		out.WriteString("\nHow the tables join. To use a column of another table, " +
			"join through one of these:\n\n")
		for _, join := range s.Joins {
			out.WriteString("- " + join.String() + "\n")
		}
	}

	if len(layers) > 0 && s.HasColumn(layerColumn) {
		out.WriteString("\nValues of the `" + layerColumn + "` column of `" +
			strings.Join(s.TablesWith(layerColumn), "`, `") + "`, and what each one holds:\n\n")
		for _, layer := range layers {
			out.WriteString("- " + layer.Name)
			if layer.Description != "" {
				out.WriteString(": " + layer.Description)
			}
			out.WriteString("\n")
		}
	}
	return out.String()
}

var references = regexp.MustCompile(`(?i)REFERENCES\s+["'` + "`" + `]?(\w+)["'` + "`" + `]?\s*\(\s*["'` + "`" + `]?(\w+)`)

// readBody keeps a table's column names and the joins its REFERENCES clauses
// declare.
//
// Only the column name is kept: the type and the constraints are noise for
// whoever has to write a SELECT, and noise in a prompt is tokens paid for and
// attention lost. The reference is the exception, because it is the only thing
// in the DDL that says how to get from one table to another, and a model that
// does not know that invents a column instead of writing a join.
func readBody(table, body string) ([]string, []Join) {
	var columns []string
	var joins []Join

	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		name := strings.Fields(line)[0]
		if isConstraintWord(name) {
			continue
		}
		name = strings.Trim(name, `"'`+"`")
		columns = append(columns, name)

		if match := references.FindStringSubmatch(line); match != nil && match[1] != table {
			joins = append(joins, Join{
				From: Column{Table: table, Column: name},
				To:   Column{Table: match[1], Column: match[2]},
			})
		}
	}
	return columns, joins
}

var constraintWords = set("PRIMARY", "FOREIGN", "UNIQUE", "CHECK", "CONSTRAINT")

func isConstraintWord(word string) bool { return constraintWords[strings.ToUpper(word)] }

// The two columns the rules talk about. They are named here once, and every
// sentence that mentions them is built from what the schema says about them.
const (
	supersededColumn = "supersedes"
	layerColumn      = "layer"
	// provenanceColumn is the one the provenance rule is written from: where it
	// exists, so do the rest of the columns of that shape.
	provenanceColumn = "tokens_in"
)

// provenanceRule says how the per-exchange provenance is filled, which is: from
// what each source itself recorded, and not at all where it recorded nothing. A
// model that does not know it writes AVG(cost_usd) over a corpus where one
// source in seven states a price and answers as if that were everybody.
func provenanceRule(schema Schema) string {
	carriers := schema.TablesWith(provenanceColumn)
	if len(carriers) == 0 {
		return ""
	}
	return "- model, provider, tokens_in, tokens_out, tokens_reasoning and cost_usd of " +
		quotedNames(carriers) + " hold what the source recorded and are NULL wherever it " +
		"recorded nothing, which is normal and not missing data: filter them with IS NOT " +
		"NULL and never read a NULL as a zero"
}

// SQLSystemPrompt is the whole instruction the model receives. The question
// goes separately, as the user's turn: mixing them is what lets a question
// rewrite the rules.
func SQLSystemPrompt(schema Schema, layers []LayerHint, layerFilter []string) string {
	rules := []string{
		"- Only generate SELECT queries (read-only)",
		"- Never use INSERT, UPDATE, DELETE, DROP, ALTER, CREATE, TRUNCATE",
	}
	// Every rule that names a column is written from the schema, so it can only
	// name the tables that really carry it. An empty rule is a column no table
	// has, and it is not written at all.
	if rule := supersededRule(schema); rule != "" {
		rules = append(rules, rule)
	}
	if rule := provenanceRule(schema); rule != "" {
		rules = append(rules, rule)
	}
	rules = append(rules,
		"- Use only the tables and columns listed above, exactly as they are written there")
	if len(schema.Joins) > 0 {
		// The rule that stops the model inventing a column. A table has exactly
		// the columns listed for it; anything else has to be reached with a JOIN
		// from the list, and saying so is cheaper than a rejected query.
		rules = append(rules,
			"- A table has ONLY the columns listed for it. To filter or select by a column "+
				"of another table, add a JOIN from the list above: never assume a table has a "+
				"column that is not listed under its own name")
	}
	rules = append(rules,
		// The dialect, not the schema. `datetime('last month')` is accepted by
		// the parser, evaluates to NULL and makes every comparison false: valid
		// SQL that can never match, which is worse than a rejection because
		// nothing complains. The engine cannot catch it and neither can the
		// gate, so the prompt states the form that works.
		"- For a relative date (last month, this week, yesterday) use SQLite modifiers: "+
			"datetime('now', '-1 month'), datetime('now', '-7 days'). "+
			"datetime('last month') is not SQLite and is silently NULL",
		"- Always end the query with an explicit LIMIT",
		"- Respond ONLY with the SQL query: no explanations, no markdown, no code fences")
	if hasTable(schema, "memories_fts") {
		// Substring LIKE '%Ana%' matches "ganancia" and "banana". The
		// FTS tables are the only honest term search; bm25 ranks, created_at
		// does not unless the question is about time.
		rules = append(rules,
			"- For keyword / who-is / what-about term search use the FTS5 tables "+
				"(`memories_fts`, `exchanges_fts`, `thinking_fts`) with MATCH and "+
				"ORDER BY bm25(...). Never write LIKE '%term%' on content, metadata, "+
				"human_text, agent_text or full_text: that matches inside other words",
			"- Quote each search token in double quotes inside MATCH "+
				`(memories_fts MATCH '"ana"'). When joining an FTS hit to its content `+
				"table, pull rowid inside a subquery as an alias and join on id = alias; "+
				"an FTS query may return unqualified rowid directly, but never write table.rowid",
			"- Rank term search by bm25 relevance, not created_at, unless the question "+
				"is explicitly temporal (last week, yesterday, recent, between dates)",
			"- In mixed term search select source_priority (memory 0, exchange or human 1, "+
				"thinking 2) and ORDER BY source_priority before the bm25 rank; curated "+
				"memories answer before transcript and reasoning echoes",
			"- In a result column named source use exactly memory, exchange, human or thinking",
			"- When returning memory rows, include a compact author column from source_agent, "+
				"source_model and source_surface; render NULL or empty historical values as unknown",
			"- An exchanges_fts hit must match the text you return: when selecting agent_text "+
				"use {agent_text} : (...) inside MATCH, and when selecting human_text use "+
				"{human_text} : (...); never match one column and return the other",
			"- Search memories, exchanges and thinking together with UNION ALL unless "+
				"the question clearly targets one source. Join memory hits to memories for "+
				"supersedes; the external-content exchange and thinking indexes may return "+
				"their own indexed text. Aggregations and counts stay on the base tables")
	}

	return "You are an expert SQL assistant. Given the user's question about the " +
		"La Roca memory database, generate ONLY a single valid SQLite SELECT query.\n\n" +
		"<schema>\n" + schema.Describe(layers) + "\n</schema>\n\n" +
		"<rules>\n" + strings.Join(rules, "\n") + "\n</rules>" +
		ftsExamples(schema) +
		layerInstruction(schema, layerFilter)
}

// ftsExamples is the worked shape the compiler already emits: multi-source
// MATCH, bm25 rank, rowid pulled inside a subquery. Without an example the
// model invents table.rowid (the AST gate rejects it) or falls back to LIKE.
func ftsExamples(schema Schema) string {
	if !hasTable(schema, "memories_fts") {
		return ""
	}
	return "\n\n<examples>\n" +
		"Term search for Ana across sources (token MATCH + bm25 — never LIKE '%Ana%'):\n" +
		"SELECT 'memory' AS source, m.id, COALESCE(NULLIF(m.source_agent, ''), 'unknown') || '/' ||\n" +
		"       COALESCE(NULLIF(m.source_model, ''), 'unknown') || ' via ' ||\n" +
		"       COALESCE(NULLIF(m.source_surface, ''), 'unknown') AS author,\n" +
		"       m.content AS text, 0 AS source_priority, f.rango AS rango\n" +
		"FROM (SELECT rowid AS fila, bm25(memories_fts) AS rango FROM memories_fts\n" +
		"      WHERE memories_fts MATCH '\"ana\"' ORDER BY rango LIMIT 20) AS f\n" +
		"JOIN memories AS m ON m.id = f.fila WHERE m.id NOT IN (SELECT supersedes FROM memories WHERE supersedes IS NOT NULL)\n" +
		"UNION ALL\n" +
		"SELECT 'exchange', rowid, NULL AS author, agent_text, 1 AS source_priority, bm25(exchanges_fts) AS rango\n" +
		"FROM exchanges_fts WHERE exchanges_fts MATCH '{agent_text} : (\"ana\")'\n" +
		"UNION ALL\n" +
		"SELECT 'human', rowid, NULL, human_text, 1, bm25(exchanges_fts)\n" +
		"FROM exchanges_fts WHERE exchanges_fts MATCH '{human_text} : (\"ana\")'\n" +
		"AND human_text NOT LIKE '<task-notification%'\n" +
		"UNION ALL\n" +
		"SELECT 'thinking', rowid, NULL, full_text, 2, bm25(thinking_fts)\n" +
		"FROM thinking_fts WHERE thinking_fts MATCH '\"ana\"'\n" +
		"ORDER BY source_priority, rango LIMIT 20\n\n" +
		"Count on base tables (not FTS), with an explicit LIMIT:\n" +
		"SELECT COUNT(*) AS n FROM exchanges LIMIT 1\n" +
		"</examples>"
}

// hasTable is shared by the prompt builder and the tests that read a schema.
func hasTable(s Schema, name string) bool {
	return slices.ContainsFunc(s.Tables, func(t Table) bool { return t.Name == name })
}

// substringLikeOnText is the bare %term% form on a content-bearing column.
// Prefix-only LIKE (task-notification%, project filters) is not the disease.
var substringLikeOnText = regexp.MustCompile(
	`(?i)\b(content|metadata|human_text|agent_text|full_text|title)\b\s+LIKE\s+'%[^']+%'`)

// SubstringLikeRejection is the narrow defense behind the prompt: a model plan
// that still writes LIKE '%term%' on a text column is rejected with a retry
// hint that points at FTS. It is not a SQL rewriter.
func SubstringLikeRejection(sql string) string {
	if !substringLikeOnText.MatchString(sql) {
		return ""
	}
	return "substring LIKE '%term%' on a text column matches inside other words " +
		"(Ana matches ganancia). For term search use the FTS tables with MATCH " +
		`and ORDER BY bm25(...), e.g. memories_fts MATCH '"ana"'. ` +
		"Search memories_fts, exchanges_fts and thinking_fts with UNION ALL unless " +
		"the question targets one source. Pull rowid inside a subquery; never table.rowid. " +
		"Respond ONLY with the corrected SQL."
}

// supersededRule is the rule that used to be a lie. It names the table that
// really carries the column, says out loud that no other one does, and describes
// the direction of replacement correctly. With no table carrying it, there is
// no rule: a rule about a column that does not exist is the same defect written
// the other way round.
func supersededRule(schema Schema) string {
	carriers := schema.TablesWith(supersededColumn)
	if len(carriers) == 0 {
		return ""
	}
	return "- A memory another row replaces stops answering: exclude the memory referenced " +
		"by another row's `" + supersededColumn + "`, using NOT IN (SELECT supersedes FROM " +
		"memories WHERE supersedes IS NOT NULL). That column exists ONLY in " +
		quotedNames(carriers) + ": never use it on any other table"
}

func layerInstruction(schema Schema, layers []string) string {
	if len(layers) == 0 {
		return ""
	}
	carriers := schema.TablesWith(layerColumn)
	if len(carriers) == 0 {
		// The restriction cannot be imposed on a schema with no such column, and
		// asking for it anyway is asking for SQL the gate will reject.
		return ""
	}

	quoted := make([]string, 0, len(layers))
	for _, layer := range layers {
		// The layer names come from the registry, but the escaping does not
		// depend on trusting them: a name with a quote inside closes the literal,
		// and that is an injection into the very prompt that generates the SQL.
		quoted = append(quoted, literal(layer))
	}
	filter := layerColumn + " = " + quoted[0]
	if len(quoted) > 1 {
		filter = layerColumn + " IN (" + strings.Join(quoted, ", ") + ")"
	}
	return "\n\nIMPORTANT: answer only out of " + quotedNames(carriers) +
		", filtering by " + filter
}

// quotedNames renders a list of identifiers the way the rest of the prompt
// writes them.
func quotedNames(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, "`"+name+"`")
	}
	if len(quoted) == 1 {
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}

// SortedLayerHints keeps the registry's layers in a stable order, so that the
// same installation always sends the same prompt and repeatable tests measure
// the model and not the map's iteration order.
func SortedLayerHints(hints []LayerHint) []LayerHint {
	sorted := slices.Clone(hints)
	slices.SortFunc(sorted, func(a, b LayerHint) int { return strings.Compare(a.Name, b.Name) })
	return sorted
}
