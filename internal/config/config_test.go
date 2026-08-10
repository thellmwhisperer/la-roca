package config_test

import (
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/config"
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

func TestTheBenchesHangOffTheDataDirectory(t *testing.T) {
	home := t.TempDir()
	paths, err := config.Resolve(config.Input{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".roca", "bench"); paths.Bench != want {
		t.Fatalf("bench = %q, want %q", paths.Bench, want)
	}
	if filepath.Dir(paths.Bench) != filepath.Dir(paths.DB) {
		t.Fatalf("the bench (%s) does not live beside the database (%s)", paths.Bench, paths.DB)
	}
}
