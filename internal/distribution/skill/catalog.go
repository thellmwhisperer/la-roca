package skill

import (
	"fmt"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

// CatalogBody composes the roca-semantica skill from the semantic fragments of
// every installed plugin manifest — the same fragments the query catalog
// composes its schema from. The catalog is a lazy-loaded second skill because
// only the agents that open it pay for its size; the main skill stays small.
//
// databases are the validated plugin databases discovery found, in discovery
// order; notes names what could not serve a query, one line each, so the
// catalog never quietly pretends a broken plugin is queryable.
func CatalogBody(databases []plugin.Database, notes []string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + CatalogName + "\n")
	b.WriteString("description: >\n")
	b.WriteString("  Generated catalog of the plugin databases La Roca queries: what each\n")
	b.WriteString("  knows, its tables, and its example questions. Load it only when you\n")
	b.WriteString("  need to know which tables or domains exist, or when you will write\n")
	b.WriteString("  exact SQL yourself. This catalog is for authors of `roca exec` SELECTs.\n")
	b.WriteString("  `roca query` and `roca explore` are last resort.\n")
	b.WriteString("---\n\n")
	b.WriteString("# La Roca semantic catalog\n\n")
	b.WriteString("Composed from the semantic fragments of every installed plugin manifest,\n")
	b.WriteString("the same fragments natural-language search composes its catalog from.\n")
	b.WriteString("`roca skill install` regenerates this file, and every `roca plugin\n")
	b.WriteString("install`, `update` and `uninstall` refreshes it. Reach a table through\n")
	b.WriteString("its database's attach alias, for example alias.table in a `roca exec`\n")
	b.WriteString("SELECT.\n\n")
	b.WriteString("For the zero-inference hybrid loop, `roca vector query --databases ...`\n")
	b.WriteString("discovers nearby rows across the selected federated databases; then FTS\n")
	b.WriteString("counts and SQL frames the claim in each hit's database. A `Vector:` line\n")
	b.WriteString("below names the stable source id and opt-in prose columns. Without one,\n")
	b.WriteString("the table keeps exactly its existing FTS/SQL behavior.\n\n")
	if len(databases) == 0 && len(notes) == 0 {
		b.WriteString("## No plugin databases installed\n\n")
		b.WriteString("The kernel is installed without plugin databases. `roca plugin install\n")
		b.WriteString("<local dir | git URL | owner/repo>` adds one, and its tables appear\n")
		b.WriteString("here with the next `roca skill install` or plugin lifecycle run.\n")
		return b.String()
	}
	for _, database := range databases {
		writeDatabase(&b, database)
	}
	if len(notes) > 0 {
		b.WriteString("## Not currently queryable\n\n")
		for _, note := range notes {
			fmt.Fprintf(&b, "- %s\n", note)
		}
	}
	return b.String()
}

// writeDatabase renders one plugin database: what it knows, its questions, and
// each table with its description, alias-qualified name, columns, retrieval
// coverage, and its own example questions.
func writeDatabase(b *strings.Builder, database plugin.Database) {
	heading := database.Name
	if database.DatabaseName != "" && database.DatabaseName != database.Name {
		heading += " — " + database.DatabaseName
	}
	fmt.Fprintf(b, "## %s (alias %s)\n\n", heading, database.Schema)
	fmt.Fprintf(b, "%s\n", database.Semantic.Description)
	if len(database.Semantic.Questions) > 0 {
		b.WriteString("\nQuestions it serves:\n")
		for _, question := range database.Semantic.Questions {
			fmt.Fprintf(b, "- %s\n", question)
		}
	}
	if len(database.VectorTables) == 0 {
		b.WriteString("\nVector coverage: not declared; this database keeps exactly its existing FTS/SQL behavior.\n")
	} else {
		b.WriteString("\nVector coverage: declared below. Tables without a `Vector:` line keep exactly their existing FTS/SQL behavior.\n")
	}
	b.WriteString("\n")
	for _, table := range database.Tables {
		fmt.Fprintf(b, "### %s · %s.%s\n\n", table.Name, database.Schema, table.Name)
		fmt.Fprintf(b, "%s\n\n", table.Description)
		fmt.Fprintf(b, "Columns: %s\n", strings.Join(table.Columns, ", "))
		if declaration, ok := vectorTable(database, table.Name); ok {
			fmt.Fprintf(b, "\nVector: source id `%s`; opt-in text columns: `%s`.\n",
				declaration.IDColumn, strings.Join(declaration.TextColumns, "`, `"))
		}
		if len(table.Questions) > 0 {
			b.WriteString("\nQuestions:\n")
			for _, question := range table.Questions {
				fmt.Fprintf(b, "- %s\n", question)
			}
		}
		b.WriteString("\n")
	}
}

func vectorTable(database plugin.Database, name string) (plugin.VectorTable, bool) {
	for _, table := range database.VectorTables {
		if table.Name == name {
			return table, true
		}
	}
	return plugin.VectorTable{}, false
}
