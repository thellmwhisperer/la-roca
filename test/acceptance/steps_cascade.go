//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Steps for the query cascade and for teach. They are still black box: not one
// symbol of the product is imported here, only `roca` is run and its output and
// its database are read.

// --- worlds ---

// longMemory seeds the memory the truncation-budget scenario needs, with the
// search match inside it and filler around it.
func (m *world) longMemory(minimum int) error {
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	content := "esta es una memoria muy larga de prueba. " +
		strings.Repeat("relleno para pasar del presupuesto de truncado. ", minimum/40+10)
	if len(content) <= minimum {
		return fmt.Errorf("the seeded memory is %d long and the scenario asks for more than %d",
			len(content), minimum)
	}
	if _, err := db.Exec(
		"INSERT INTO memories (layer, content, origin) VALUES ('project', ?, 'agent')",
		content); err != nil {
		return err
	}
	m.memories++
	return nil
}

// aHandoffMemoryAbout seeds a session-continuity handoff that free-text search
// must still find: handoff is not private messaging.
func (m *world) aHandoffMemoryAbout(about string) error {
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	content := "traspaso donde dejamos " + about + " para el siguiente agente"
	if _, err := db.Exec(
		"INSERT INTO memories (layer, content, origin) VALUES ('handoff', ?, 'agent')",
		content); err != nil {
		return err
	}
	m.memories++
	return nil
}

// hasNeverAnswered clears the working cache so the next query pays the cold
// load of the personal classifier. The personal artefact next to the database
// may already exist (init/calibrate train it); what the scenario measures is
// that the first answer still lands under the budget when nothing is warm in
// this process.
func (m *world) hasNeverAnswered() error {
	_ = os.RemoveAll(m.classifierCache())
	return nil
}

// --- execution ---

// iRunTheSQLItReturned takes the SQL from the previous answer and passes it
// through `roca exec`, which is exactly what an operator does with `--sql-only`.
func (m *world) iRunTheSQLItReturned() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	stmt, _ := document["sql"].(string)
	if stmt == "" {
		return fmt.Errorf("the previous answer carried no SQL: %s", m.last.stdout)
	}
	_, err = m.runWith("roca exec "+stmt, []string{"exec", stmt, "--json"})
	return err
}

// iRestartTheRuntime does nothing, and that is the contract: La Roca has no
// daemon, so every command is already a new process and the previous scenario
// left none alive. The step exists so the frozen suite runs without being
// touched, and so that the property is visibly met by construction.
func (m *world) iRestartTheRuntime() error { return nil }

func (m *world) iDeleteTheClassifierCache() error {
	return os.RemoveAll(m.classifierCache())
}

func (m *world) classifierCache() string {
	return filepath.Join(m.home, ".roca", "cache")
}

// --- assertions ---

func (m *world) jsonKeyNotEmpty(key string) error {
	document, err := m.json()
	if err != nil {
		return err
	}
	value, ok := document[key]
	if !ok || value == nil {
		return fmt.Errorf("the JSON output has no %q: %v", key, keys(document))
	}
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s comes back empty", key)
		}
	case map[string]any:
		if len(v) == 0 {
			return fmt.Errorf("%s comes back empty", key)
		}
	case []any:
		if len(v) == 0 {
			return fmt.Errorf("%s comes back empty", key)
		}
	}
	return nil
}

func (m *world) jsonKeyLessThan(key string, cap int) error {
	document, err := m.json()
	if err != nil {
		return err
	}
	value, ok := document[key].(float64)
	if !ok {
		return fmt.Errorf("the JSON output has no numeric %q: %v", key, keys(document))
	}
	if value >= float64(cap) {
		return fmt.Errorf("%s = %v, want less than %d", key, value, cap)
	}
	return nil
}

func (m *world) jsonKeyNotEqualTo(key, value string) error {
	document, err := m.json()
	if err != nil {
		return err
	}
	if fmt.Sprint(document[key]) == value {
		return fmt.Errorf("%s = %q, and the scenario asks for it to be different", key, value)
	}
	return nil
}

// withoutModelCall: the answer did not leave by the model and does not claim to
// have used it. This binary does not link any provider either, so a call would
// be impossible; what the step checks is that it does not pretend to have made
// one.
func (m *world) withoutModelCall() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	if path := fmt.Sprint(document["path"]); path == "llm_fallback" {
		return fmt.Errorf("the answer left by the model")
	}
	if _, ok := document["provider"]; ok {
		return fmt.Errorf("the answer declares a model provider: %v", document["provider"])
	}
	return nil
}

// withoutDataQuery: with --sql-only the answer carries neither rows nor
// columns. That is what a black-box test can assert about "no data was
// queried"; that the read connection is not even opened is pinned by the
// service's unit test.
func (m *world) withoutDataQuery() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	if rows, ok := document["rows"]; ok && rows != nil {
		return fmt.Errorf("the answer carries rows: %v", rows)
	}
	if columns, ok := document["columns"]; ok && columns != nil {
		return fmt.Errorf("the answer carries columns: %v", columns)
	}
	if count, ok := document["row_count"].(float64); ok && count != 0 {
		return fmt.Errorf("row_count = %v", count)
	}
	return nil
}

// rowsEqualToTheDirectQuery: the SQL --sql-only prints has to return through
// `roca exec` the same thing the whole query returns on its own.
func (m *world) rowsEqualToTheDirectQuery() error {
	byExec, err := m.json()
	if err != nil {
		return err
	}
	question := questionOf(m.previous.command)
	if question == "" {
		return fmt.Errorf("I do not know what question was asked before: %q", m.previous.command)
	}
	if _, err := m.runWith("roca query "+question, []string{"query", question, "--json"}); err != nil {
		return err
	}
	direct, err := m.json()
	if err != nil {
		return err
	}
	if !sameRows(byExec["rows"], direct["rows"]) {
		return fmt.Errorf("the rows differ:\n exec: %v\n query: %v",
			byExec["rows"], direct["rows"])
	}
	return nil
}

func (m *world) isNotTheMemoryTotal() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	rows, _ := document["rows"].([]any)
	if len(rows) != 1 {
		return nil // an answer that is not a single figure is not the total
	}
	first, _ := rows[0].(map[string]any)
	total, ok := first["total"].(float64)
	if !ok {
		return nil
	}
	if int(total) == m.memories {
		return fmt.Errorf("the answer is the memory total (%d) and the question asked for a filter",
			m.memories)
	}
	return nil
}

// filtersOrDeclines resolves the two alternatives the scenario declares: either
// the SQL carries the filter, or the fast route declines. The scenario's two
// "either/or else" steps check the same disjunction, which is what they mean
// together.
func (m *world) filtersOrDeclines() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	stmt, _ := document["sql"].(string)
	if strings.Contains(strings.ToLower(stmt), "like") {
		return nil
	}
	if fmt.Sprint(document["path"]) != "compiler" {
		return nil
	}
	return fmt.Errorf("the fast route answered without filtering by the term: %s", stmt)
}

func (m *world) allRowsBelongToTheLayer(layer string) error {
	rows, err := m.rowsOfTheAnswer()
	if err != nil {
		return err
	}
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		id, ok := row["id"].(float64)
		if !ok {
			return fmt.Errorf("a row does not say which memory it comes from: %v", row)
		}
		var own string
		if err := db.QueryRow("SELECT layer FROM memories WHERE id = ?", int(id)).Scan(&own); err != nil {
			return fmt.Errorf("row %d is not a memory: %w", int(id), err)
		}
		if own != layer {
			return fmt.Errorf("memory %d is in layer %q, not in %q", int(id), own, layer)
		}
	}
	return nil
}

func (m *world) noFieldExceeds(cap int) error {
	rows, err := m.rowsOfTheAnswer()
	if err != nil {
		return err
	}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		for key, value := range row {
			text, isText := value.(string)
			if isText && len([]rune(text)) > cap {
				return fmt.Errorf("%s takes %d characters, and the budget is %d",
					key, len([]rune(text)), cap)
			}
		}
	}
	return nil
}

func (m *world) theTextKeepsTheMatch() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	plan, _ := document["queryplan"].(map[string]any)
	term, _ := plan["term"].(string)
	if term == "" {
		return fmt.Errorf("the answer does not say which term it searched by: %v", plan)
	}
	rows, _ := document["rows"].([]any)
	if len(rows) == 0 {
		return fmt.Errorf("there are no rows to check")
	}
	first, _ := rows[0].(map[string]any)
	text := strings.ToLower(fmt.Sprint(first["text"]))
	for _, part := range strings.Split(term, "+") {
		if part != "" && !strings.Contains(text, strings.ToLower(part)) {
			return fmt.Errorf("the truncation ate %q: %q", part, text)
		}
	}
	return nil
}

func (m *world) theMemoriesTableStillExists() error {
	if _, err := m.countMemories(); err != nil {
		return fmt.Errorf("the memories table is gone: %w", err)
	}
	return nil
}

func (m *world) aSingleTaughtExample() error {
	question := taughtQuestionOf(m.last.command)
	if question == "" {
		return fmt.Errorf("I do not know what question was taught: %q", m.last.command)
	}
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM queryplan_teach_examples WHERE question = ?", question).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("there are %d taught examples for %q, want 1", n, question)
	}
	return nil
}

func (m *world) namesTheUnknownTemplate() error {
	return m.outputContains("plantilla_inventada")
}

func (m *world) itListsTheAvailableTemplates() error {
	// Naming several of the ones that do exist is enough: if it named only
	// one, the operator would still not know which ones they can use.
	for _, template := range []string{"count_memories", "search_all_sources_by_term"} {
		if err := m.outputContains(template); err != nil {
			return err
		}
	}
	return nil
}

func (m *world) noExampleStored() error {
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM queryplan_teach_examples").Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("%d examples were stored after a rejection", n)
	}
	return nil
}

// cleanOutput: what goes to stdout is the answer and nothing else. The cost of
// preparing the classifier must not show up as noise in front of the JSON.
func (m *world) cleanOutput() error {
	if _, err := m.json(); err != nil {
		return err
	}
	if strings.TrimSpace(m.last.stderr) != "" {
		return fmt.Errorf("the answer comes with noise on stderr: %s", m.last.stderr)
	}
	return nil
}

// --- helpers ---

func sameRows(a, b any) bool {
	oneA, errA := json.Marshal(a)
	oneB, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(oneA) == string(oneB)
}

// questionOf pulls the quoted question out of one of the suite's commands.
func questionOf(command string) string {
	return quoted(command)
}

// taughtQuestionOf pulls out whatever follows --question.
func taughtQuestionOf(command string) string {
	i := strings.Index(command, "--question ")
	if i < 0 {
		return ""
	}
	return quoted(command[i:])
}

func quoted(text string) string {
	start := strings.IndexAny(text, "'\"")
	if start < 0 {
		return ""
	}
	quote := text[start]
	end := strings.IndexByte(text[start+1:], quote)
	if end < 0 {
		return ""
	}
	return text[start+1 : start+1+end]
}
