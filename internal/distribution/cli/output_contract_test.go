package cli

import (
	"database/sql"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider"
)

// A build the whole file shares for its assertions. The version travels into
// every JSON envelope, so the contract pins it by this constant rather than by
// the binary's release string.
const (
	contractVersion = "test"
	contractSHA     = "test-sha"
)

func contractBuild() Build { return Build{Version: contractVersion, Commit: contractSHA} }

// mustJSON parses a command's output as a JSON object, failing the test with the
// raw output when it is not. Every --json contract does this parse, so it lives
// once instead of being re-typed until two copies drift apart.
func mustJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not a JSON object: %v\n%s", err, out)
	}
	return doc
}

// runRootSplit captures stdout and stderr apart. It wires only the env's own
// writers (not cobra's SetOut/SetErr), which is all the login prompt contract
// needs: the prompt writes through env.errOut and the JSON through env.out, and
// a program reads stdout alone.
func runRootSplit(t *testing.T, build Build, in io.Reader, args ...string) (string, string) {
	t.Helper()
	var out, errs strings.Builder
	env := hermeticCLIEnv(&cliEnv{build: build, out: &out, errOut: &errs})
	root := rootCommand(env)
	root.SetArgs(args)
	root.SetIn(in)
	_ = root.Execute()
	return out.String(), errs.String()
}

func TestUninstallPromptUsesStderrSoJSONStdoutStaysClean(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out, errs strings.Builder
	env := &cliEnv{out: &out, errOut: &errs, json: true}

	if _, err := env.askAboutTheData(strings.NewReader("\n")); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("uninstall prompt polluted stdout: %q", out.String())
	}
	if !strings.Contains(errs.String(), "Keep the Roca database") {
		t.Fatalf("stderr does not carry the prompt: %q", errs.String())
	}
}

// failingRoot runs a command that has to fail and returns its error, failing the
// test when it did not. The data commands share this error-path shape, and a
// second copy of it is a clone that drifts.
func failingRoot(t *testing.T, args ...string) error {
	t.Helper()
	_, err := runRootErr(t, contractBuild(), nil, args...)
	if err == nil {
		t.Fatalf("roca %v: expected a failure", args)
	}
	return err
}

// `roca login --json` with no provider used to print the human catalogue
// regardless of the flag. The flag is a contract: a program asking for JSON may
// not be handed prose it then has to parse.
func TestBareLoginHonoursTheJSONFlag(t *testing.T) {
	out := runRoot(t, contractBuild(), "login", "--json")

	if strings.Contains(out, "Supported providers:") {
		t.Fatalf("bare login --json printed the human catalogue:\n%s", out)
	}
	doc := mustJSON(t, out)
	providers, _ := doc["providers"].([]any)
	if len(providers) == 0 {
		t.Fatalf("bare login --json lists no providers:\n%s", out)
	}
	first, _ := providers[0].(map[string]any)
	if first["name"] != provider.NameCodex || first["flow"] != "local_cli" {
		t.Errorf("first provider is not codex/local_cli: %v", first)
	}
	if !strings.Contains(first["command"].(string), "roca login codex") {
		t.Errorf("first command does not name the login verb: %v", first)
	}
}

// `roca logout` used to print its confirmation as prose even under --json.
// Forgetting is an end state a script checks, so it answers a document.
// Forgetting what was already forgotten is the end state logout promises, so no
// credential is staged here: the document says "forgotten" either way.
func TestLogoutHonoursTheJSONFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	doc := mustJSON(t, runRoot(t, contractBuild(), "logout", "xai", "--json"))
	if doc["provider"] != provider.NameXAI || doc["forgotten"] != true {
		t.Errorf("logout --json shape is wrong: %v", doc)
	}
}

// `roca login <key-provider> --json` answers who, where and with what model,
// and never echoes the key. The prompt that asks for it travels on stderr, so
// stdout is a pure envelope a program parses. The human confirmation is covered
// elsewhere; this pins the machine envelope.
func TestKeyLoginAnswersAJSONEnvelope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stdout, stderr := runRootSplit(t, contractBuild(), strings.NewReader("sk-deepseek\n"),
		"login", provider.NameDeepSeek, "--json")
	doc := mustJSON(t, stdout)
	if doc["provider"] != provider.NameDeepSeek {
		t.Errorf("provider = %v, want %q", doc["provider"], provider.NameDeepSeek)
	}
	if doc["path"] == nil || doc["path"] == "" {
		t.Errorf("path is missing: %v", doc["path"])
	}
	if strings.Contains(stdout, "sk-deepseek") {
		t.Errorf("the key leaked into the JSON output:\n%s", stdout)
	}
	if !strings.Contains(stderr, "API key") {
		t.Errorf("the prompt did not move to stderr:\n%s", stderr)
	}
}

// `roca doctor` is the diagnosis the skill points an agent at ("diagnosis +
// remedies"). Its human narration names the database, the configuration and the
// model verdict, and its JSON carries the same fields a program reads.
func TestDoctorNarratesAndAnswersJSON(t *testing.T) {
	fixtureInstallation(t)

	human := runRoot(t, contractBuild(), "doctor")
	for _, want := range []string{
		"roca " + contractVersion, "database:", "configuration:",
		"agents detected:", "agents not found:", "model:",
	} {
		if !strings.Contains(human, want) {
			t.Errorf("doctor narration does not carry %q:\n%s", want, human)
		}
	}

	doc := mustJSON(t, runRoot(t, contractBuild(), "doctor", "--json"))
	for _, key := range []string{"version", "source_sha", "config_path", "memories"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("doctor --json is missing %q:\n%s", key, doc)
		}
	}
	if doc["version"] != contractVersion {
		t.Errorf("doctor --json version = %v, want %q", doc["version"], contractVersion)
	}
}

func TestInitAndDoctorNarrateBedrockAndExposeItAsJSON(t *testing.T) {
	home := t.TempDir()
	isolateRuntimeDirs(t, home)
	dbPath := filepath.Join(home, ".roca", "roca.db")
	runRoot(t, contractBuild(), "init", "--db-path", dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO sessions (session_id, project, started_at)
		VALUES ('first', 'bedrock-project', '2026-01-31T08:15:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for _, command := range []string{"init", "doctor"} {
		human := runRoot(t, contractBuild(), command, "--db-path", dbPath)
		if !strings.Contains(human, "bedrock: your memory reaches back to 31 Jan 2026 (first session: bedrock-project)") {
			t.Errorf("%s did not narrate bedrock:\n%s", command, human)
		}
		doc := mustJSON(t, runRoot(t, contractBuild(), command, "--db-path", dbPath, "--json"))
		bedrock, _ := doc["bedrock"].(map[string]any)
		if bedrock["timestamp"] != "2026-01-31T08:15:00Z" || bedrock["project"] != "bedrock-project" {
			t.Errorf("%s bedrock JSON = %#v", command, bedrock)
		}
	}
}

func TestInitAndDoctorTellTheTruthWhenTheCorpusIsEmpty(t *testing.T) {
	home := t.TempDir()
	isolateRuntimeDirs(t, home)
	dbPath := filepath.Join(home, ".roca", "roca.db")
	for _, command := range []string{"init", "doctor"} {
		human := runRoot(t, contractBuild(), command, "--db-path", dbPath)
		if !strings.Contains(human, "bedrock: your memory has no history yet") {
			t.Errorf("%s empty bedrock narration:\n%s", command, human)
		}
		if strings.Contains(human, "1970") {
			t.Errorf("%s invented an epoch:\n%s", command, human)
		}
		doc := mustJSON(t, runRoot(t, contractBuild(), command, "--db-path", dbPath, "--json"))
		if doc["bedrock"] != nil {
			t.Errorf("%s empty bedrock JSON = %#v, want null", command, doc["bedrock"])
		}
	}
}

// `roca health` is the shell form of the MCP `roca_health` tool. Its status
// line and its TOON check table are the contract both surfaces share.
func TestHealthReportsItsStatus(t *testing.T) {
	fixtureInstallation(t)

	human := runRoot(t, contractBuild(), "health")
	if !strings.HasPrefix(human, "health: ") {
		t.Errorf("health narration does not start with the status:\n%s", human)
	}
	if !strings.Contains(human, "rows[") || !strings.Contains(human, "{status,check,count,summary}") {
		t.Errorf("health narration does not carry the TOON check table:\n%s", human)
	}
	if !strings.Contains(human, "ghost_sessions") {
		t.Errorf("health narration dropped a known check:\n%s", human)
	}

	doc := mustJSON(t, runRoot(t, contractBuild(), "health", "--json"))
	// A missing key decodes to nil, and nil is not "": the old check passed over
	// an envelope with no status at all.
	if status, ok := doc["status"].(string); !ok || status == "" {
		t.Errorf("health --json has no status:\n%s", doc)
	}
	checks, _ := doc["checks"].(map[string]any)
	if _, ok := checks["ghost_sessions"]; !ok {
		t.Errorf("health --json dropped the ghost_sessions check:\n%s", doc)
	}
}

// `roca init` narrates its phases for a human (covered in init_narration_test);
// under --json it answers the outcome envelope a program parses, with the
// adopted-by-copy flag telling the adoption path apart from a fresh create and
// the version telling it which build answered. fixtureInstallation already owns
// the isolated-home preamble, so this reuses it for the env and asks init to
// build a second database by an explicit path under --json.
func TestInitAnswersAJSONEnvelope(t *testing.T) {
	fixtureInstallation(t)

	out := runRoot(t, contractBuild(), "init", "--db-path",
		filepath.Join(t.TempDir(), "other.db"), "--json")
	doc := mustJSON(t, out)
	if doc["version"] != contractVersion {
		t.Errorf("init --json version = %v, want %q", doc["version"], contractVersion)
	}
	if _, ok := doc["adopted_by_copy"]; ok {
		t.Errorf("a fresh init must not claim it adopted: %v", doc["adopted_by_copy"])
	}
	if rows, _ := doc["rows"].(map[string]any); rows == nil {
		t.Errorf("init --json carries no rows envelope:\n%s", out)
	}
}

// `roca query` paints AXI TOON rows under a route narration line, which is what
// the skill documents. The route line names the path and the template so the
// operator can tell a compiler answer from a model one.
func TestQueryPaintsTOONRowsAndARouteLine(t *testing.T) {
	fixtureInstallation(t)

	runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--content", "ffmpeg patterns for trimming video", "--origin", "agent")

	// A SELECT under the gate renders the same TOON rows a model answer does,
	// without needing a model in the hermetic fixture.
	human := runRoot(t, contractBuild(), "exec",
		"SELECT 'memory' AS source, id, content AS text, created_at FROM memories LIMIT 1")
	if !strings.Contains(human, "rows[1]{source,id,created_at,text}") {
		t.Errorf("the TOON row header changed shape:\n%s", human)
	}
	if !strings.Contains(human, "ffmpeg patterns for trimming video") {
		t.Errorf("the matched text did not surface:\n%s", human)
	}
}

// A question the deterministic route declines and no model can lift is not an
// answer. The exit is not a failure under --json and the envelope says so by
// name, so a script does not read "it worked" from a machine with nothing to say.
func TestQueryRefusalIsHonestUnderJSON(t *testing.T) {
	fixtureInstallation(t)

	out, err := runRootErr(t, contractBuild(), nil, "query", "tulipanismo", "--json")
	if err != nil {
		t.Fatalf("an unresolved question is not a program failure under --json: %v\n%s", err, out)
	}
	doc := mustJSON(t, out)
	if doc["path"] != "unresolved" {
		t.Errorf("path = %v, want unresolved", doc["path"])
	}
	if doc["match"] != "empty" {
		t.Errorf("match = %v, want empty", doc["match"])
	}
	if doc["message"] == nil || doc["message"] == "" {
		t.Errorf("the unresolved answer does not say why:\n%s", out)
	}
}

func TestStoreHumanJSONAndError(t *testing.T) {
	fixtureInstallation(t)

	human := runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--content", "a pinned note", "--origin", "agent")
	if !strings.Contains(human, "stored: memory") || !strings.Contains(human, "layer discovery") {
		t.Errorf("store narration lost its id and layer:\n%s", human)
	}

	doc := mustJSON(t, runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--content", "a second note", "--origin", "agent", "--json"))
	if doc["layer"] != "discovery" || doc["id"] == nil {
		t.Errorf("store --json lost its id and layer: %v", doc)
	}

	if err := failingRoot(t, "store", "--content", "no layer"); !strings.Contains(err.Error(), "layer") {
		t.Errorf("the error does not name the missing flag: %v", err)
	}
}

func TestExecHumanJSONAndError(t *testing.T) {
	fixtureInstallation(t)

	human := runRoot(t, contractBuild(), "exec", "SELECT COUNT(*) AS n FROM memories")
	if !strings.Contains(human, "SELECT COUNT(*) AS n FROM memories") {
		t.Errorf("exec narration lost the SQL it ran:\n%s", human)
	}
	// A COUNT(*) returns exactly one row, and one row is counted in the singular.
	if !strings.Contains(human, "1 row ·") {
		t.Errorf("exec narration lost its row count and latency:\n%s", human)
	}

	doc := mustJSON(t, runRoot(t, contractBuild(), "exec",
		"SELECT COUNT(*) AS n FROM memories", "--json"))
	if doc["row_count"] == nil || doc["sql"] == nil {
		t.Errorf("exec --json lost its sql and row_count: %v", doc)
	}

	if err := failingRoot(t, "exec", "DELETE FROM memories"); !strings.Contains(err.Error(), "SELECT") {
		t.Errorf("the gate error does not name SELECT: %v", err)
	}
}

// An unknown command is a failure that names what was asked, and a typo close to
// a real one is answered with the suggestion Cobra computes. That is how an
// operator recovers from a misspelling; a wholly unknown name still fails by
// name so a script does not mistake it for success.
func TestUnknownCommandFailsByNameAndSuggestsATypo(t *testing.T) {
	if err := failingRoot(t, "boguscmd"); !strings.Contains(err.Error(), "boguscmd") {
		t.Errorf("the error does not name the unknown command: %v", err)
	}

	_, err := runRootErr(t, contractBuild(), nil, "quer")
	if err == nil || !strings.Contains(err.Error(), "Did you mean this?") ||
		!strings.Contains(err.Error(), "query") {
		t.Errorf("a close typo is not suggested:\n%v", err)
	}
}
