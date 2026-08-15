//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/test/compatibility"
	_ "modernc.org/sqlite"
)

const (
	oracleFixtureSchema   = "la-roca.data-split-oracle-fixture/v1"
	oracleGoldenSchema    = "la-roca.data-split-oracle-golden/v1"
	oraclePublicKeyBase64 = "HfdYb438IgjL6HuHpfj0FlxVE67kr1AdPVmDJCBafpQ="
)

type oracleFixture struct {
	Schema          string         `json:"schema"`
	Memories        []oracleMemory `json:"memories"`
	ModelQuestion   string         `json:"model_question"`
	ModelSQL        string         `json:"model_sql"`
	LiteralQuestion string         `json:"literal_question"`
	MissingQuestion string         `json:"missing_question"`
	StoreContent    string         `json:"store_content"`
	MCPStoreContent string         `json:"mcp_store_content"`
}

type oracleMemory struct {
	ID            int64  `json:"id"`
	Layer         string `json:"layer"`
	Content       string `json:"content"`
	Project       string `json:"project"`
	CreatedAt     string `json:"created_at"`
	SourceAgent   string `json:"source_agent"`
	SourceModel   string `json:"source_model"`
	SourceSurface string `json:"source_surface"`
}

type oracleGolden struct {
	Schema string       `json:"schema"`
	Cases  []oracleCase `json:"cases"`
}

type oracleCase struct {
	Name      string `json:"name"`
	Surface   string `json:"surface"`
	Operation string `json:"operation"`
	ExitCode  int    `json:"exit_code,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	Output    string `json:"output"`
	Stderr    string `json:"stderr,omitempty"`
}

type oracleWorld struct {
	binary  string
	root    string
	fixture oracleFixture
	bundle  oracleGolden
	err     error
}

func TestDataSplitCompatibilityOracle(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "data-split-oracle"))
	if err != nil {
		t.Fatal(err)
	}
	feature, err := os.ReadFile(filepath.Join("..", "..", "features", "provider", "data-split-compatibility.feature"))
	if err != nil {
		t.Fatal(err)
	}
	w := &oracleWorld{binary: binary, root: root}
	suite := godog.TestSuite{
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			ctx.Given(`^the sealed DATA SPLIT synthetic fixture$`, w.loadFixture)
			ctx.When(`^the compatibility oracle records and replays the golden bundle$`, w.recordAndReplay)
			ctx.Then(`^the CLI golden cases cover query, rescue, ranking, store, SQL, warnings, identities, and failures$`, w.cliCoverage)
			ctx.Then(`^the MCP golden cases cover query, exec, store, read-only enforcement, and failures$`, w.mcpCoverage)
			ctx.When(`^one byte of the signed golden bundle is changed$`, w.changeSignedGolden)
			ctx.Then(`^the compatibility oracle rejects the changed bundle$`, w.changedGoldenRejected)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Tags:            "@data-split-oracle",
			FeatureContents: []godog.Feature{{Name: "features/provider/data-split-compatibility.feature", Contents: feature}},
		},
	}
	if suite.Run() != 0 {
		t.Fail()
	}
}

func (w *oracleWorld) loadFixture() error {
	raw, err := os.ReadFile(filepath.Join(w.root, "fixture.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &w.fixture); err != nil {
		return err
	}
	if w.fixture.Schema != oracleFixtureSchema || len(w.fixture.Memories) < 4 {
		return fmt.Errorf("fixture is not a representative %s document", oracleFixtureSchema)
	}
	w.bundle = oracleGolden{}
	w.err = nil
	return nil
}

func (w *oracleWorld) recordAndReplay() error {
	manifest, err := compatibility.VerifyBundle(w.root, oraclePublicKey())
	if err != nil {
		return err
	}
	actual, err := w.record()
	if err != nil {
		return err
	}
	recordingRoot, cleanup, err := oracleTempDir("recording-")
	if err != nil {
		return err
	}
	defer cleanup()
	recording := filepath.Join(recordingRoot, "recorded.json")
	if err := os.WriteFile(recording, actual, 0o600); err != nil {
		return err
	}
	recorded, err := os.ReadFile(recording)
	if err != nil {
		return err
	}
	want, err := os.ReadFile(filepath.Join(w.root, manifest.Golden.Path))
	if err != nil {
		return err
	}
	if !bytes.Equal(recorded, want) {
		return fmt.Errorf("recorded behavior differs from the sealed golden (-want +got):\n%s", compactDiff(string(want), string(recorded)))
	}
	return json.Unmarshal(want, &w.bundle)
}

func (w *oracleWorld) record() ([]byte, error) {
	homeRoot, err := filepath.Abs(filepath.Join("..", "..", ".tmp"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(homeRoot, 0o700); err != nil {
		return nil, err
	}
	home, err := os.MkdirTemp(homeRoot, "data-split-oracle-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	if err := os.MkdirAll(filepath.Join(home, ".roca"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		return nil, err
	}
	runner := &oracleRunner{binary: w.binary, home: home, normalizer: compatibility.Normalizer{Home: home}}
	if err := runner.writeConfig("http://127.0.0.1:1"); err != nil {
		return nil, err
	}
	initResult := runner.runCLI("init", "init", "init", "--db-path", runner.dbPath(), "--json")
	if initResult.ExitCode != 0 {
		return nil, fmt.Errorf("initialize oracle HOME: %s", initResult.Stderr)
	}
	if err := seedOracleMemories(runner.dbPath(), w.fixture.Memories); err != nil {
		info, statErr := os.Stat(runner.dbPath())
		return nil, fmt.Errorf("%w (database=%s stat=%v size=%v init=%s)", err,
			runner.dbPath(), statErr, fileSize(info), initResult.Output)
	}
	if result := runner.runCLI("index", "index", "index", "--json"); result.ExitCode != 0 {
		return nil, fmt.Errorf("index oracle fixture: %s", result.Stderr)
	}

	server := oracleModelServer(w.fixture.ModelSQL)
	defer server.Close()
	if err := runner.writeConfig(server.URL); err != nil {
		return nil, err
	}

	var cases []oracleCase
	appendCLI := func(name, operation string, readOnly bool, args ...string) oracleCase {
		var result oracleCase
		if readOnly {
			result = runner.runCLIReadOnly(name, operation, args...)
		} else {
			result = runner.runCLI(name, operation, args...)
		}
		cases = append(cases, result)
		return result
	}
	appendCLI("cli.query.model.json", "query", false, "query", w.fixture.ModelQuestion, "--json")
	appendCLI("cli.query.model.toon", "query", false, "query", w.fixture.ModelQuestion)
	sqlOnly := appendCLI("cli.sql-only", "sql", false, "query", w.fixture.ModelQuestion, "--sql-only", "--json")
	statement, err := sqlFromOracleOutput(sqlOnly.Output)
	if err != nil {
		return nil, err
	}
	appendCLI("cli.exec.generated", "exec", false, "exec", statement, "--json")
	appendCLI("cli.fts-ranking", "exec", false, "exec", oracleFTSSQL, "--json")
	appendCLI("cli.store.first", "store", false, "store", "--layer", "discovery", "--content", w.fixture.StoreContent, "--origin", "agent", "--json")
	appendCLI("cli.store.idempotent", "store", false, "store", "--layer", "discovery", "--content", w.fixture.StoreContent, "--origin", "agent", "--json")
	appendCLI("cli.store.count", "exec", false, "exec", "SELECT COUNT(*) AS copies FROM memories WHERE content = 'Synthetic idempotence beacon for DATA SPLIT.'", "--json")
	appendCLI("cli.exec.write-refused", "exec", false, "exec", "DELETE FROM memories", "--json")
	appendCLI("cli.store.read-only", "store", true, "store", "--layer", "discovery", "--content", "Synthetic refused write.", "--json")
	appendCLI("cli.store.read-only-count", "exec", false, "exec", "SELECT COUNT(*) AS copies FROM memories WHERE content = 'Synthetic refused write.'", "--json")

	if err := runner.writeConfig("http://127.0.0.1:1"); err != nil {
		return nil, err
	}
	appendCLI("cli.query.literal-rescue", "query", false, "query", w.fixture.LiteralQuestion, "--json")
	appendCLI("cli.query.empty-failure", "query", false, "query", w.fixture.MissingQuestion, "--json")
	if err := runner.writeConfig(server.URL); err != nil {
		return nil, err
	}

	mcpCases, err := runner.recordMCP(w.fixture)
	if err != nil {
		return nil, err
	}
	cases = append(cases, mcpCases...)
	bundle := oracleGolden{Schema: oracleGoldenSchema, Cases: cases}
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(bundle); err != nil {
		return nil, err
	}
	return raw.Bytes(), nil
}

func fileSize(info os.FileInfo) any {
	if info == nil {
		return nil
	}
	return info.Size()
}

const oracleFTSSQL = "SELECT 'memory' AS source, m.id, m.layer, m.content AS text, f.rank FROM (SELECT rowid AS fila, bm25(memories_fts) AS rank FROM memories_fts WHERE memories_fts MATCH '\"quartz\"') AS f JOIN memories AS m ON m.id = f.fila ORDER BY f.rank, m.id LIMIT 10"

type oracleRunner struct {
	binary     string
	home       string
	normalizer compatibility.Normalizer
}

func (r *oracleRunner) dbPath() string { return filepath.Join(r.home, ".roca", "roca.db") }

func (r *oracleRunner) environment(readOnly bool) []string {
	env := []string{
		"HOME=" + r.home,
		"PATH=" + filepath.Join(r.home, "bin"),
		"TMPDIR=" + filepath.Join(r.home, "tmp"),
	}
	if readOnly {
		env = append(env, "ROCA_READ_ONLY=1")
	}
	return env
}

func (r *oracleRunner) writeConfig(endpoint string) error {
	body := fmt.Sprintf("[models]\norder = [\"ollama\"]\ntimeout_ms = 1000\nprobe_ms = 500\n\n[models.ollama]\nbase_url = %q\nmodel = \"oracle-model\"\n\n[features]\noracle_shadow = true\n", endpoint)
	return os.WriteFile(filepath.Join(r.home, ".roca", "config.toml"), []byte(body), 0o600)
}

func (r *oracleRunner) runCLI(name, operation string, args ...string) oracleCase {
	return r.runCLIWithEnvironment(name, operation, false, args...)
}

func (r *oracleRunner) runCLIReadOnly(name, operation string, args ...string) oracleCase {
	return r.runCLIWithEnvironment(name, operation, true, args...)
}

func (r *oracleRunner) runCLIWithEnvironment(name, operation string, readOnly bool, args ...string) oracleCase {
	command := exec.Command(r.binary, args...)
	command.Env = r.environment(readOnly)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		code = -1
	}
	return oracleCase{
		Name: name, Surface: "cli", Operation: operation, ExitCode: code,
		Output: r.normalizeOutput(stdout.Bytes()), Stderr: r.normalizer.Text(stderr.String()),
	}
}

func (r *oracleRunner) normalizeOutput(raw []byte) string {
	if json.Valid(bytes.TrimSpace(raw)) {
		if normalized, err := r.normalizer.JSON(raw); err == nil {
			return normalized
		}
	}
	return r.normalizer.Text(string(raw))
}

func (r *oracleRunner) recordMCP(fixture oracleFixture) ([]oracleCase, error) {
	session, closeSession, err := r.openMCP(false)
	if err != nil {
		return nil, err
	}
	defer closeSession()
	var cases []oracleCase
	call := func(name, operation, tool string, arguments map[string]any) error {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: arguments})
		if err != nil {
			return err
		}
		cases = append(cases, oracleCase{
			Name: name, Surface: "mcp", Operation: operation, IsError: result.IsError,
			Output: r.normalizer.Text(renderedText(result)),
		})
		return nil
	}
	if err := call("mcp.query", "query", "roca_query", map[string]any{"query": fixture.ModelQuestion}); err != nil {
		return nil, err
	}
	if err := call("mcp.exec", "exec", "roca_exec", map[string]any{"sql": oracleFTSSQL}); err != nil {
		return nil, err
	}
	store := map[string]any{"layer": "discovery", "content": fixture.MCPStoreContent, "origin": "agent"}
	if err := call("mcp.store.first", "store", "roca_store", store); err != nil {
		return nil, err
	}
	if err := call("mcp.store.idempotent", "store", "roca_store", store); err != nil {
		return nil, err
	}
	if err := call("mcp.query.missing-argument", "failure", "roca_query", map[string]any{}); err != nil {
		return nil, err
	}
	closeSession()

	readOnly, closeReadOnly, err := r.openMCP(true)
	if err != nil {
		return nil, err
	}
	defer closeReadOnly()
	result, err := readOnly.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "roca_store", Arguments: map[string]any{"layer": "discovery", "content": "Synthetic refused MCP write."},
	})
	if err != nil {
		return nil, err
	}
	cases = append(cases, oracleCase{
		Name: "mcp.store.read-only", Surface: "mcp", Operation: "store", IsError: result.IsError,
		Output: r.normalizer.Text(renderedText(result)),
	})
	result, err = readOnly.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "roca_exec", Arguments: map[string]any{"sql": "SELECT COUNT(*) AS copies FROM memories WHERE content = 'Synthetic refused MCP write.'"},
	})
	if err != nil {
		return nil, err
	}
	cases = append(cases, oracleCase{
		Name: "mcp.store.read-only-count", Surface: "mcp", Operation: "exec", IsError: result.IsError,
		Output: r.normalizer.Text(renderedText(result)),
	})
	return cases, nil
}

func (r *oracleRunner) openMCP(readOnly bool) (*mcp.ClientSession, func(), error) {
	command := exec.Command(r.binary, "mcp", "serve")
	command.Env = r.environment(readOnly)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "data-split-oracle", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open oracle MCP session: %w: %s", err, stderr.String())
	}
	return session, func() { _ = session.Close() }, nil
}

func seedOracleMemories(path string, memories []oracleMemory) error {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer database.Close()
	for _, memory := range memories {
		_, err := database.Exec(`INSERT INTO memories
            (id, layer, content, metadata, origin, source_agent, source_model,
             source_surface, project, status, created_at)
            VALUES (?, ?, ?, '{}', 'agent', ?, ?, ?, ?, 'active', ?)`,
			memory.ID, memory.Layer, memory.Content, memory.SourceAgent, memory.SourceModel,
			memory.SourceSurface, memory.Project, memory.CreatedAt)
		if err != nil {
			return fmt.Errorf("seed synthetic memory %d: %w", memory.ID, err)
		}
	}
	return nil
}

func oracleModelServer(statement string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(out http.ResponseWriter, _ *http.Request) {
		out.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(out, `{"models":[{"name":"oracle-model","model":"oracle-model"}]}`)
	})
	mux.HandleFunc("/api/chat", func(out http.ResponseWriter, _ *http.Request) {
		out.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(out).Encode(map[string]any{"message": map[string]string{"content": statement}})
	})
	return httptest.NewServer(mux)
}

func sqlFromOracleOutput(normalized string) (string, error) {
	var document map[string]any
	if err := json.Unmarshal([]byte(normalized), &document); err != nil {
		return "", err
	}
	statement := strings.TrimSpace(fmt.Sprint(document["sql"]))
	if statement == "" || !strings.HasPrefix(strings.ToUpper(statement), "SELECT") {
		return "", fmt.Errorf("SQL-only case did not return SELECT: %s", normalized)
	}
	return statement, nil
}

func oraclePublicKey() ed25519.PublicKey {
	raw, _ := base64.StdEncoding.DecodeString(oraclePublicKeyBase64)
	return ed25519.PublicKey(raw)
}

func (w *oracleWorld) cliCoverage() error {
	return requireOracleCases(w.bundle, "cli", []string{
		"cli.exec.generated", "cli.exec.write-refused", "cli.fts-ranking",
		"cli.query.empty-failure", "cli.query.literal-rescue", "cli.query.model.json",
		"cli.query.model.toon", "cli.sql-only", "cli.store.first",
		"cli.store.count", "cli.store.idempotent", "cli.store.read-only",
		"cli.store.read-only-count",
	})
}

func (w *oracleWorld) mcpCoverage() error {
	return requireOracleCases(w.bundle, "mcp", []string{
		"mcp.exec", "mcp.query", "mcp.query.missing-argument", "mcp.store.first",
		"mcp.store.idempotent", "mcp.store.read-only", "mcp.store.read-only-count",
	})
}

func requireOracleCases(bundle oracleGolden, surface string, want []string) error {
	if bundle.Schema != oracleGoldenSchema {
		return fmt.Errorf("golden schema = %q, want %q", bundle.Schema, oracleGoldenSchema)
	}
	var got []string
	for _, testCase := range bundle.Cases {
		if testCase.Surface == surface {
			got = append(got, testCase.Name)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%s golden cases = %v, want %v", surface, got, want)
	}
	return nil
}

func (w *oracleWorld) changeSignedGolden() error {
	public := oraclePublicKey()
	if _, err := compatibility.VerifyBundle(w.root, public); err != nil {
		return err
	}
	tampered, cleanup, err := oracleTempDir("tampered-")
	if err != nil {
		return err
	}
	defer cleanup()
	for _, name := range []string{"fixture.json", "golden.json", "manifest.json"} {
		raw, err := os.ReadFile(filepath.Join(w.root, name))
		if err != nil {
			return err
		}
		if name == "golden.json" {
			raw = append(raw, ' ')
		}
		if err := os.WriteFile(filepath.Join(tampered, name), raw, 0o600); err != nil {
			return err
		}
	}
	_, w.err = compatibility.VerifyBundle(tampered, public)
	return nil
}

func oracleTempDir(prefix string) (string, func(), error) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".tmp"))
	if err != nil {
		return "", func() {}, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", func() {}, err
	}
	directory, err := os.MkdirTemp(root, "data-split-oracle-"+prefix)
	if err != nil {
		return "", func() {}, err
	}
	return directory, func() { _ = os.RemoveAll(directory) }, nil
}

func (w *oracleWorld) changedGoldenRejected() error {
	if w.err == nil || !strings.Contains(w.err.Error(), "digest") {
		return fmt.Errorf("changed golden error = %v, want digest refusal", w.err)
	}
	return nil
}

func compactDiff(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	limit := len(wantLines)
	if len(gotLines) < limit {
		limit = len(gotLines)
	}
	for index := 0; index < limit; index++ {
		if wantLines[index] != gotLines[index] {
			return fmt.Sprintf("line %d\n-%s\n+%s", index+1, wantLines[index], gotLines[index])
		}
	}
	return fmt.Sprintf("line counts differ: want %d, got %d", len(wantLines), len(gotLines))
}
