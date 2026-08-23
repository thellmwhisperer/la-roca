//go:build acceptance

package acceptance

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	_ "modernc.org/sqlite"
)

func registerProviderQuerySteps(ctx *godog.ScenarioContext, w *providerAcceptanceWorld) {
	ctx.Given(`^the memory "([^"]*)" exists$`, w.memoryExists)
	ctx.Given(`^the synthetic plugin "([^"]*)" is installed$`, w.syntheticPluginInstalled)
	ctx.When(`^I ask "([^"]*)"$`, w.ask)
	ctx.When(`^I ask only for SQL for "([^"]*)"$`, w.askForSQL)
	ctx.When(`^I submit the SQL "([^"]*)"$`, w.submitSQL)
	ctx.When(`^I submit these statements to the SQL gate:$`, w.submitStatements)
	ctx.Then(`^the result used the literal search path$`, w.usedLiteralSearch)
	ctx.Then(`^one row contains "([^"]*)"$`, w.oneRowContains)
	ctx.Then(`^one statement is accepted and one is blocked$`, w.oneAcceptedOneBlocked)
	ctx.Then(`^the database still contains (\d+) memor(?:y|ies)$`, w.databaseContainsMemories)
	ctx.Then(`^the returned SQL contains "([^"]*)"$`, w.returnedSQLContains)
	ctx.Then(`^the returned SQL starts with SELECT$`, w.returnedSQLStartsWithSelect)
	ctx.Then(`^no rows were returned$`, w.noRowsReturned)
	ctx.Then(`^zero rows are reported$`, w.zeroRowsReported)
	ctx.Then(`^the literal rescue is reported$`, w.literalRescueReported)
	ctx.Then(`^the result used the model SQL path$`, w.usedModelSQL)
	ctx.Then(`^exactly (\d+) rows? (?:is|are) reported$`, w.exactRowsReported)
	ctx.Then(`^the degraded reason is "([^"]*)"$`, w.degradedReason)
	ctx.Then(`^the consulted databases are "([^"]*)"$`, w.consultedDatabases)
	ctx.Then(`^the first row declares database "([^"]*)"$`, w.firstRowDatabase)
	ctx.Then(`^a warning names the plugin "([^"]*)" and column "([^"]*)"$`, w.pluginWarning)
}

func (w *providerAcceptanceWorld) syntheticPluginInstalled(name string) error {
	w.pluginsEnabled = true
	source := filepath.Join("..", "..", "testdata", "plugin-standard", name)
	destination := filepath.Join(w.home, ".roca", "plugins", name)
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	semantic, err := os.ReadFile(filepath.Join(source, plugin.SemanticFilename))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(destination, plugin.SemanticFilename), semantic, 0o600); err != nil {
		return err
	}
	ddl, err := os.ReadFile(filepath.Join(source, "schema.sql"))
	if err != nil {
		return err
	}
	database, err := sql.Open("sqlite", filepath.Join(destination, "plugin.db"))
	if err != nil {
		return err
	}
	if _, err := database.Exec(string(ddl)); err != nil {
		database.Close()
		return err
	}
	return database.Close()
}

func (w *providerAcceptanceWorld) consultedDatabases(want string) error {
	document, err := w.lastJSON()
	if err != nil {
		return err
	}
	values, _ := document["databases"].([]any)
	got := make([]string, 0, len(values))
	for _, value := range values {
		got = append(got, fmt.Sprint(value))
	}
	if strings.Join(got, ", ") != want {
		return fmt.Errorf("consulted databases = %q, want %q", strings.Join(got, ", "), want)
	}
	return nil
}

func (w *providerAcceptanceWorld) firstRowDatabase(want string) error {
	document, err := w.lastJSON()
	if err != nil {
		return err
	}
	rows := objectList(document["rows"])
	if len(rows) == 0 || fmt.Sprint(rows[0]["database"]) != want {
		return fmt.Errorf("first row database = %v, want %q", rows, want)
	}
	return nil
}

func (w *providerAcceptanceWorld) pluginWarning(name, column string) error {
	document, err := w.lastJSON()
	if err != nil {
		return err
	}
	warnings, _ := document["warnings"].([]any)
	for _, warning := range warnings {
		text := fmt.Sprint(warning)
		if strings.Contains(text, name) && strings.Contains(text, column) {
			return nil
		}
	}
	return fmt.Errorf("warnings do not name plugin %q and column %q: %v", name, column, warnings)
}

func (w *providerAcceptanceWorld) usedModelSQL() error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	if doc["path"] != "model" || strings.TrimSpace(fmt.Sprint(doc["sql"])) == "" {
		return fmt.Errorf("model SQL path not declared: %s", w.last.stdout)
	}
	return nil
}

func (w *providerAcceptanceWorld) exactRowsReported(want int) error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	if got := intValue(doc["row_count"]); got != want {
		return fmt.Errorf("row_count = %d, want %d: %s", got, want, w.last.stdout)
	}
	return nil
}

func (w *providerAcceptanceWorld) degradedReason(want string) error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	if got := fmt.Sprint(doc["degraded"]); got != want {
		return fmt.Errorf("degraded = %q, want %q: %s", got, want, w.last.stdout)
	}
	return nil
}

func (w *providerAcceptanceWorld) memoryExists(content string) error {
	if err := w.mustRun("store", "--layer", "project", "--content", content, "--origin", "agent", "--json"); err != nil {
		return err
	}
	return w.mustRun("index", "--json")
}

func (w *providerAcceptanceWorld) ask(question string) error {
	return w.run("playground", question, "--json")
}

func (w *providerAcceptanceWorld) askForSQL(question string) error {
	return w.run("playground", question, "--sql-only", "--json")
}

func (w *providerAcceptanceWorld) submitSQL(statement string) error {
	return w.run("exec", statement, "--json")
}

func (w *providerAcceptanceWorld) submitStatements(table *godog.Table) error {
	w.statements = nil
	for _, row := range table.Rows[1:] {
		if len(row.Cells) != 1 {
			return fmt.Errorf("SQL table row has %d cells, want 1", len(row.Cells))
		}
		if err := w.submitSQL(row.Cells[0].Value); err != nil {
			return err
		}
		w.statements = append(w.statements, w.last)
	}
	return nil
}

func (w *providerAcceptanceWorld) usedLiteralSearch() error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	if doc["path"] != "keyword" || doc["retried"] != true {
		return fmt.Errorf("literal route not declared: %s", w.last.stdout)
	}
	return nil
}

func (w *providerAcceptanceWorld) oneRowContains(text string) error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	rows := objectList(doc["rows"])
	if len(rows) != 1 || !strings.Contains(fmt.Sprint(rows[0]["text"]), text) {
		return fmt.Errorf("rows do not contain one %q match: %v", text, rows)
	}
	return nil
}

func (w *providerAcceptanceWorld) oneAcceptedOneBlocked() error {
	if len(w.statements) != 2 || w.statements[0].code != 0 || w.statements[1].code == 0 {
		return fmt.Errorf("gate outcomes = %+v, want accepted then blocked", w.statements)
	}
	if !strings.Contains(w.statements[1].stdout+w.statements[1].stderr, "Only SELECT") {
		return fmt.Errorf("blocked statement did not name the SELECT rule: %+v", w.statements[1])
	}
	return nil
}

func (w *providerAcceptanceWorld) databaseContainsMemories(want int) error {
	before := w.last
	if err := w.run("exec", "SELECT COUNT(*) AS total FROM memories", "--json"); err != nil {
		return err
	}
	doc, err := w.lastJSON()
	w.last = before
	if err != nil {
		return err
	}
	rows := objectList(doc["rows"])
	if len(rows) != 1 || intValue(rows[0]["total"]) != want {
		return fmt.Errorf("memory count = %v, want %d", rows, want)
	}
	return nil
}

func (w *providerAcceptanceWorld) returnedSQLContains(part string) error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	if !strings.Contains(fmt.Sprint(doc["sql"]), part) {
		return fmt.Errorf("SQL does not contain %q: %v", part, doc["sql"])
	}
	return nil
}

func (w *providerAcceptanceWorld) returnedSQLStartsWithSelect() error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(fmt.Sprint(doc["sql"]))), "SELECT") {
		return fmt.Errorf("SQL does not start with SELECT: %v", doc["sql"])
	}
	return nil
}

func (w *providerAcceptanceWorld) noRowsReturned() error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	if intValue(doc["row_count"]) != 0 || len(objectList(doc["rows"])) != 0 || doc["match"] != nil {
		return fmt.Errorf("SQL-only returned or matched rows: %s", w.last.stdout)
	}
	return nil
}

func (w *providerAcceptanceWorld) zeroRowsReported() error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	if intValue(doc["row_count"]) != 0 || doc["match"] != "empty" {
		return fmt.Errorf("zero was not declared honestly: %s", w.last.stdout)
	}
	return nil
}

func (w *providerAcceptanceWorld) literalRescueReported() error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	if !strings.Contains(fmt.Sprint(doc["message"]), "falling back to literal term search") {
		return fmt.Errorf("literal rescue is not reported: %s", w.last.stdout)
	}
	return nil
}
