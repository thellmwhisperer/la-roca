package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBundledPlacementDoctorAndVectorQueryAcceptAheadOpsSchema(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX vector executable")
	}
	fixture := fixtureInstallation(t)
	writeConfig(t, fixture.home, "[features]\nplugins = true\nvector = true\n")
	opsPath := filepath.Join(fixture.home, ".roca", "plugins", "roca-ops", "roca-ops.db")
	db, err := sql.Open("sqlite", opsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"ALTER TABLE memories ADD COLUMN legacy_id INTEGER",
		"CREATE UNIQUE INDEX idx_memories_legacy_id ON memories(legacy_id) WHERE legacy_id IS NOT NULL",
		"UPDATE plugin_schema SET schema_version = 6, index_version = 2",
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		name string
		args []string
	}{
		{"bundled placement", []string{"_install-bundled-plugins", "--json"}},
		{"doctor", []string{"doctor"}},
		{"vector query", []string{"vector", "query", "ops schema ahead", "5", "--databases", "corpus,ops"}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			output, err := runSchemaAheadCLI(check.args...)
			if err != nil {
				t.Fatalf("%s failed: %v\n%s", check.name, err, output)
			}
			for _, rejected := range []string{"newer than supported", "plugin skipped", "schema-adoption"} {
				if strings.Contains(output, rejected) {
					t.Fatalf("%s reported %q:\n%s", check.name, rejected, output)
				}
			}
			if check.name == "bundled placement" {
				var result struct {
					Installed bool `json:"installed"`
				}
				if err := json.Unmarshal([]byte(output), &result); err != nil || !result.Installed {
					t.Fatalf("bundled placement result = %+v, err %v:\n%s", result, err, output)
				}
			}
		})
	}
}

func runSchemaAheadCLI(args ...string) (string, error) {
	var output strings.Builder
	env := hermeticCLIEnv(&cliEnv{
		build: contractBuild(), out: &output, errOut: &output,
		bundledVectorPayload: []byte("#!/bin/sh\nexit 0\n"),
	})
	code, err := executeWithOptions(env, args, nil, true)
	if err == nil && code != ExitOK {
		err = fmt.Errorf("exit code %d", code)
	}
	return output.String(), err
}
