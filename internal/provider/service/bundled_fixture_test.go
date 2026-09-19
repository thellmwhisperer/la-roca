package service_test

import (
	"database/sql"

	"path/filepath"

	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	_ "modernc.org/sqlite"
)

func enabledRocaOps(t *testing.T) (*service.Service, string) {
	t.Helper()
	paths := freshPaths(t)
	plugins := ensureRocaOps(t, paths)
	svc := initialized(t, paths, func(options *service.Options) {
		options.PluginDir, options.RocaOpsEnabled = plugins, true
	})
	return svc, plugins
}

func ensureRocaOps(t *testing.T, paths testPaths) string {
	t.Helper()
	root := filepath.Join(paths.data, "plugins")
	if _, err := rocaops.Ensure(root, filepath.Join(paths.data, "bin"), "v-test"); err != nil {
		t.Fatal(err)
	}
	return root
}

func openRocaOps(t *testing.T, root string) *sql.DB {
	t.Helper()
	descriptor, err := plugin.Inspect(rocaops.Name, filepath.Join(root, rocaops.Name))
	if err != nil {
		t.Fatal(err)
	}
	return openSQLite(t, descriptor.Database)
}

func openSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func scopedBundledPlugins(t *testing.T) (testPaths, string) {
	t.Helper()
	paths := freshPaths(t)
	plugins := ensureRocaOps(t, paths)
	if _, err := rocacorpus.Ensure(plugins, filepath.Join(paths.data, "bin"), "v-test"); err != nil {
		t.Fatal(err)
	}
	return paths, plugins
}

func TestRocaOpsDrainOnlyRemovesExplicitlyExpiredRows(t *testing.T) {
	svc, plugins := enabledRocaOps(t)
	for _, testCase := range []struct {
		content   string
		expiresAt string
	}{
		{content: "synthetic expired handoff", expiresAt: "2026-08-12T00:00:00Z"},
		{content: "synthetic future handoff", expiresAt: "2026-08-14T00:00:00Z"},
		{content: "synthetic immortal handoff"},
	} {
		metadata := map[string]any{}
		if testCase.expiresAt != "" {
			metadata["expires_at"] = testCase.expiresAt
		}
		_, err := svc.Store(t.Context(), service.StoreRequest{
			Layer: "discovery", Content: testCase.content, Metadata: metadata,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	result, err := svc.DrainRocaOps(t.Context(), time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 1 {
		t.Fatalf("drain = %+v, want one removed row", result)
	}
	opsDB := openRocaOps(t, plugins)
	defer opsDB.Close()
	var remaining int
	if err := opsDB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("remaining = %d, want future and immortal rows", remaining)
	}
}

func TestRocaOpsExactStoreGuardIncludesExpiry(t *testing.T) {
	svc, _ := enabledRocaOps(t)
	request := service.StoreRequest{Layer: "discovery", Content: "synthetic exact ops retry",
		Metadata: map[string]any{"expires_at": "2026-08-18T00:00:00Z"}}
	first, err := svc.Store(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := svc.Store(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Skipped || retry.ID != first.ID {
		t.Fatalf("ops retry = %+v, want canonical %d", retry, first.ID)
	}
	request.Metadata = map[string]any{"expires_at": "2026-08-19T00:00:00Z"}
	near, err := svc.Store(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if near.Skipped || near.ID == first.ID {
		t.Fatal("different ops expiry was coalesced")
	}
}

func TestOpsStoreIssuesShortIdsAndKeepsHistoricalIdsAddressable(t *testing.T) {
	svc, plugins := enabledRocaOps(t)
	opsDB := openRocaOps(t, plugins)
	defer opsDB.Close()
	const historical int64 = 1152921504606853945
	if _, err := opsDB.Exec(`INSERT INTO memories (id, layer, content, origin, project, status, created_at)
		VALUES (?, 'handoff', 'pre-migration workspace handoff', 'agent', 'workspace', 'active', '2026-01-01 00:00:00')`,
		historical); err != nil {
		t.Fatal(err)
	}
	opsPath := filepath.Join(plugins, rocaops.Name, rocaops.DatabaseFilename)
	if err := rocaops.ApplySchema(opsPath); err != nil {
		t.Fatal(err)
	}

	first, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "discovery", Project: "la-roca-e2e", Content: "id-size probe",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID < 1 || first.ID > 1<<53-1 {
		t.Fatalf("stored id %d is not a newly issued sqlite id", first.ID)
	}

	var compactID int64
	if err := opsDB.QueryRow(`SELECT id FROM memories WHERE legacy_id = ?`, historical).Scan(&compactID); err != nil {
		t.Fatalf("historical id %d is not addressable through legacy_id: %v", historical, err)
	}
	resolved, err := svc.ResolveMemory(t.Context(), historical)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.CanonicalID != compactID {
		t.Fatalf("resolve %d = %+v, want canonical %d", historical, resolved, compactID)
	}
	handoffs, err := svc.LatestHandoffs(t.Context(), "workspace")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range handoffs.Handoffs {
		if row.ID == compactID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("handoff latest dropped compacted historical row %d: %+v", compactID, handoffs.Handoffs)
	}

	second, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "discovery", Project: "la-roca-e2e", Content: "id-size probe 2",
		Supersedes: first.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var supersedes sql.NullInt64
	if err := opsDB.QueryRow(`SELECT supersedes FROM memories WHERE id = ?`, second.ID).Scan(&supersedes); err != nil {
		t.Fatal(err)
	}
	if !supersedes.Valid || supersedes.Int64 != first.ID {
		t.Fatalf("supersedes = %+v, want %d", supersedes, first.ID)
	}
	const dedupAlias int64 = 1152921504606853999
	if _, err := opsDB.Exec(`CREATE TABLE IF NOT EXISTS memory_id_remaps (
		old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL REFERENCES memories(id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := opsDB.Exec(`INSERT INTO memory_id_remaps(old_id, canonical_id) VALUES (?, ?)`, dedupAlias, first.ID); err != nil {
		t.Fatal(err)
	}
	third, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "discovery", Project: "la-roca-e2e", Content: "id-size probe 3",
		Supersedes: dedupAlias,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := opsDB.QueryRow(`SELECT supersedes FROM memories WHERE id = ?`, third.ID).Scan(&supersedes); err != nil {
		t.Fatal(err)
	}
	if !supersedes.Valid || supersedes.Int64 != first.ID {
		t.Fatalf("dedup alias supersedes = %+v, want %d", supersedes, first.ID)
	}
}
