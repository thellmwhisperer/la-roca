package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperatorRideFileGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rides.toml")
	if err := os.WriteFile(path, []byte(`[ride.backup]
command = "echo backup"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckOperatorRideFile(path); err != nil {
		t.Fatalf("owner plus 0600: %v", err)
	}

	if err := os.Chmod(path, 0o664); err != nil {
		t.Fatal(err)
	}
	if err := CheckOperatorRideFile(path); err == nil ||
		!strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("group-writable: %v", err)
	}

	if err := os.Chmod(path, 0o606); err != nil {
		t.Fatal(err)
	}
	if err := CheckOperatorRideFile(path); err == nil ||
		!strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("other-writable: %v", err)
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	rides, warnings, err := DiscoverOperatorRides(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rides) != 1 || len(warnings) != 0 || rides[0].Name != "backup" {
		t.Fatalf("trusted discovery = %+v warnings = %v", rides, warnings)
	}
	if err := os.Chmod(path, 0o664); err != nil {
		t.Fatal(err)
	}
	rides, warnings, err = DiscoverOperatorRides(path, "")
	if err == nil || len(rides) != 0 || len(warnings) != 0 ||
		!strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("untrusted discovery = rides=%+v warnings=%v err=%v", rides, warnings, err)
	}
}
