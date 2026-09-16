package plugin_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

func TestDiscoverRidesReadsEveryInstalledPluginInDeterministicOrder(t *testing.T) {
	root := t.TempDir()
	writeRides(t, root, "vector", `[ride.delta_ingest]
command = "roca vector ingest --delta"
gate = "after_ingest"
`)
	writeRides(t, root, "archive", `[ride.compact]
train = "weekly"
command = "roca archive compact"
`)
	if err := os.Mkdir(filepath.Join(root, "without-rides"), 0o700); err != nil {
		t.Fatal(err)
	}

	rides, warnings := plugin.DiscoverRides(root, allowInstalledRideFixture)
	if len(warnings) != 0 {
		t.Fatalf("ride warnings = %v", warnings)
	}
	if len(rides) != 2 {
		t.Fatalf("rides = %+v", rides)
	}
	if rides[0].Plugin != "archive" || rides[0].Name != "compact" || rides[0].Train != "weekly" {
		t.Fatalf("first ride = %+v", rides[0])
	}
	if rides[1].Plugin != "vector" || rides[1].Name != "delta_ingest" ||
		rides[1].Train != plugin.DefaultTrain || rides[1].Gate != "after_ingest" {
		t.Fatalf("second ride = %+v", rides[1])
	}
}

func TestDiscoverRidesRejectsUnknownFieldsAndUnsafeNames(t *testing.T) {
	root := t.TempDir()
	writeRides(t, root, "unknown-field", `[ride.ingest]
command = "roca ingest"
surprise = true
`)
	writeRides(t, root, "unsafe-name", `[ride."not safe"]
command = "roca ingest"
`)
	writeRides(t, root, "invalid-gate", `[ride.ingest]
command = "roca ingest"
gate = "whenever"
`)
	writeRides(t, root, "unresolvable-gate", `[ride.prune]
command = "roca archive prune"
gate = "after_compact"
`)

	rides, warnings := plugin.DiscoverRides(root, allowInstalledRideFixture)
	if len(rides) != 0 || len(warnings) != 4 {
		t.Fatalf("rides = %+v warnings = %v", rides, warnings)
	}
	for _, warning := range warnings {
		if !strings.Contains(warning, plugin.RidesFilename) {
			t.Errorf("warning does not name %s: %s", plugin.RidesFilename, warning)
		}
	}
}

func TestDiscoverRidesRejectsAnUnverifiedPluginBeforeReadingItsManifest(t *testing.T) {
	root := t.TempDir()
	writeRides(t, root, "unverified", `[ride.payload]
command = "echo should-not-run"
`)

	rides, warnings := plugin.DiscoverRides(root, func(name, directory string) error {
		return fmt.Errorf("%s at %s has no installer proof", name, directory)
	})
	if len(rides) != 0 || len(warnings) != 1 ||
		!strings.Contains(warnings[0], "no installer proof") {
		t.Fatalf("rides = %+v warnings = %v", rides, warnings)
	}
}

func TestDiscoverOperatorRidesResolvesGatesAcrossFiles(t *testing.T) {
	ridesDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ridesDir, "10-export.toml"), []byte(`[ride.export]
command = "echo export"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ridesDir, "20-upload.toml"), []byte(`[ride.upload]
command = "echo upload"
gate = "after_export"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	rides, warnings := plugin.DiscoverOperatorRides("", ridesDir)
	if len(warnings) != 0 || len(rides) != 2 {
		t.Fatalf("rides = %+v warnings = %v", rides, warnings)
	}
	if rides[0].Name != "export" || rides[1].Name != "upload" || rides[1].Gate != "after_export" {
		t.Fatalf("rides = %+v", rides)
	}
}

func TestDiscoverOperatorRidesPreservesIndependentRidesAfterRefusal(t *testing.T) {
	ridesDir := t.TempDir()
	for name, spec := range map[string]struct {
		mode os.FileMode
		body string
	}{
		"10-export.toml":  {mode: 0o664, body: "[ride.export]\ncommand = \"echo export\"\n"},
		"20-upload.toml":  {mode: 0o600, body: "[ride.upload]\ncommand = \"echo upload\"\ngate = \"after_export\"\n"},
		"30-cleanup.toml": {mode: 0o600, body: "[ride.cleanup]\ncommand = \"echo cleanup\"\n"},
	} {
		if err := os.WriteFile(filepath.Join(ridesDir, name), []byte(spec.body), spec.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(ridesDir, name), spec.mode); err != nil {
			t.Fatal(err)
		}
	}

	rides, warnings := plugin.DiscoverOperatorRides("", ridesDir)
	if len(rides) != 1 || rides[0].Name != "cleanup" ||
		!strings.Contains(strings.Join(warnings, "\n"), "upload") ||
		!strings.Contains(strings.Join(warnings, "\n"), "writable by group or others") {
		t.Fatalf("filtered operator rides = %+v warnings = %v", rides, warnings)
	}
}

func allowInstalledRideFixture(string, string) error { return nil }

func writeRides(t *testing.T, root, name, body string) {
	t.Helper()
	directory := filepath.Join(root, name)
	err := os.MkdirAll(directory, 0o700)
	if err == nil {
		err = os.WriteFile(filepath.Join(directory, plugin.RidesFilename), []byte(body), 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
}
