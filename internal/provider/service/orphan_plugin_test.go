package service_test

import (
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	_ "modernc.org/sqlite"
)

// Issue #461: a real `layers` table left inside the roca-corpus database by an
// interrupted migrate must not take the whole service down. The plugin keeps
// serving, doctor names the table, and the table's rows are still there.
func TestACorpusOrphanTableWarnsWithoutTakingTheServiceDown(t *testing.T) {
	paths, plugins := scopedBundledPlugins(t)
	corpus, err := sql.Open("sqlite",
		filepath.Join(plugins, "roca-corpus", "roca-corpus.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.Exec(`CREATE TABLE layers (
		name TEXT PRIMARY KEY, description TEXT NOT NULL, schema_file TEXT NOT NULL,
		access_mode TEXT DEFAULT 'read-write', ingest_allowed INTEGER DEFAULT 1,
		is_coordination INTEGER DEFAULT 0, search_excluded INTEGER DEFAULT 0,
		alias_of TEXT, added_by TEXT DEFAULT 'kernel', deprecated INTEGER DEFAULT 0,
		lifecycle TEXT DEFAULT 'curated', capabilities TEXT DEFAULT '{}',
		since_version TEXT)`); err != nil {
		corpus.Close()
		t.Fatal(err)
	}
	if _, err := corpus.Exec(`INSERT INTO layers (name, description, schema_file)
		VALUES ('knowledge', '#461 fixture layer', '')`); err != nil {
		corpus.Close()
		t.Fatal(err)
	}
	if err := corpus.Close(); err != nil {
		t.Fatal(err)
	}

	svc := initialized(t, paths, func(options *service.Options) {
		options.PluginDir = plugins
		options.RocaOpsEnabled = true
		options.CorpusEnabled = true
	})
	report, err := svc.Doctor(t.Context())
	if err != nil {
		t.Fatalf("doctor on an orphan-carrying corpus database: %v", err)
	}
	named := slices.ContainsFunc(report.Warnings, func(warning string) bool {
		return strings.Contains(warning, "roca-corpus") && strings.Contains(warning, "layers")
	})
	if !named {
		t.Fatalf("doctor warnings %v do not name the corpus orphan table", report.Warnings)
	}

	orphans, err := sql.Open("sqlite",
		filepath.Join(plugins, "roca-corpus", "roca-corpus.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer orphans.Close()
	var rows int
	if err := orphans.QueryRow(`SELECT count(*) FROM layers`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("the orphan table lost rows: %d", rows)
	}
}
