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

	"github.com/cucumber/godog"
	_ "modernc.org/sqlite"
)

// world is a scenario's sandbox: a HOME of its own, the real binary and the
// last output it gave. Anything the binary creates outside this HOME is
// residue.
type world struct {
	binary string
	// releaseStamped is a copy of this product built with a clean release version
	// linked in. `roca update` refuses to overwrite a build that is not a
	// published release, so the scenario that drives the whole update flow
	// installs this one and the scenario that pins the refusal installs the
	// working copy `make build` produces.
	releaseStamped string
	home           string
	last           run
	previous       run
	memories       int
	// everything is every run of the scenario, for the steps that ask about a
	// whole session's output and not only the last command's.
	everything []run
	// models is the scenario's model world: the fake providers and what the
	// binary is told about them. Its zero value turns the model off, which is
	// what keeps every scenario that is not about providers from touching a
	// real one on the machine running the suite.
	models modelWorld
	// dbFingerprint is the content of the database when the world was seeded, for
	// the steps that prove a command wrote nothing.
	dbFingerprint string
	// plug is the scenario's MCP session over stdio and what it last got back.
	plug plugWorld
	// readOnly is the operator's switch, applied to every command and every
	// session of this scenario.
	readOnly bool
	// agentConfig and settings are the two files an integration touches: the
	// runtime's MCP configuration and its lifecycle settings. Both keep the
	// bytes they had before Roca arrived.
	agentConfig        string
	agentConfigBefore  string
	agentConfigRuntime string
	settings           string
	settingsBefore     string
	// install is the scenario's release channel, and installed is the copy of
	// the binary that lives inside its HOME. Every scenario runs its own copy,
	// because `roca uninstall` deletes the binary it runs from and a shared one
	// would take the suite's own build with it.
	install   installWorld
	installed string
	// agentConfigsBefore is what each runtime's configuration said before Roca
	// was declared in it, so every other byte can be restored, and
	// configBefore is the same question about this product's own file across an
	// update.
	agentConfigsBefore map[string]string
	configBefore       string
}

type run struct {
	command string
	code    int
	stdout  string
	stderr  string
}

func registerSteps(ctx *godog.ScenarioContext, binary string) {
	m := &world{binary: binary}

	ctx.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
		home, err := os.MkdirTemp("", "roca-acceptance-")
		if err != nil {
			return c, err
		}
		m.home = home
		// TMPDIR points inside this HOME (see environment), and it has to be
		// there before the first thing that shelled out read it. Creating it
		// once here is what keeps the sandbox self-contained on both platforms:
		// BSD mktemp fell back to a system temp when this dir was missing and
		// GNU mktemp died, which is exactly the gap that hid the installer's
		// portability bug from macOS.
		if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
			return c, err
		}
		m.last = run{}
		m.previous = run{}
		m.memories = 0
		m.everything = nil
		m.models = modelWorld{}
		m.dbFingerprint = ""
		m.plug = plugWorld{}
		m.readOnly = false
		m.agentConfig, m.agentConfigBefore, m.agentConfigRuntime = "", "", ""
		m.settings, m.settingsBefore = "", ""
		m.installed = ""
		m.agentConfigsBefore = nil
		m.configBefore = ""
		return c, nil
	})
	ctx.After(func(c context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		// A traceback in the operator's face is always a failure, whether the
		// step asserted it explicitly or not.
		if trace := hasTraceback(m.last); trace != "" && err == nil {
			m.closeThePlug()
			m.closeModels()
			m.closeTheChannel()
			return c, fmt.Errorf("the output contains a trace: %s", trace)
		}
		m.closeThePlug()
		m.closeModels()
		m.closeTheChannel()
		os.RemoveAll(m.home)
		return c, nil
	})

	ctx.Given(`^La Roca is installed and initialized$`, m.installedAndInitialized)
	ctx.Given(`^a HOME with the seeded world "([^"]*)"$`, m.seededWorld)
	ctx.Given(`^a HOME with an aged Roca database carrying tables from withdrawn features$`, m.agedDB)
	ctx.Given(`^a HOME with a database whose schema differs only in whitespace, comments and constraint order$`, m.dbWithDDLNoise)
	ctx.Given(`^there is a memory with content longer than (\d+) characters$`, m.longMemory)
	ctx.Given(`^there is a handoff memory about "([^"]*)"$`, m.aHandoffMemoryAbout)

	ctx.When(`^I run "([^"]*)"$`, m.iRun)
	ctx.When(`^I run "([^"]*)" a second time$`, m.iRun)
	ctx.When(`^I run "roca exec" with the SQL it returned, in JSON format$`, m.iRunTheSQLItReturned)

	ctx.Then(`^the command exits with code (\d+)$`, m.itExitsWithCode)
	ctx.Then(`^the command exits with a code other than 0$`, m.itExitsWithNonZeroCode)
	ctx.Then(`^the second command exits with code (\d+)$`, m.itExitsWithCode)
	ctx.Then(`^the JSON output has "([^"]*)" equal to "([^"]*)"$`, m.jsonKeyEqualTo)
	ctx.Then(`^the JSON output of the second has "([^"]*)" other than "([^"]*)"$`, m.jsonKeyNotEqualTo)
	ctx.Then(`^the JSON output has "([^"]*)" less than (\d+)$`, m.jsonKeyLessThan)
	ctx.Then(`^the JSON output has "([^"]*)" not empty$`, m.jsonKeyNotEmpty)
	ctx.Then(`^the JSON output has zero rows$`, m.jsonHasZeroRows)
	ctx.Then(`^the JSON output has no rows$`, m.jsonHasZeroRows)
	ctx.Then(`^the JSON output declares the match was empty$`, m.jsonDeclaresEmptyMatch)
	ctx.Then(`^the output names the question as outside the scope of the query$`, m.namesOutOfScope)
	ctx.Then(`^the output asks to be more specific$`, m.asksToBeMoreSpecific)
	ctx.Then(`^the output contains "([^"]*)"$`, m.outputContains)
	ctx.Then(`^no row has been returned$`, m.jsonHasZeroRows)
	ctx.Then(`^the memory count has not changed$`, m.theMemoryCountHasNotChanged)
	ctx.Then(`^the memories table still exists$`, m.theMemoriesTableStillExists)
	ctx.Then(`^the first row contains the synthetic release marker$`, m.firstRowCarriesTheSeed)
	ctx.Then(`^the first row contains the text "([^"]*)"$`, m.firstRowContains)
	ctx.Then(`^the output contains no invented text$`, m.withoutInventedText)
	ctx.Then(`^no call has been made to the model provider$`, m.withoutModelCall)
	ctx.Then(`^the database has not been queried for data$`, m.withoutDataQuery)
	ctx.Then(`^the rows are equal to those of the direct query$`, m.rowsEqualToTheDirectQuery)
	ctx.Then(`^the answer is not the memory total$`, m.isNotTheMemoryTotal)
	ctx.Then(`^either the emitted SQL contains a filter by that term$`, m.filtersOrDeclines)
	ctx.Then(`^or else the fast route declines and the question goes to the model$`, m.filtersOrDeclines)
	ctx.Then(`^every returned row belongs to the layer "([^"]*)"$`, m.allRowsBelongToTheLayer)
	ctx.Then(`^no text field of the answer exceeds (\d+) characters$`, m.noFieldExceeds)
	ctx.Then(`^the kept text includes the search match$`, m.theTextKeepsTheMatch)
	ctx.Then(`^the output does not mix warm-up noise with the answer$`, m.cleanOutput)
	ctx.Then(`^the orphan tables are reported and do not block$`, m.orphansReportedAndNotBlocking)
	ctx.Then(`^the decision to adopt does not depend on the text of the create statements$`, m.adoptionByStructure)
	ctx.Then(`^the output contains no traceback$`, m.withoutTraceback)
	ctx.Then(`^the output to the operator contains no traceback$`, m.withoutTraceback)

	registerModelSteps(ctx, m)
	registerInstallSteps(ctx, m)
	registerIngestSteps(ctx, m)
	registerMCPSteps(ctx, m)
}

// registerModelSteps are the ones about the providers. They live apart because
// they are the only ones that stand up servers of their own.
func registerModelSteps(ctx *godog.ScenarioContext, m *world) {
	ctx.Given(`^there is a valid credential for the frontier provider$`, m.aValidFrontierCredential)
	ctx.Given(`^the frontier provider is available$`, m.theFrontierIsAvailable)
	ctx.Given(`^the local model is available too$`, m.theLocalModelIsAvailable)
	ctx.Given(`^the local model is available$`, m.theLocalModelIsAvailable)
	ctx.Given(`^there is no network$`, m.thereIsNoNetwork)
	ctx.Given(`^there is no frontier provider credential$`, m.thereIsNoFrontierCredential)
	ctx.Given(`^the local model is not available$`, m.theLocalModelIsNotAvailable)
	ctx.Given(`^the configuration declares the provider order$`, m.theConfigurationDeclaresTheOrder)
	ctx.Given(`^the configuration declares a provider this version does not know$`,
		m.theConfigurationDeclaresAnUnknownProvider)
	ctx.Given(`^the configuration chooses model "([^"]*)" for the frontier provider$`,
		m.configurationChoosesFrontierModel)

	ctx.When(`^I log in to "([^"]*)" with model "([^"]*)"$`, m.loginWithModel)

	ctx.Then(`^the JSON output has "([^"]*)" equal to the frontier provider$`, m.jsonKeyEqualToTheFrontier)
	ctx.Then(`^the local provider has received no request$`, m.theLocalProviderReceivedNoRequest)
	ctx.Then(`^the output declares that it degraded to the local floor$`, m.itDeclaresItDegradedToTheLocalFloor)
	ctx.Then(`^no action has been asked of the operator$`, m.noActionAskedOfTheOperator)
	ctx.Then(`^the output names which providers were tried and why each one failed$`,
		m.itNamesEveryProviderTriedAndWhy)
	ctx.Then(`^the output names the exact command to install or start the local model$`,
		m.itNamesTheCommandThatStartsTheLocalModel)
	ctx.Then(`^the JSON output lists the providers in the declared order$`,
		m.theProvidersAreListedInTheDeclaredOrder)
	ctx.Then(`^for each one it declares whether it is available and why$`,
		m.eachOneDeclaresWhetherItIsAvailableAndWhy)
	ctx.Then(`^the output contains a warning that names the unknown provider$`,
		m.aWarningNamesTheUnknownProvider)
	ctx.Then(`^that warning lists the available providers$`, m.thatWarningListsTheAvailableProviders)
	ctx.Then(`^no output contains the credential's value$`, m.noOutputCarriesTheCredential)
	ctx.Then(`^no persistent log contains the credential's value$`, m.noPersistentLogCarriesTheCredential)
	ctx.Then(`^the configuration chooses model "([^"]*)" for provider "([^"]*)"$`,
		m.configurationChoosesProviderModel)
	ctx.Then(`^the login output names the model, its configuration source and both ways to change it$`,
		func() error { return m.modelNarrationNames("grok-demo", "xai") })
	ctx.Then(`^the model narration names "([^"]*)", its configuration source and both ways to change it$`,
		func(model string) error { return m.modelNarrationNames(model, theFrontierName) })
}

// --- worlds ---

func (m *world) installedAndInitialized() error {
	// A copy of the binary inside the HOME, because "installed" means installed:
	// the uninstall scenarios delete what they are running from, and the suite
	// cannot afford that to be its own build.
	if err := m.installBinary(); err != nil {
		return err
	}
	if _, err := m.run(m.initCommand(true)); err != nil {
		return err
	}
	if m.last.code != 0 {
		return fmt.Errorf("init exited with code %d: %s", m.last.code, m.last.stderr)
	}
	return nil
}

// seededWorld plants the synthetic corpus the scenario counts on.
// seeded by writing memories into the database; the operator's world is seeded on
// disk, as the artefact families the agents leave behind, and it is
// `roca ingest` that has to find them.
func (m *world) seededWorld(name string) error {
	if name == "operator" {
		return m.operatorWorld()
	}
	if name == "session-lifecycle" {
		return m.sessionLifecycleWorld()
	}
	if name != "synthetic-corpus" {
		return fmt.Errorf("I do not know how to seed the world %q", name)
	}
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	seeded := []struct{ layer, content string }{
		{"project", "Synthetic release marker: ORCHID_FIXTURE_7741 is ready for verification."},
		{"feedback", "Synthetic acceptance anchor: ROCAE2E_TRINI_ALPHA_7741."},
		{"discovery", "Database adoption compares structure, never DDL formatting."},
	}
	for _, s := range seeded {
		if _, err := db.Exec(
			"INSERT INTO memories (layer, content, origin) VALUES (?, ?, 'agent')",
			s.layer, s.content); err != nil {
			return err
		}
	}
	m.memories = len(seeded)
	// The FTS index has to cover what was seeded straight into the database,
	// the same way a later ingest does on a real machine, so the search layer
	// the model operates over is ready.
	return m.mustRun("roca index")
}

// The pill a session receives and the newest handoff it is handed. They are
// written out here because the lifecycle scenarios assert on them by name.
const (
	theSeededPill    = "the build must remain green"
	theNewestHandoff = "the previous session left the index incomplete"
)

// sessionLifecycleWorld plants what job J3 serves: one pill on the roster and
// three handoffs, the newest of them last.
func (m *world) sessionLifecycleWorld() error {
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.Exec(
		`INSERT INTO memories (layer, content, origin, metadata)
		 VALUES ('pill', ?, 'agent', ?)`,
		"# Build\n"+theSeededPill,
		`{"pill_slug": "build", "pill_title": "Build", "pill_order": 1}`); err != nil {
		return fmt.Errorf("seed the pill: %w", err)
	}
	handoffs := []string{
		"the first session adopted the schema",
		"the second session completed the provider path",
		theNewestHandoff,
	}
	for i, content := range handoffs {
		if _, err := db.Exec(
			`INSERT INTO memories (layer, content, origin, metadata, created_at)
			 VALUES ('handoff', ?, 'agent', '{}', ?)`,
			content, fmt.Sprintf("2026-08-05 1%d:00:00", i)); err != nil {
			return fmt.Errorf("seed the handoff: %w", err)
		}
	}
	m.memories = len(handoffs) + 1
	return nil
}

// agedDB plants a real Roca database with tables from withdrawn features
// inside it, like the one on a machine that has been running for months.
func (m *world) agedDB() error {
	return m.alterInstalledDB("age the database",
		`CREATE TABLE garden_notes (id INTEGER PRIMARY KEY, note TEXT)`,
		`INSERT INTO garden_notes (note) VALUES ('from a retired feature')`,
		`CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, sequence INTEGER)`,
		`CREATE TABLE proposals (id INTEGER PRIMARY KEY, kind TEXT, summary TEXT)`,
		`INSERT INTO memories (layer, content, origin) VALUES ('project', 'memoria vieja', 'human')`,
	)
}

// dbWithDDLNoise rewrites a table with the same structural content and a
// different DDL text: another column order, comments and other spacing.
func (m *world) dbWithDDLNoise() error {
	return m.alterInstalledDB("add formatting noise",
		`DROP TABLE ingest_file_state`,
		`CREATE TABLE ingest_file_state (
		   -- rewritten by hand by another operator
		   metadata       TEXT     DEFAULT '{}',
		   path           TEXT     NOT NULL PRIMARY KEY,
		   last_error     TEXT,     fingerprint TEXT,
		   source_kind    TEXT NOT NULL,
		   last_synced_at TEXT DEFAULT (datetime('now')),
		   project        TEXT,     source_agent TEXT
		 )`,
		`CREATE INDEX idx_ingest_state_project ON ingest_file_state (project)`,
		`CREATE INDEX idx_ingest_state_source_agent ON ingest_file_state (source_agent)`,
	)
}

// --- execution ---

func (m *world) iRun(command string) error {
	if command == "roca init --json" {
		command = m.initCommand(true)
	}
	_, err := m.run(command)
	return err
}

// mustRun runs a command and turns a non-zero exit into the error, for the
// steps that only have a happy path to describe.
func (m *world) mustRun(command string) error {
	if _, err := m.run(command); err != nil {
		return err
	}
	if m.last.code != 0 {
		return fmt.Errorf("%s: code %d: %s", command, m.last.code, m.last.stderr)
	}
	return nil
}

func (m *world) run(command string) (run, error) {
	arguments, err := split(command)
	if err != nil {
		return run{}, err
	}
	if arguments[0] != "roca" {
		return run{}, fmt.Errorf("this suite only runs roca, not %q", arguments[0])
	}
	return m.runWith(command, arguments[1:])
}

// runWith runs the binary with the arguments already split. It exists apart
// because some steps pass SQL with quotes inside, and splitting that line again
// would change what gets run.
func (m *world) runWith(command string, arguments []string) (run, error) {
	if err := os.MkdirAll(filepath.Join(m.home, "tmp"), 0o700); err != nil {
		return run{}, err
	}
	err := m.record(command, exec.Command(m.binaryPath(), arguments...))
	return m.last, err
}

// record runs one process with the scenario's environment and files what it
// said as this scenario's last run, which is what every Then step reads.
//
// The binary and the installer's own shell both come through here: two copies
// of "run it and file what it said" would be two answers to what `m.last` is,
// and every Then step of the suite reads exactly that.
func (m *world) record(label string, command *exec.Cmd) error {
	command.Env = m.environment()

	var out, failures strings.Builder
	command.Stdout, command.Stderr = &out, &failures
	err := command.Run()

	m.previous = m.last
	m.last = run{command: label, stdout: out.String(), stderr: failures.String()}
	if err != nil {
		var exit *exec.ExitError
		if !asExitError(err, &exit) {
			return fmt.Errorf("run %q: %w", label, err)
		}
		m.last.code = exit.ExitCode()
	}
	// Credential checks cover every output of a session, not only the last one.
	m.everything = append(m.everything, m.last)
	return nil
}

// environment is what every invocation of the binary is told, whether it is run
// as a command or launched as an MCP server: the same sandbox HOME, the same
// model world and the same read-only switch, so that the two surfaces are
// really being compared and not two different installations.
func (m *world) environment() []string {
	environment := append([]string{
		"HOME=" + m.home,
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"TMPDIR=" + filepath.Join(m.home, "tmp"),
	}, m.modelEnvironment()...)
	if m.readOnly {
		environment = append(environment, "ROCA_READ_ONLY=1")
	}
	// The scenario's own release channel, when it has one. `roca update` reads
	// these two the same way an operator would set them once instead of passing
	// --repo on every update, and no scenario here ever reaches github.com.
	if m.install.server != nil {
		certificate := filepath.Join(m.home, "tls-ca.pem")
		environment = append(environment,
			"ROCA_REPO="+m.install.repo,
			"ROCA_GITHUB_API="+m.install.server.URL,
			"GITHUB_TOKEN="+m.install.token,
			"SSL_CERT_FILE="+certificate,
			"CURL_CA_BUNDLE="+certificate)
	}
	return environment
}

// --- assertions ---

func (m *world) itExitsWithCode(expected int) error {
	if m.last.code != expected {
		return fmt.Errorf("code %d, want %d (stderr: %s)",
			m.last.code, expected, m.last.stderr)
	}
	return nil
}

func (m *world) itExitsWithNonZeroCode() error {
	if m.last.code == 0 {
		return fmt.Errorf("code 0, want other than 0 (stdout: %s)", m.last.stdout)
	}
	return nil
}

func (m *world) jsonKeyEqualTo(key, value string) error {
	document, err := m.json()
	if err != nil {
		return err
	}
	found, ok := lookup(document, key)
	if !ok {
		return fmt.Errorf("the JSON output has no %q: %v", key, keys(document))
	}
	if fmt.Sprint(found) != value {
		return fmt.Errorf("%s = %v, want %q", key, found, value)
	}
	return nil
}

// lookup walks a dotted path with optional indexes: `model.ready`,
// `ingest.errors`, `rows[0].COUNT(*)`.
//
// The frozen suite asks about nested keys, and a flat lookup would answer
// "there is no such key" over a document that carries it three levels down. A
// key with a dot inside it wins over the path reading, because `rows[0].COUNT(*)`
// is a column name and not a route.
func lookup(document map[string]any, path string) (any, bool) {
	if value, direct := document[path]; direct {
		return value, true
	}
	var current any = document
	for _, step := range strings.Split(path, ".") {
		name, indexes := splitIndexes(step)
		if name != "" {
			table, isTable := current.(map[string]any)
			if !isTable {
				return nil, false
			}
			value, found := table[name]
			if !found {
				return nil, false
			}
			current = value
		}
		for _, index := range indexes {
			list, isList := current.([]any)
			if !isList || index >= len(list) {
				return nil, false
			}
			current = list[index]
		}
	}
	return current, true
}

// splitIndexes cuts `rows[0]` into the member and the indexes that follow it.
func splitIndexes(step string) (string, []int) {
	open := strings.Index(step, "[")
	if open < 0 {
		return step, nil
	}
	name := step[:open]
	var indexes []int
	for _, part := range strings.Split(step[open:], "[") {
		digits, closed := strings.CutSuffix(part, "]")
		if !closed || digits == "" {
			continue
		}
		index, err := strconv.Atoi(digits)
		if err != nil {
			return step, nil
		}
		indexes = append(indexes, index)
	}
	return name, indexes
}

func (m *world) jsonHasZeroRows() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	if rows, ok := document["rows"]; ok && rows != nil {
		if list, ok := rows.([]any); ok && len(list) > 0 {
			return fmt.Errorf("the output carries %d rows, want none", len(list))
		}
	}
	return nil
}

func (m *world) jsonDeclaresEmptyMatch() error {
	return m.jsonKeyEqualTo("match", "empty")
}

func (m *world) namesOutOfScope() error {
	return m.outputContains("outside the scope of the query")
}

func (m *world) asksToBeMoreSpecific() error {
	return m.outputContains("be more specific")
}

func (m *world) theMemoryCountHasNotChanged() error {
	n, err := m.countMemories()
	if err != nil {
		return err
	}
	if n != m.memories {
		return fmt.Errorf("memories = %d, want %d", n, m.memories)
	}
	return nil
}

func (m *world) firstRowCarriesTheSeed() error {
	return m.firstRowContains("ORCHID_FIXTURE_7741")
}

func (m *world) firstRowContains(want string) error {
	document, err := m.json()
	if err != nil {
		return err
	}
	rows, _ := document["rows"].([]any)
	if len(rows) == 0 {
		return fmt.Errorf("there are no rows: %s", m.last.stdout)
	}
	first, _ := rows[0].(map[string]any)
	text := fmt.Sprint(first["text"])
	if !strings.Contains(text, want) {
		return fmt.Errorf("the first row does not contain %q: %q", want, text)
	}
	return nil
}

// withoutInventedText: an empty answer cannot carry prose that looks like an
// answer. Only the declared empty-match message is admitted.
func (m *world) withoutInventedText() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	message := fmt.Sprint(document["message"])
	if message != "<nil>" && !strings.Contains(message, "no matches in memory") {
		return fmt.Errorf("the empty answer carries text: %q", message)
	}
	return nil
}

func (m *world) orphansReportedAndNotBlocking() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	orphans, _ := document["orphans"].([]any)
	if len(orphans) == 0 {
		return fmt.Errorf("no orphan table has been reported")
	}
	// Not blocking means having finished well and not having touched them.
	if m.last.code != 0 {
		return fmt.Errorf("the orphans blocked: code %d", m.last.code)
	}
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	var score string
	if err := db.QueryRow("SELECT note FROM garden_notes").Scan(&score); err != nil {
		return fmt.Errorf("the orphan table is gone: %w", err)
	}
	return nil
}

// adoptionByStructure: the same database with its DDL rewritten is still adopted.
func (m *world) adoptionByStructure() error {
	if err := m.dbWithDDLNoise(); err != nil {
		return err
	}
	if _, err := m.run(m.initCommand(true)); err != nil {
		return err
	}
	if err := m.itExitsWithCode(0); err != nil {
		return err
	}
	return m.jsonKeyEqualTo("database", "adopted")
}

func (m *world) initCommand(jsonOutput bool) string {
	command := "roca init"
	if jsonOutput {
		command += " --json"
	}
	return command + " --db-path " + strconv.Quote(filepath.Join(m.home, ".roca", "roca.db"))
}

func (m *world) withoutTraceback() error {
	if trace := hasTraceback(m.last); trace != "" {
		return fmt.Errorf("the output contains a trace: %s", trace)
	}
	return nil
}

// --- helpers ---

func (m *world) json() (map[string]any, error) {
	var document map[string]any
	if err := json.Unmarshal([]byte(m.last.stdout), &document); err != nil {
		return nil, fmt.Errorf("the output of %q is not valid JSON: %w\n%s",
			m.last.command, err, m.last.stdout)
	}
	return document, nil
}

func (m *world) outputContains(text string) error {
	all := m.last.stdout + m.last.stderr
	if !strings.Contains(all, text) {
		return fmt.Errorf("the output does not contain %q:\n%s", text, all)
	}
	return nil
}

func (m *world) openDB() (*sql.DB, error) {
	path := filepath.Join(m.home, ".roca", "roca.db")
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("there is no database at %s: %w", path, err)
	}
	return sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
}

// rowsOfTheAnswer is the `rows` list of the JSON answer, refused when it is
// empty: every step that checks a property of the rows one by one needs the same
// thing first, and an empty list would let the check pass by having nothing to
// look at.
func (m *world) rowsOfTheAnswer() ([]any, error) {
	document, err := m.json()
	if err != nil {
		return nil, err
	}
	rows, _ := document["rows"].([]any)
	if len(rows) == 0 {
		return nil, fmt.Errorf("there are no rows to check: %s", m.last.stdout)
	}
	return rows, nil
}

// countMemories is how many memories the database holds right now.
func (m *world) countMemories() (int, error) {
	db, err := m.openDB()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	err = db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n)
	return n, err
}

// alterInstalledDB runs statements against an installed database, which is how
// the world plants the shapes that adoption has to survive. `what` names the
// manoeuvre so a failure says which one broke, not merely that SQL failed.
func (m *world) alterInstalledDB(what string, statements ...string) error {
	if err := m.installedAndInitialized(); err != nil {
		return err
	}
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
	}
	return nil
}

func hasTraceback(e run) string {
	for _, state := range []string{"Traceback (most recent call last)", "goroutine 1 [running]", "panic: "} {
		if strings.Contains(e.stdout+e.stderr, state) {
			return state
		}
	}
	return ""
}

func keys(document map[string]any) []string {
	var names []string
	for k := range document {
		names = append(names, k)
	}
	return names
}

// split cuts a command line respecting single quotes, which is how the suite
// writes the questions.
func split(line string) ([]string, error) {
	var parts []string
	var current strings.Builder
	var quote rune
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
		case r == ' ':
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote in %q", line)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	return parts, nil
}

func asExitError(err error, dest **exec.ExitError) bool {
	out, ok := err.(*exec.ExitError)
	if ok {
		*dest = out
	}
	return ok
}
