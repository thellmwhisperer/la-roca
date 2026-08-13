package config_test

import (
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestTheDefaultDatabaseLivesUnderTheHome(t *testing.T) {
	home := t.TempDir()
	paths, err := config.Resolve(config.Input{Home: home})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := filepath.Join(home, ".roca", "roca.db")
	if paths.DB != want {
		t.Errorf("database = %q, want %q", paths.DB, want)
	}
	if paths.Backups != filepath.Join(home, ".roca", "backups") {
		t.Errorf("backups = %q", paths.Backups)
	}
	if paths.Reconciliation != filepath.Join(home, ".roca", "reconciliation.json") {
		t.Errorf("reconciliation = %q", paths.Reconciliation)
	}
	if paths.Artifacts != filepath.Join(home, ".roca", "artifacts.json") {
		t.Errorf("artifact registry = %q", paths.Artifacts)
	}
}

func TestTheFlagBeatsTheEnvironmentAndTheHome(t *testing.T) {
	home := t.TempDir()
	paths, err := config.Resolve(config.Input{
		Home: home, Flag: "/tmp/other.db", Env: "/tmp/environment.db",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if paths.DB != "/tmp/other.db" {
		t.Errorf("database = %q, want the flag's", paths.DB)
	}
}

func TestTheEnvironmentBeatsTheHome(t *testing.T) {
	home := t.TempDir()
	paths, err := config.Resolve(config.Input{Home: home, Env: "/tmp/environment.db"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if paths.DB != "/tmp/environment.db" {
		t.Errorf("database = %q, want the environment's", paths.DB)
	}
}

func TestWithoutAHomeNoPathIsGuessed(t *testing.T) {
	if _, err := config.Resolve(config.Input{}); err == nil {
		t.Fatal("Resolve invented a path with no HOME")
	}
}

// A named database resolves with no HOME at all. Every resolved path has to
// stay absolute there: a relative registry would be created, and later purged,
// against whatever directory each command happened to run in.
func TestANamedDatabaseWithoutAHomeKeepsTheRegistryBesideIt(t *testing.T) {
	dir := t.TempDir()
	paths, err := config.Resolve(config.Input{Flag: filepath.Join(dir, "chosen.db")})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if paths.Artifacts != filepath.Join(dir, "artifacts.json") {
		t.Errorf("artifact registry = %q, want it beside the chosen database", paths.Artifacts)
	}
	if !filepath.IsAbs(paths.Artifacts) {
		t.Errorf("artifact registry is relative to the working directory: %q", paths.Artifacts)
	}
}
