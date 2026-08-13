package service

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
)

func TestResidentInitializationHonorsItsContext(t *testing.T) {
	options := residentTestOptions(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	svc, err := openWithContext(ctx, options)
	if svc != nil {
		svc.Close()
	}
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("resident initialization with a canceled context = %v", err)
	}
}

func TestResidentQueriesAcquireIndependentReadConnections(t *testing.T) {
	svc, err := openWithContext(t.Context(), residentTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	first, firstAttached, err := svc.openQueryConnection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer closeQueryConnection(first, firstAttached)
	second, secondAttached, err := svc.openQueryConnection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer closeQueryConnection(second, secondAttached)
	if first == second {
		t.Fatal("concurrent resident queries share one serialized connection")
	}
	for _, connection := range []*sql.Conn{first, second} {
		var rows int
		if err := connection.QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM plugin_roca_ops.memories").Scan(&rows); err != nil {
			t.Fatalf("resident database is unavailable on an acquired connection: %v", err)
		}
	}
}

func residentTestOptions(t *testing.T) Options {
	t.Helper()
	directory := t.TempDir()
	plugins := filepath.Join(directory, "plugins")
	if _, err := rocaops.Ensure(plugins, filepath.Join(directory, "bin"), "v-test"); err != nil {
		t.Fatal(err)
	}
	return Options{
		DBPath: filepath.Join(directory, "roca.db"), PluginDir: plugins, RocaOpsEnabled: true,
	}
}
