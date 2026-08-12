package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestExecAppliesTheDefaultCharacterBudget(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	long := strings.Repeat("abcdefghij", 80)

	result, err := svc.Exec(t.Context(), service.ExecRequest{
		SQL: "SELECT '" + long + "' AS text",
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	text := result.Rows[0]["text"].(string)
	if len([]rune(text)) > service.DefaultMaxChars || !strings.HasSuffix(text, "…") {
		t.Fatalf("default-budget text = %d runes, %q", len([]rune(text)), text)
	}
}

func TestExecStopsAQueryThatExceedsTheCostBudget(t *testing.T) {
	paths := freshPaths(t)
	svc := serviceOn(t, paths, func(options *service.Options) {
		options.QueryTimeout = time.Millisecond
	})
	if _, err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	_, err := svc.Exec(t.Context(), service.ExecRequest{SQL: `
		WITH RECURSIVE costly(n) AS (
			SELECT 1 UNION ALL SELECT n + 1 FROM costly WHERE n < 100000000
		) SELECT sum(n) FROM costly`})
	if err == nil {
		t.Fatal("runaway recursive query completed without the cost budget")
	}
	if time.Since(started) > time.Second {
		t.Fatalf("query timeout took too long: %v", time.Since(started))
	}
}

func TestExecNeverAppliesModelSQLRepairs(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	for _, statement := range []string{
		"```sql\nSELECT id FROM memories LIMIT 1\n```",
		"Here is the query:\nSELECT id FROM memories LIMIT 1",
		"SELECT id FROM memories ORDER BY id LIMIT 1 UNION ALL SELECT id FROM exchanges LIMIT 1",
	} {
		if _, err := svc.Exec(t.Context(), service.ExecRequest{SQL: statement}); err == nil {
			t.Errorf("Exec silently repaired user SQL %q", statement)
		}
	}
}

func TestInitCreatesTheDatabaseAndSyncsTheLayerRegistry(t *testing.T) {
	svc, _ := openService(t)
	ctx := context.Background()

	result, err := svc.Init(ctx)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if result.Database != "created" {
		t.Errorf("database = %q, want created", result.Database)
	}
	if result.DBPath == "" {
		t.Error("empty db_path")
	}

	var layers int
	if err := svc.DB().SQL().QueryRow("SELECT COUNT(*) FROM layers").Scan(&layers); err != nil {
		t.Fatalf("COUNT layers: %v", err)
	}
	if layers != 12 {
		t.Errorf("layers synced = %d, want 12", layers)
	}
}

func TestInitCarriesFactoryBinarySelectionMetadata(t *testing.T) {
	paths := freshPaths(t)
	local := answering("codex", "")
	local.commandTransport = true
	svc := serviceOn(t, paths, func(options *service.Options) {
		options.Providers = provider.Cascade{
			Providers: []provider.Provider{local}, DetectedBinaries: []string{"codex"}, FactoryDefault: true,
		}
	})
	result, err := svc.Init(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.DetectedModelBinaries, ",") != "codex" ||
		strings.Join(result.MissingModelBinaries, ",") != "claude" ||
		!result.FactoryDefault || result.FactoryDefaultProvider != "codex" {
		t.Fatalf("init model metadata = %+v", result)
	}
}

func TestInitReportsPromptWriteFailureWithoutDiscardingItsResult(t *testing.T) {
	paths := freshPaths(t)
	if err := os.Mkdir(filepath.Join(paths.data, "prompt.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	svc := serviceOn(t, paths)
	result, err := svc.Init(t.Context())
	if err != nil {
		t.Fatalf("database bootstrap failed with the prompt write: %v", err)
	}
	if result.Database == "" || len(result.Warnings) != 1 {
		t.Fatalf("partial result = %+v", result)
	}
	if result.PromptPath != "" {
		t.Fatalf("failed prompt path is still advertised: %q", result.PromptPath)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	svc, path := openService(t)
	ctx := context.Background()
	if _, err := svc.Init(ctx); err != nil {
		t.Fatalf("first Init: %v", err)
	}

	second, err := svc.Init(ctx)
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if second.Database != "adopted" {
		t.Errorf("database = %q on the second pass, want adopted", second.Database)
	}
	if second.DBPath != path {
		t.Errorf("db_path = %q, want %q: the database does not move", second.DBPath, path)
	}
	var layers int
	if err := svc.DB().SQL().QueryRow("SELECT COUNT(*) FROM layers").Scan(&layers); err != nil {
		t.Fatalf("COUNT layers: %v", err)
	}
	if layers != 12 {
		t.Errorf("layers = %d after two inits, want 12", layers)
	}
}

// --- helpers ---

// testPaths are those of a toy installation: database, backups and the data
// directory personal artefacts hang off.
type testPaths struct{ db, backups, cache, data string }

// openService opens the database without initializing it: it is what the init
// tests need, since they measure precisely what that first pass does.
func openService(t *testing.T) (*service.Service, string) {
	t.Helper()
	paths := freshPaths(t)
	return serviceOn(t, paths), paths.db
}

func freshPaths(t *testing.T) testPaths {
	t.Helper()
	dir := t.TempDir()
	return testPaths{
		db:      filepath.Join(dir, "roca.db"),
		backups: filepath.Join(dir, "backups"),
		cache:   filepath.Join(dir, "cache"),
		data:    dir,
	}
}

// serviceWithPaths opens an already initialized installation.
func serviceWithPaths(t *testing.T) (*service.Service, testPaths) {
	t.Helper()
	paths := freshPaths(t)
	svc := serviceOn(t, paths)
	if _, err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return svc, paths
}

func serviceOn(t *testing.T, paths testPaths, different ...func(*service.Options)) *service.Service {
	t.Helper()
	options := baseOptions(paths)
	for _, apply := range different {
		apply(&options)
	}
	svc, err := service.Open(options)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}

func baseOptions(paths testPaths) service.Options {
	return service.Options{
		DBPath:    paths.db,
		BackupDir: paths.backups,
		DataDir:   paths.data,
		Version:   "0.0.0-test",
		Commit:    "0123456789abcdef",
	}
}

func seededService(t *testing.T) *service.Service {
	t.Helper()
	svc, _ := serviceWithPaths(t)
	seedTheUsualMemories(t, svc)
	return svc
}

// seededServiceWith is the same seeded installation with a model cascade
// plugged in. The model tests need it. A second cascade is the installation
// that splits the two inferences: the rows go to it and nowhere else.
func seededServiceWith(t *testing.T, providers provider.Cascade,
	interpreters ...provider.Cascade) *service.Service {
	t.Helper()
	svc := initialized(t, freshPaths(t), func(options *service.Options) {
		options.Providers = providers
		if len(interpreters) > 0 {
			options.Interpreters = interpreters[0]
		}
	})
	seedTheUsualMemories(t, svc)
	return svc
}

// initialized opens a toy installation and runs Init over it. Every constructor
// in this suite goes through it and says what makes its own installation
// different in the one function it passes, instead of carrying another copy of
// the same Options literal.
func initialized(t *testing.T, paths testPaths,
	different ...func(*service.Options)) *service.Service {
	t.Helper()
	svc := serviceOn(t, paths, different...)
	if _, err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return svc
}

// readOnlyService is an already initialized installation reopened in read-only
// mode: the database exists and has its schema, and the service refuses every
// write over it before touching the disk.
func readOnlyService(t *testing.T) *service.Service {
	t.Helper()
	paths := freshPaths(t)
	if _, err := serviceOn(t, paths).Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return serviceOn(t, paths, func(o *service.Options) { o.ReadOnly = true })
}

func seedTheUsualMemories(t *testing.T, svc *service.Service) {
	t.Helper()
	seed(t, svc, "project", "the team hates long dashes in the generated text")
	seed(t, svc, "feedback", "layer anchor for the layer constraint")
	seed(t, svc, "project", "a very long memory: "+strings.Repeat("filler ", 800))
}

func seed(t *testing.T, svc *service.Service, layer, content string) {
	t.Helper()
	_, err := svc.DB().SQL().Exec(
		"INSERT INTO memories (layer, content, origin) VALUES (?, ?, 'agent')", layer, content)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func memoriesInTheDatabase(t *testing.T, svc *service.Service) int {
	t.Helper()
	var n int
	if err := svc.DB().SQL().QueryRow(
		"SELECT COUNT(*) FROM memories WHERE layer = 'feedback'").Scan(&n); err != nil {
		t.Fatalf("COUNT: %v", err)
	}
	return n
}

func firstRowText(t *testing.T, res service.QueryResult) string {
	t.Helper()
	if len(res.Rows) == 0 {
		t.Fatal("there are no rows")
	}
	text, _ := res.Rows[0]["text"].(string)
	return text
}
