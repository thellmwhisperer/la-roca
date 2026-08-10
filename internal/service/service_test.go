package service_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/service"
)

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

// The search layer finds what was seeded: this is the semantic layer the model
// operates over, measured directly without a model in the path.
func TestTheSearchFindsWhatWasSeeded(t *testing.T) {
	svc := seededService(t)
	ctx := context.Background()

	res, err := svc.Search(ctx, service.SearchRequest{Question: "guiones largos"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.SQL == "" || res.QueryPlan == nil {
		t.Fatalf("empty sql or queryplan: %+v", res)
	}
	if res.RowCount == 0 {
		t.Fatalf("zero rows: the seeded memory was not found")
	}
	if !strings.Contains(firstRowText(t, res), "guiones largos") {
		t.Errorf("the first row does not carry the seed: %v", res.Rows[0])
	}
	if res.Version == "" || res.SourceSHA == "" {
		t.Errorf("the answer does not say which version and which code it comes from: %+v", res)
	}
	if res.Match != service.MatchFound {
		t.Errorf("match = %q, want %q", res.Match, service.MatchFound)
	}
}

// Handoff is session continuity, not private messaging: term search and FTS
// must both surface a handoff memory. Messaging layers stay excluded.
func TestAHandoffMemoryIsSearchableByTermAndFTS(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	ctx := context.Background()
	const marker = "zingalor calabaza"
	seed(t, svc, "handoff", "traspaso donde dejamos "+marker+" para el siguiente agente")
	seed(t, svc, "question", "mensaje privado con "+marker+" que no debe salir en busqueda")
	if _, err := svc.Index(ctx); err != nil {
		t.Fatalf("Index: %v", err)
	}

	for _, method := range []string{"like", "fts"} {
		res, err := svc.Search(ctx, service.SearchRequest{
			Question: marker, Method: method,
		})
		if err != nil {
			t.Fatalf("Search(%s): %v", method, err)
		}
		if res.RowCount == 0 {
			t.Fatalf("Search(%s): zero rows: handoff was excluded from search", method)
		}
		foundHandoff := false
		for _, row := range res.Rows {
			text, _ := row["text"].(string)
			if strings.Contains(text, "traspaso donde dejamos") {
				foundHandoff = true
			}
			if strings.Contains(text, "mensaje privado") {
				t.Errorf("Search(%s) returned a question-layer message: %q", method, text)
			}
		}
		if !foundHandoff {
			t.Errorf("Search(%s): no handoff row among %d results", method, res.RowCount)
		}
	}
}

// Defect 2: the adopted corpus carries genuine duplicate rows — identical
// content, identical timestamp, consecutive ids — and the term search returned
// each one. The answer collapses rows whose source and text are identical,
// keeping the best-ranked, so row_count reflects what the operator actually
// sees. The database is never mutated: the duplicates stay where they are.
func TestIdenticalResultRowsAreDeduplicatedAtAnswerTime(t *testing.T) {
	for _, method := range []string{"like", "fts"} {
		t.Run(method, func(t *testing.T) {
			svc, _ := serviceWithPaths(t)
			ctx := context.Background()
			// Five genuine duplicates: same text, same timestamp, distinct ids.
			const dup = "registro duplicado del despliegue canario"
			const when = "2026-08-01 12:00:00"
			for i := 0; i < 5; i++ {
				if _, err := svc.DB().SQL().Exec(
					"INSERT INTO memories (layer, content, origin, created_at) "+
						"VALUES ('project', ?, 'agent', ?)", dup, when); err != nil {
					t.Fatalf("seed duplicate: %v", err)
				}
			}
			if method == "fts" {
				if _, err := svc.Index(ctx); err != nil {
					t.Fatalf("Index: %v", err)
				}
			}

			res, err := svc.Search(ctx, service.SearchRequest{
				Question: "registro duplicado del despliegue canario",
				Method:   method,
			})
			if err != nil {
				t.Fatalf("Search(%s): %v", method, err)
			}
			if res.RowCount != 1 {
				t.Errorf("Search(%s).row_count = %d, want 1: identical rows were not collapsed",
					method, res.RowCount)
			}
			// Presentation only: the five rows are still in the database.
			var remaining int
			if err := svc.DB().SQL().QueryRow(
				"SELECT COUNT(*) FROM memories WHERE content = ?", dup).Scan(&remaining); err != nil {
				t.Fatalf("COUNT: %v", err)
			}
			if remaining != 5 {
				t.Errorf("the database was mutated: %d duplicate rows remain, want 5", remaining)
			}
		})
	}
}

func TestHonestZeroRowsAreDeclaredAsSuch(t *testing.T) {
	svc := seededService(t)
	res, err := svc.Search(context.Background(), service.SearchRequest{
		Question: "que tiempo hace en madrid",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.RowCount != 0 {
		t.Fatalf("rows = %d, want 0", res.RowCount)
	}
	if res.Match != service.MatchEmpty {
		t.Errorf("match = %q, want %q", res.Match, service.MatchEmpty)
	}
}

func TestALayerConstraintIsAlwaysRespected(t *testing.T) {
	svc := seededService(t)
	res, err := svc.Search(context.Background(), service.SearchRequest{
		Question: "ancla de capa", Layer: "feedback",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.RowCount == 0 {
		t.Fatalf("zero rows: %s", res.SQL)
	}
	for _, row := range res.Rows {
		if row["source"] != "memory" {
			t.Errorf("a row does not come from a layer: %v", row)
		}
	}
}

func TestTheTruncationBudgetIsRespected(t *testing.T) {
	svc := seededService(t)
	res, err := svc.Search(context.Background(), service.SearchRequest{
		Question: "memoria muy larga", MaxChars: 200,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.RowCount == 0 {
		t.Fatalf("zero rows: %s", res.SQL)
	}
	text := firstRowText(t, res)
	if len([]rune(text)) > 200 {
		t.Errorf("the text takes %d characters, want at most 200", len([]rune(text)))
	}
	if !strings.Contains(text, "memoria muy larga") {
		t.Errorf("the truncation ate the match: %q", text)
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
		BenchDir:  filepath.Join(paths.data, "bench"),
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
// plugged in. The model tests need it.
func seededServiceWith(t *testing.T, providers provider.Cascade) *service.Service {
	t.Helper()
	svc := initialized(t, freshPaths(t), func(options *service.Options) {
		options.Providers = providers
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
	seed(t, svc, "project", "el capitan odia los guiones largos en el texto generado")
	seed(t, svc, "feedback", "ancla de capa para la restriccion por capa")
	seed(t, svc, "project", "memoria muy larga: "+strings.Repeat("relleno ", 800))
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
