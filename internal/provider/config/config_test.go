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
}

func TestTheFlagBeatsTheEnvironmentAndTheHome(t *testing.T) {
	home := t.TempDir()
	paths, err := config.Resolve(config.Input{
		Home: home, Flag: "/tmp/otra.db", Env: "/tmp/entorno.db",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if paths.DB != "/tmp/otra.db" {
		t.Errorf("database = %q, want the flag's", paths.DB)
	}
}

func TestTheEnvironmentBeatsTheHome(t *testing.T) {
	home := t.TempDir()
	paths, err := config.Resolve(config.Input{Home: home, Env: "/tmp/entorno.db"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if paths.DB != "/tmp/entorno.db" {
		t.Errorf("database = %q, want the environment's", paths.DB)
	}
}

func TestWithoutAHomeNoPathIsGuessed(t *testing.T) {
	if _, err := config.Resolve(config.Input{}); err == nil {
		t.Fatal("Resolve invented a path with no HOME")
	}
}
