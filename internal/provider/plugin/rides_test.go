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

	rides, warnings := plugin.DiscoverRides(root, allowInstalledRideFixture)
	if len(rides) != 0 || len(warnings) != 3 {
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
