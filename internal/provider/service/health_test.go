package service_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	_ "modernc.org/sqlite"
)

// seedOrphanSupersedes plants what only an aged database has: memories pointing
// at a memory that is no longer there. It is seeded with the foreign keys down
// on purpose, because v1 turns them on and the check exists precisely for the
// databases that were written before anybody did.
func seedOrphanSupersedes(t *testing.T, svc *service.Service, rows int) {
	t.Helper()
	ctx := context.Background()
	conn, err := svc.DB().SQL().Conn(ctx)
	if err != nil {
		t.Fatalf("take a connection: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatalf("lower the foreign keys: %v", err)
	}
	for range rows {
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO memories (layer, content, origin, supersedes)
			 VALUES ('discovery', hex(randomblob(8)), 'agent', 99999)`); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func TestHealthOnACleanInstallationPasses(t *testing.T) {
	svc, _ := serviceWithPaths(t)

	report, err := svc.Health(context.Background(), service.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if report.Status != service.HealthPass {
		t.Errorf("status = %q on a clean installation, want %q",
			report.Status, service.HealthPass)
	}
	if len(report.Checks) == 0 {
		t.Fatal("a health report with no checks is not a diagnosis")
	}
	for name, check := range report.Checks {
		if check.Summary == "" {
			t.Errorf("check %q has no summary: an operator cannot act on it", name)
		}
		if check.Status != service.HealthPass {
			t.Errorf("check %q = %q on a clean installation", name, check.Status)
		}
	}
}

func TestHealthVerdictsUseTheFirstReadableLayerRegistry(t *testing.T) {
	memory := openVerdictDatabase(t, `
		CREATE TABLE memories (
			id INTEGER PRIMARY KEY, layer TEXT, supersedes INTEGER, metadata TEXT,
			source_agent TEXT, created_at TEXT
		);
		INSERT INTO memories (id, layer, created_at) VALUES (1, 'handoff', '2026-08-18T12:00:00Z');`)
	empty := openVerdictDatabase(t, `CREATE TABLE layers (name TEXT, alias_of TEXT);`)
	fallback := openVerdictDatabase(t, `
		CREATE TABLE layers (name TEXT, alias_of TEXT);
		INSERT INTO layers (name) VALUES ('handoff');`)
	unavailable := openVerdictDatabase(t, `CREATE TABLE unrelated (id INTEGER);`)

	cases := []struct {
		name         string
		registries   []*sql.DB
		wantRuntime  string
		wantPhysical string
	}{
		{"empty active registry", []*sql.DB{empty, fallback}, service.HealthFail, service.HealthPass},
		{"unavailable active registry", []*sql.DB{unavailable, fallback}, service.HealthPass, service.HealthPass},
		{"no readable registry", []*sql.DB{unavailable}, "skipped", "skipped"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			statuses := map[string]string{}
			for _, verdict := range service.HealthVerdicts(
				t.Context(), testCase.registries, []*sql.DB{memory}, nil) {
				statuses[verdict.Name] = verdict.Status
			}
			if statuses["runtime_layers_not_in_registry"] != testCase.wantRuntime {
				t.Errorf("runtime layer verdict = %q, want %q",
					statuses["runtime_layers_not_in_registry"], testCase.wantRuntime)
			}
			if statuses["physical_alias_layer_rows"] != testCase.wantPhysical {
				t.Errorf("physical alias verdict = %q, want %q",
					statuses["physical_alias_layer_rows"], testCase.wantPhysical)
			}
		})
	}
}

func TestHealthVerdictsDoNotPassAfterADeadline(t *testing.T) {
	schema := `
		CREATE TABLE memories (
			id INTEGER PRIMARY KEY, layer TEXT, supersedes INTEGER, metadata TEXT,
			source_agent TEXT, created_at TEXT
		);`
	passing := openVerdictDatabase(t, schema)
	locked := openVerdictDatabase(t, schema)
	locked.SetMaxOpenConns(1)
	connection, err := locked.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if _, err := connection.ExecContext(t.Context(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = connection.ExecContext(t.Context(), `ROLLBACK`) })

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	statuses := map[string]string{}
	for _, verdict := range service.HealthVerdicts(
		ctx, nil, []*sql.DB{passing, locked}, nil) {
		statuses[verdict.Name] = verdict.Status
	}
	for _, name := range []string{"orphan_supersedes", "memory_created_at_formats"} {
		if statuses[name] != "skipped" {
			t.Errorf("%s verdict = %q, want skipped", name, statuses[name])
		}
	}
}

func openVerdictDatabase(t *testing.T, schema string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return db
}

// A memory pointing at a memory that is not there is the check that has to
// fail, and it is the reason this command exists: it reads live data and says
// what is broken in it.
func TestHealthFailsOnAMemoryThatSupersedesNothing(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	seedOrphanSupersedes(t, svc, 1)

	report, err := svc.Health(context.Background(), service.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if report.Status != service.HealthFail {
		t.Errorf("status = %q, want %q", report.Status, service.HealthFail)
	}
	check, ok := report.Checks["orphan_supersedes"]
	if !ok {
		t.Fatal("there is no orphan_supersedes check")
	}
	if check.Count != 1 {
		t.Errorf("count = %d, want 1", check.Count)
	}
	if len(check.Rows) == 0 {
		t.Error("a failing check with no sample rows cannot be acted on")
	}
}

func TestHealthWarnsAboutALayerThatIsInNoRegistry(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	if _, err := svc.DB().SQL().Exec(
		`INSERT INTO memories (layer, content, origin)
		 VALUES ('a-layer-nobody-declared', 'x', 'agent')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	report, err := svc.Health(context.Background(), service.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	check, ok := report.Checks["runtime_layers_not_in_registry"]
	if !ok {
		t.Fatal("there is no runtime_layers_not_in_registry check")
	}
	if check.Count != 1 {
		t.Errorf("count = %d, want 1", check.Count)
	}
}

func TestLayerRepairsDriveRegistryHealthBackToGreen(t *testing.T) {
	tests := []struct {
		name   string
		repair func(*testing.T, *service.Service)
	}{
		{
			name: "register the runtime layer",
			repair: func(t *testing.T, svc *service.Service) {
				result, err := svc.AddLayer(t.Context(), "a-layer-nobody-declared")
				if err != nil {
					t.Fatal(err)
				}
				if !result.Added {
					t.Fatal("the new layer was not registered")
				}
				if _, err := svc.Store(t.Context(), service.StoreRequest{
					Layer: "a-layer-nobody-declared", Content: "registered write",
				}); err != nil {
					t.Fatalf("store on registered layer: %v", err)
				}
			},
		},
		{
			name: "migrate to a registered layer",
			repair: func(t *testing.T, svc *service.Service) {
				result, err := svc.MigrateLayer(t.Context(),
					"a-layer-nobody-declared", "discovery")
				if err != nil {
					t.Fatal(err)
				}
				if result.Migrated != 1 || result.To != "discovery" {
					t.Fatalf("migration = %+v", result)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, _ := serviceWithPaths(t)
			if _, err := svc.DB().SQL().Exec(
				`INSERT INTO memories (layer, content, origin)
				 VALUES ('a-layer-nobody-declared', 'synthetic drift', 'agent')`); err != nil {
				t.Fatal(err)
			}
			before, err := svc.Health(t.Context(), service.HealthRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if before.Checks["runtime_layers_not_in_registry"].Status != service.HealthFail {
				t.Fatalf("health before repair = %+v", before)
			}

			test.repair(t, svc)
			after, err := svc.Health(t.Context(), service.HealthRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if after.Checks["runtime_layers_not_in_registry"].Status != service.HealthPass {
				t.Fatalf("health after repair = %+v", after)
			}
		})
	}
}

// The sample is capped so that a health report over a database with millions of
// broken rows is still a report and not a dump.
func TestHealthCapsTheSampleItReturns(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	seedOrphanSupersedes(t, svc, 12)

	report, err := svc.Health(context.Background(), service.HealthRequest{MaxRows: 5})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	check := report.Checks["orphan_supersedes"]
	if check.Count != 12 {
		t.Errorf("count = %d, want the 12 that are really there", check.Count)
	}
	if len(check.Rows) != 5 {
		t.Errorf("%d sample rows, want the requested 5", len(check.Rows))
	}
}

// Health reads and never writes, so it is the one check a read-only
// installation still answers.
func TestHealthAnswersInReadOnlyMode(t *testing.T) {
	svc := readOnlyService(t)

	report, err := svc.Health(context.Background(), service.HealthRequest{})
	if err != nil {
		t.Fatalf("Health in read-only mode: %v", err)
	}
	if report.Status != service.HealthPass {
		t.Errorf("status = %q, want %q", report.Status, service.HealthPass)
	}
}

// v1 has no `runs` table: it is v2 scope and the binary creates none. A health
// report that named a check over it would be naming a component this version
// does not have.
func TestHealthNamesNoComponentThisVersionDoesNotHave(t *testing.T) {
	svc, _ := serviceWithPaths(t)

	report, err := svc.Health(context.Background(), service.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	for _, withdrawn := range []string{"runs", "inbox", "proposals"} {
		for name, check := range report.Checks {
			if strings.Contains(name, withdrawn) ||
				strings.Contains(strings.ToLower(check.Summary), withdrawn) {
				t.Errorf("check %q names %q, which v1 does not have", name, withdrawn)
			}
		}
	}
}
