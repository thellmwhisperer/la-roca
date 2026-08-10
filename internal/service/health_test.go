package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/service"
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
// does not have, which F10-03 forbids.
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
