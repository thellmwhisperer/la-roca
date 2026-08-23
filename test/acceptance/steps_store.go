//go:build acceptance

package acceptance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/cucumber/godog"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

// registerStoreSteps wires the curated step vocabulary of the STORE domain.
//
// It reuses the black-box world shared by the journey suite (run, record, the
// environment, openDB, the JSON readers and the exit-code checks): the store
// domain is the same product spoken of in fewer words, not a second harness.
// What is new here is only the store-domain Given/When/Then and the fixtures
// they stand up, each built in code from synthetic content.
func registerStoreSteps(ctx *godog.ScenarioContext, binary string) {
	m := &world{binary: binary}

	ctx.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
		return c, m.freshSandbox()
	})
	ctx.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		m.closeModels()
		_ = os.RemoveAll(m.home)
		return c, nil
	})

	// --- given: the world a scenario starts in ---
	ctx.Given(`^a clean HOME$`, func() error { return nil })
	ctx.Given(`^a fresh Roca database$`, m.freshDatabase)
	ctx.Given(`^a Roca database that needs one structural repair$`, m.databaseNeedingRepair)
	ctx.Given(`^the model always defers to literal search$`, m.modelDefersToLiteralSearch)
	ctx.Given(`^a memory in layer "([^"]*)" with content "([^"]*)"$`, m.storeMemoryFixture)
	ctx.Given(`^a clean HOME with a "([^"]*)" file at the database path$`, m.foreignOrCorruptFile)
	ctx.Given(`^a home Roca database containing a memory unique to home$`, m.homeDatabaseWithUniqueMemory)
	ctx.Given(`^another Roca database containing a memory unique to that database$`, m.otherDatabaseWithUniqueMemory)
	ctx.Given(`^a selected path where no Roca database exists$`, m.missingDatabasePath)

	// --- when: the one action the scenario exercises ---
	ctx.When(`^I initialize the database$`, m.runInit)
	ctx.When(`^I initialize the database again$`, m.runInit)
	ctx.When(`^I initialize that database$`, m.runInit)
	ctx.When(`^I initialize the database without JSON$`, m.initializeDatabasePlain)
	ctx.When(`^I store a memory in layer "([^"]*)" with content "([^"]*)"$`, m.storeMemoryFixture)
	ctx.When(`^I store a memory in layer "([^"]*)" with origin "([^"]*)", project "([^"]*)" and content "([^"]*)"$`,
		m.storeMemoryFull)
	ctx.When(`^I store a memory in layer "([^"]*)" with empty content$`, m.storeMemoryEmpty)
	ctx.When(`^I store a memory in layer "([^"]*)" superseding the previous one with content "([^"]*)"$`,
		m.storeSupersedingPrevious)
	ctx.When(`^I store a memory in layer "([^"]*)" superseding memory (\d+) with content "([^"]*)"$`,
		m.storeSupersedingID)
	ctx.When(`^I search for "([^"]*)"$`, m.searchFor)
	ctx.When(`^I search the other database for its unique memory$`, m.searchOtherDatabase)
	ctx.When(`^I search without choosing a database for the command$`, m.searchHomeDatabase)
	ctx.When(`^I search that database$`, m.searchMissingDatabase)
	ctx.When(`^two writers store different memories at the same time$`, m.twoConcurrentWriters)

	// --- then: properties the scenario asserts ---
	ctx.Then(`^the command exits with code (\d+)$`, m.itExitsWithCode)
	ctx.Then(`^the command exits with a code other than 0$`, m.itExitsWithNonZeroCode)
	ctx.Then(`^the output contains "([^"]*)"$`, m.outputContains)
	ctx.Then(`^the output names "([^"]*)"$`, m.outputContains)
	ctx.Then(`^the JSON output declares the match was empty$`, m.jsonDeclaresEmptyMatch)
	ctx.Then(`^the database holds every v1 table$`, m.holdsEveryV1Table)
	ctx.Then(`^the output is plain text, not JSON$`, m.plainTextNotJSON)
	ctx.Then(`^init reports the word search round trip$`, m.wordSearchWasProved)
	ctx.Then(`^init never reports the word index as broken$`, m.wordIndexNeverBroken)
	ctx.Then(`^init did not start reading the history for meaning$`, m.meaningPassNotStarted)
	ctx.Then(`^the configuration leaves the meaning pass switched off$`, m.meaningPassSwitchedOff)
	ctx.Then(`^the memory is still there$`, m.memoryStillThere)
	ctx.Then(`^a backup was taken before the repair$`, m.backupWasTaken)
	ctx.Then(`^the repair left the schema current$`, m.schemaLeftCurrent)
	ctx.Then(`^exactly one dated backup file is left$`, m.oneDatedBackupFile)
	ctx.Then(`^the backup restores to the same memory count$`, m.backupRestoresSameCount)
	ctx.Then(`^the stored memory has layer "([^"]*)", origin "([^"]*)" and project "([^"]*)"$`,
		m.storedMemoryHas)
	ctx.Then(`^the search returns at least one result$`, m.searchReturnsAtLeastOne)
	ctx.Then(`^the search returns no results$`, m.searchReturnsNone)
	ctx.Then(`^the search returns exactly (\d+) results?$`, m.searchReturnsExactly)
	ctx.Then(`^the first result contains "([^"]*)"$`, m.firstResultContains)
	ctx.Then(`^no result contains "([^"]*)"$`, m.noResultContains)
	ctx.Then(`^the output names the missing content$`, m.namesMissingContent)
	ctx.Then(`^the output names the refused write$`, m.namesRefusedWrite)
	ctx.Then(`^the search returns the memory from the other database$`, m.searchReturnsOtherMemory)
	ctx.Then(`^the search returns the memory from the home database$`, m.searchReturnsHomeMemory)
	ctx.Then(`^the output labels the result as core database evidence$`, m.outputIdentifiesHomeDatabase)
	ctx.Then(`^the output says to run "roca init" before searching it$`, m.outputPointsToInit)
	ctx.Then(`^both writes succeed$`, m.bothWritesSucceed)
	ctx.Then(`^the database holds both memories intact$`, m.holdsBothMemories)
}

// freshSandbox gives a scenario a HOME of its own with nothing else in it.
func (m *world) freshSandbox() error {
	home, err := os.MkdirTemp("", "roca-store-")
	if err != nil {
		return err
	}
	m.home = home
	m.last, m.previous, m.memories = run{}, run{}, 0
	m.everything = nil
	return os.MkdirAll(filepath.Join(home, "tmp"), 0o700)
}

// storeDBPath is where every store-domain scenario keeps its database.
func (m *world) storeDBPath() string {
	return filepath.Join(m.home, ".roca", "roca.db")
}

func (m *world) backupsDir() string {
	return filepath.Join(m.home, ".roca", "backups")
}

func (m *world) otherDBPath() string {
	return filepath.Join(m.home, "other", "roca.db")
}

func (m *world) missingDBPath() string {
	return filepath.Join(m.home, "missing", "roca.db")
}

const (
	homeUniqueMemory  = "home-only albatross"
	otherUniqueMemory = "other-only narwhal"
)

// --- given fixtures ---

// freshDatabase is the Given fixture: init over a clean HOME is expected to
// succeed, and a failure here means the scenario's premise is broken, not that
// the product misbehaved.
func (m *world) freshDatabase() error {
	if err := m.runInit(); err != nil {
		return err
	}
	if m.last.code != 0 {
		return fmt.Errorf("init exited %d: %s", m.last.code, m.last.stderr)
	}
	// The store-domain scenarios measure the core store itself. New installs
	// deliberately route agent memories to roca-ops, so this harness becomes an
	// explicit existing-config operator who keeps the legacy core write target.
	return os.WriteFile(filepath.Join(m.home, ".roca", "config.toml"), []byte(
		"[features]\nplugins = true\nroca_ops = false\ncron = true\nvector = false\n"), 0o600)
}

// runInit is the When action: it runs init and files what it said, and the
// scenario's own Then step reads the exit code. A scenario that expects init to
// refuse (a corrupt file at the path) reaches the same step and asserts the
// refusal, instead of the step declaring success on its own.
func (m *world) runInit() error {
	_, err := m.run(m.initCommand(true))
	return err
}

// initializeDatabasePlain is the same bootstrap without --json, so its output is
// the prose an operator reads.
func (m *world) initializeDatabasePlain() error {
	_, err := m.run(m.initCommand(false))
	return err
}

// databaseNeedingRepair leaves a real Roca database one index short of current,
// with a memory in it, so that adoption has something to protect with a backup
// before it repairs.
func (m *world) databaseNeedingRepair() error {
	if err := m.freshDatabase(); err != nil {
		return err
	}
	if err := m.storeMemoryFixture("project", "the repair fixture memory about anchors"); err != nil {
		return err
	}
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("DROP INDEX idx_memories_status"); err != nil {
		return fmt.Errorf("make the database need a repair: %w", err)
	}
	return nil
}

// modelDefersToLiteralSearch points the model at a provider whose valid plan
// deliberately finds no rows, so every question falls to the literal FTS rescue
// over the question's own words. That is the one route the search scenarios
// measure, reached without depending on any real model on the host or turning a
// healthy no-match into a provider failure.
func (m *world) modelDefersToLiteralSearch() error {
	m.models.factoryDefault = true
	if err := m.writeFrontierCLI("printf '%s' 'SELECT id FROM memories WHERE 0 LIMIT 1'"); err != nil {
		return err
	}
	return m.writeModelConfig()
}

// foreignOrCorruptFile plants the two shapes an operator can leave at the
// database path that are not a Roca database: bytes that are not SQLite at all,
// and a valid SQLite file whose tables Roca does not recognize.
func (m *world) foreignOrCorruptFile(kind string) error {
	db := m.storeDBPath()
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		return err
	}
	switch kind {
	case "corrupt":
		return os.WriteFile(db, []byte("not a database, only bytes"), 0o600)
	case "foreign":
		fdb, err := sql.Open("sqlite", "file:"+db)
		if err != nil {
			return err
		}
		defer fdb.Close()
		_, err = fdb.Exec("CREATE TABLE unrelated (payload TEXT)")
		return err
	default:
		return fmt.Errorf("I do not know how to plant a %q file", kind)
	}
}

func (m *world) homeDatabaseWithUniqueMemory() error {
	if err := m.freshDatabase(); err != nil {
		return err
	}
	if err := m.modelDefersToLiteralSearch(); err != nil {
		return err
	}
	return m.storeMemoryFixture("project", homeUniqueMemory)
}

func (m *world) otherDatabaseWithUniqueMemory() error {
	if _, err := m.runWith("roca init --db-path <other> --json", []string{
		"init", "--db-path", m.otherDBPath(), "--json",
	}); err != nil {
		return err
	}
	if m.last.code != 0 {
		return fmt.Errorf("init other database exited %d: %s", m.last.code, m.last.stderr)
	}

	config, err := os.ReadFile(filepath.Join(m.home, ".roca", "config.toml"))
	if err != nil {
		return fmt.Errorf("read the home model configuration: %w", err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(m.otherDBPath()), "config.toml"), config, 0o600); err != nil {
		return fmt.Errorf("configure the other database: %w", err)
	}
	return m.storeMemoryAt(m.otherDBPath(), "project", otherUniqueMemory, "", "", 0)
}

func (m *world) missingDatabasePath() error {
	if _, err := os.Stat(m.missingDBPath()); !os.IsNotExist(err) {
		return fmt.Errorf("the selected path is not missing: %v", err)
	}
	return nil
}

// --- when actions ---

func (m *world) storeMemoryFixture(layer, content string) error {
	return m.storeMemory(layer, content, "", "", 0)
}

func (m *world) storeMemoryFull(layer, origin, project, content string) error {
	return m.storeMemory(layer, content, origin, project, 0)
}

func (m *world) storeMemoryEmpty(layer string) error {
	return m.storeMemory(layer, "", "", "", 0)
}

func (m *world) storeSupersedingPrevious(layer, content string) error {
	previous, err := m.json()
	if err != nil {
		return err
	}
	id, ok := previous["id"].(float64)
	if !ok {
		return fmt.Errorf("the previous store named no memory id: %v", previous)
	}
	return m.storeMemory(layer, content, "", "", int64(id))
}

func (m *world) storeSupersedingID(layer string, id int, content string) error {
	return m.storeMemory(layer, content, "", "", int64(id))
}

// storeMemory runs the one write the product has against the home database and
// always in JSON, because the JSON carries the id the next step reads. The
// path-aware helper also seeds the selected-database scenarios.
func (m *world) storeMemory(layer, content, origin, project string, supersedes int64) error {
	return m.storeMemoryAt(m.storeDBPath(), layer, content, origin, project, supersedes)
}

func (m *world) storeMemoryAt(path, layer, content, origin, project string, supersedes int64) error {
	args := []string{"store", "--layer", layer, "--content", content,
		"--db-path", path, "--json"}
	if origin != "" {
		args = append(args, "--origin", origin)
	}
	if project != "" {
		args = append(args, "--project", project)
	}
	if supersedes != 0 {
		args = append(args, "--supersedes", strconv.FormatInt(supersedes, 10))
	}
	label := fmt.Sprintf("roca store --layer %s --content %s", layer, strconv.Quote(content))
	_, err := m.runWith(label, args)
	return err
}

func (m *world) searchFor(term string) error {
	args := append([]string{"query", "--db-path", m.storeDBPath(), "--json"}, strings.Fields(term)...)
	_, err := m.runWith(fmt.Sprintf("roca query %s --json", strconv.Quote(term)), args)
	return err
}

func (m *world) searchOtherDatabase() error {
	_, err := m.runWith("roca query narwhal --db-path <other> --json", []string{
		"query", "narwhal", "--db-path", m.otherDBPath(), "--json",
	})
	return err
}

func (m *world) searchHomeDatabase() error {
	_, err := m.runWith("roca query albatross --json", []string{"query", "albatross", "--json"})
	return err
}

func (m *world) searchMissingDatabase() error {
	_, err := m.runWith("roca query anything --db-path <missing>", []string{
		"query", "anything", "--db-path", m.missingDBPath(),
	})
	return err
}

// twoConcurrentWriters runs two store processes at once against the same
// database, the way two agents on the same machine would. They are real
// processes, not goroutines over one handle, because the guarantee is about
// contention between processes.
func (m *world) twoConcurrentWriters() error {
	const (
		alpha = "concurrent alpha memory about lions"
		beta  = "concurrent beta memory about tigers"
	)
	store := func(content string) run {
		cmd := exec.Command(m.binaryPath(), "store", "--layer", "project",
			"--content", content, "--origin", "human", "--db-path", m.storeDBPath())
		cmd.Env = m.environment()
		var out, failures strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &failures
		r := run{command: "roca store (" + content + ")", stdout: out.String(), stderr: failures.String()}
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if asExitError(err, &exit) {
				r.code = exit.ExitCode()
			} else {
				r.code = -1
				r.stderr = err.Error()
			}
		}
		return r
	}

	var first, second run
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); first = store(alpha) }()
	go func() { defer wait.Done(); second = store(beta) }()
	wait.Wait()

	m.everything = append(m.everything, first, second)
	m.last = second
	return nil
}

// --- then assertions ---

func (m *world) holdsEveryV1Table() error {
	return m.hasTables("sessions", "memories", "layers", "exchanges",
		"tool_uses", "thinking_blocks", "ingest_file_state")
}

func (m *world) hasTables(want ...string) error {
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return err
	}
	defer rows.Close()
	present := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		present[name] = true
	}
	for _, table := range want {
		if !present[table] {
			return fmt.Errorf("the v1 table %q is missing from the database", table)
		}
	}
	return nil
}

func (m *world) plainTextNotJSON() error {
	var probe any
	if err := json.Unmarshal([]byte(strings.TrimSpace(m.last.stdout)), &probe); err == nil {
		return fmt.Errorf("the init output is JSON, and it had to be plain text")
	}
	return m.outputContains("database outcome")
}

// wordSearchWasProved reads the promise init exists to keep: it does not return
// until it has asked the index for a word and said what came back.
func (m *world) wordSearchWasProved() error {
	return m.outputContains("word search:")
}

func (m *world) wordIndexNeverBroken() error {
	if strings.Contains(m.last.stdout, "did not answer") {
		return fmt.Errorf("init called the word index broken: %s", m.last.stdout)
	}
	return nil
}

// meaningPassNotStarted holds the line the decision drew: a run nobody is
// reading is never asked, so it downloads nothing and starts nothing.
func (m *world) meaningPassNotStarted() error {
	if strings.Contains(m.last.stdout, "deep search: reading") {
		return fmt.Errorf("bare init started the meaning pass: %s", m.last.stdout)
	}
	return nil
}

func (m *world) meaningPassSwitchedOff() error {
	path := filepath.Join(m.home, ".roca", "config.toml")
	loaded, err := config.LoadFile(path)
	if err != nil {
		return err
	}
	if loaded.Features.Vector {
		return fmt.Errorf("bare init enabled the meaning pass in %s", path)
	}
	return nil
}

func (m *world) memoryStillThere() error {
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM memories WHERE content LIKE '%kites%'").Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("the survivor memory is gone: %d rows", n)
	}
	return nil
}

func (m *world) backupWasTaken() error {
	if n, err := backupFileCount(m.backupsDir()); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("no backup was taken before the repair")
	}
	return nil
}

func (m *world) oneDatedBackupFile() error {
	n, err := backupFileCount(m.backupsDir())
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("there are %d dated backup files, want exactly 1", n)
	}
	return nil
}

// backupFileCount counts the whole, dated copies a repair leaves behind. Their
// name ends in .backup.db; everything else in the directory is not one.
func backupFileCount(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read the backup directory %s: %w", dir, err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".backup.db") {
			count++
		}
	}
	return count, nil
}

func (m *world) schemaLeftCurrent() error {
	if _, err := m.runWith("roca schema status --json",
		[]string{"schema", "status", "--db-path", m.storeDBPath(), "--json"}); err != nil {
		return err
	}
	return m.jsonKeyEqualTo("verdict", "current")
}

func (m *world) backupRestoresSameCount() error {
	live, err := m.countMemories()
	if err != nil {
		return err
	}
	backup, err := newestBackup(m.backupsDir())
	if err != nil {
		return err
	}
	copyDB, err := sql.Open("sqlite", "file:"+backup)
	if err != nil {
		return fmt.Errorf("the backup does not open: %w", err)
	}
	defer copyDB.Close()
	var integrity string
	if err := copyDB.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("the backup is not whole: %s", integrity)
	}
	var copied int
	if err := copyDB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&copied); err != nil {
		return fmt.Errorf("the backup carries no memories table: %w", err)
	}
	if copied != live {
		return fmt.Errorf("the backup has %d memories and the live database has %d", copied, live)
	}
	return nil
}

func newestBackup(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var name string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".backup.db") {
			name = entry.Name()
		}
	}
	if name == "" {
		return "", fmt.Errorf("no dated backup file in %s", dir)
	}
	return filepath.Join(dir, name), nil
}

func (m *world) storedMemoryHas(layer, origin, project string) error {
	document, err := m.json()
	if err != nil {
		return err
	}
	id, ok := document["id"].(float64)
	if !ok {
		return fmt.Errorf("the store named no memory id: %v", document)
	}
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	var gotLayer, gotOrigin string
	var gotProject sql.NullString
	if err := db.QueryRow("SELECT layer, origin, project FROM memories WHERE id = ?", int64(id)).
		Scan(&gotLayer, &gotOrigin, &gotProject); err != nil {
		return fmt.Errorf("the stored memory is not there: %w", err)
	}
	if gotLayer != layer || gotOrigin != origin || gotProject.String != project {
		return fmt.Errorf("the memory is layer=%q origin=%q project=%q, want %q/%q/%q",
			gotLayer, gotOrigin, gotProject.String, layer, origin, project)
	}
	return nil
}

// searchRows is the hits list of the last query answer, refused empty so a step
// that expects results cannot pass by finding nothing to look at.
func (m *world) searchRows(wantNonEmpty bool) ([]any, error) {
	document, err := m.json()
	if err != nil {
		return nil, err
	}
	raw, declared := document["hits"]
	rows, ok := raw.([]any)
	if declared && !ok {
		return nil, fmt.Errorf("hits is not a list: %s", m.last.stdout)
	}
	if wantNonEmpty && len(rows) == 0 {
		return nil, fmt.Errorf("the search returned no hits: %s", m.last.stdout)
	}
	return rows, nil
}

func (m *world) searchReturnsAtLeastOne() error {
	rows, err := m.searchRows(true)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("the search returned no result")
	}
	return nil
}

func (m *world) searchReturnsNone() error {
	rows, err := m.searchRows(false)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		return fmt.Errorf("the search returned %d results, want none", len(rows))
	}
	return nil
}

func (m *world) searchReturnsExactly(want int) error {
	rows, err := m.searchRows(true)
	if err != nil {
		return err
	}
	if len(rows) != want {
		return fmt.Errorf("the search returned %d results, want %d", len(rows), want)
	}
	return nil
}

func (m *world) firstResultContains(text string) error {
	rows, err := m.searchRows(true)
	if err != nil {
		return err
	}
	first, _ := rows[0].(map[string]any)
	if !strings.Contains(fmt.Sprint(first["snippet"]), text) {
		return fmt.Errorf("the first result does not contain %q: %v", text, first["snippet"])
	}
	return nil
}

func (m *world) noResultContains(text string) error {
	rows, _ := m.searchRows(false)
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if strings.Contains(fmt.Sprint(row["snippet"]), text) {
			return fmt.Errorf("a result contains %q: %v", text, row["snippet"])
		}
	}
	return nil
}

func (m *world) namesMissingContent() error {
	if !strings.Contains(strings.ToLower(m.last.stdout+m.last.stderr), "content") {
		return fmt.Errorf("the refusal does not name the missing content: %s", m.last.stderr)
	}
	return nil
}

func (m *world) namesRefusedWrite() error {
	if !strings.Contains(strings.ToLower(m.last.stdout+m.last.stderr), "foreign key") {
		return fmt.Errorf("the refusal does not name the rejected write: %s", m.last.stderr)
	}
	return nil
}

func (m *world) searchReturnsOtherMemory() error {
	return m.firstResultContains(otherUniqueMemory)
}

func (m *world) outputIdentifiesOtherDatabase() error {
	return m.firstSearchHitHasDatabase("core")
}

func (m *world) searchReturnsHomeMemory() error {
	return m.outputContains(homeUniqueMemory)
}

func (m *world) outputIdentifiesHomeDatabase() error {
	return m.firstSearchHitHasDatabase("core")
}

func (m *world) firstSearchHitHasDatabase(want string) error {
	hits, err := m.searchRows(true)
	if err != nil {
		return err
	}
	first, _ := hits[0].(map[string]any)
	if got := fmt.Sprint(first["database"]); got != want {
		return fmt.Errorf("first hit database = %q, want %q", got, want)
	}
	return nil
}

func (m *world) outputPointsToInit() error {
	return m.outputContains("run `roca init` before this command")
}

func (m *world) bothWritesSucceed() error {
	var seen int
	for _, r := range m.everything {
		if !strings.HasPrefix(r.command, "roca store") {
			continue
		}
		seen++
		if r.code != 0 {
			return fmt.Errorf("a concurrent store exited %d: %s", r.code, r.stderr)
		}
	}
	if seen < 2 {
		return fmt.Errorf("only %d concurrent stores ran", seen)
	}
	return nil
}

func (m *world) holdsBothMemories() error {
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("the database is corrupt after the concurrent writes: %s", integrity)
	}
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM memories WHERE content LIKE '%lions%' OR content LIKE '%tigers%'").
		Scan(&n); err != nil {
		return err
	}
	if n != 2 {
		return fmt.Errorf("the database holds %d of the 2 concurrent memories", n)
	}
	return nil
}
