package rocacron_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	_ "modernc.org/sqlite"
)

func TestEnsureInstallsTheCustodialJourneyPluginAndPreservesItsDatabase(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	bin := filepath.Join(t.TempDir(), "bin")
	result, err := rocacron.Ensure(root, bin, "v-test")
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != rocacron.Name || result.Risk != plugininstall.DataOnly || result.Executable != "" {
		t.Fatalf("installed bundle = %+v", result)
	}

	directory := filepath.Join(root, rocacron.Name)
	descriptor, err := plugin.Inspect(rocacron.Name, directory)
	if err != nil {
		t.Fatal(err)
	}
	if !descriptor.Semantic.Custody || descriptor.Semantic.Attachment != plugin.AttachmentOnDemand {
		t.Fatalf("semantic contract = %+v", descriptor.Semantic)
	}
	validated, err := plugin.Validate(t.Context(), descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if len(validated.Tables) != 3 {
		t.Fatalf("visible cron tables = %d, want 3", len(validated.Tables))
	}
	db, err := sql.Open("sqlite", descriptor.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO journeys
		(train, ride, plugin, started_at, ended_at, duration_ms, exit_code, gate_status)
		VALUES ('nightly', 'fixture', 'synthetic', 'start', 'end', 1, 0, 'ready')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rocacron.Ensure(root, bin, "v-next"); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", descriptor.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM journeys WHERE ride = 'fixture'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("preserved journeys = %d, want 1", count)
	}
}
