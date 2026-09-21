package cli

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestHealthRemedyRoundTripClearsASeededLabHome(t *testing.T) {
	fixture := fixtureInstallation(t)
	writeConfig(t, fixture.home, coreMemoryFeatureConfig)
	dbPath := filepath.Join(fixture.home, ".roca", "roca.db")

	runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--content", "keeper memory that must survive", "--origin", "agent")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`PRAGMA foreign_keys=OFF;
		INSERT INTO memories (layer, content, origin, supersedes)
		VALUES ('discovery', 'orphan fixture', 'agent', 99999);
		INSERT INTO memories (layer, content, origin, metadata)
		VALUES ('discovery', 'test metadata fixture', 'agent', '{"_test":true}');
		INSERT INTO memories (layer, content, origin, source_agent)
		VALUES ('discovery', 'test agent fixture', 'agent', 'test-agent');
		INSERT INTO memories (layer, content, origin)
		VALUES ('handover', 'alias layer fixture', 'agent');
		INSERT INTO memories (layer, content, origin)
		VALUES ('knowledge', 'unknown layer fixture', 'agent');`)
	closeErr := db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}

	human := runRoot(t, contractBuild(), "health")
	if !strings.HasPrefix(human, "health: fail") {
		t.Fatalf("seeded health = %q", human)
	}
	remedies := printedRemedies(human)
	if len(remedies) == 0 {
		t.Fatalf("failing health named no remedies:\n%s", human)
	}
	doc := mustJSON(t, runRoot(t, contractBuild(), "health", "--json"))
	if doc["status"] != service.HealthFail {
		t.Fatalf("json health status = %v", doc["status"])
	}
	checks, _ := doc["checks"].(map[string]any)
	for name, raw := range checks {
		check, _ := raw.(map[string]any)
		if check["status"] != service.HealthFail {
			continue
		}
		remedy, _ := check["remedy"].(string)
		if remedy == "" {
			t.Fatalf("failing check %s has no remedy: %v", name, check)
		}
		if !containsRemedy(remedies, remedy) {
			t.Fatalf("printed remedies %v do not include %q", remedies, remedy)
		}
	}

	for _, remedy := range remedies {
		args, ok := doctorRepairArgs(remedy)
		if !ok {
			t.Fatalf("remedy is not an executable doctor repair: %q", remedy)
		}
		out := runRoot(t, contractBuild(), args...)
		if !strings.Contains(out, "repaired ") {
			t.Fatalf("remedy %v did not report a repair:\n%s", args, out)
		}
	}

	after := mustJSON(t, runRoot(t, contractBuild(), "health", "--json"))
	if after["status"] != service.HealthPass {
		t.Fatalf("health after remedies = %v", after)
	}

	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var keepers, testRows, orphans, aliases int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE content = 'keeper memory that must survive'`).
		Scan(&keepers); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories
		WHERE content IN ('test metadata fixture', 'test agent fixture')`).
		Scan(&testRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories
		WHERE supersedes IS NOT NULL AND supersedes NOT IN (SELECT id FROM memories)`).
		Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE layer = 'handover'`).
		Scan(&aliases); err != nil {
		t.Fatal(err)
	}
	if keepers != 1 {
		t.Fatal("the keeper memory was deleted")
	}
	if testRows != 0 || orphans != 0 || aliases != 0 {
		t.Fatalf("named defects remain: testRows=%d orphans=%d aliases=%d",
			testRows, orphans, aliases)
	}
	var unknown int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE content = 'unknown layer fixture'`).
		Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if unknown != 1 {
		t.Fatal("the unknown-layer memory was deleted instead of registered")
	}
}

func printedRemedies(output string) []string {
	var remedies []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "remedy:"); ok {
			remedies = append(remedies, strings.TrimSpace(rest))
		}
	}
	return remedies
}

func containsRemedy(remedies []string, want string) bool {
	for _, remedy := range remedies {
		if remedy == want {
			return true
		}
	}
	return false
}

func doctorRepairArgs(remedy string) ([]string, bool) {
	fields := strings.Fields(remedy)
	if len(fields) < 3 || fields[0] != "roca" {
		return nil, false
	}
	return fields[1:], true
}
