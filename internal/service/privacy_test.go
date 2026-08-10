package service_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/service"
)

// The decision of 2026-08-05 ~21:55: agent-facing JSON surfaces must
// never carry the database file path. The CLI text output may still show it to
// the operator, but the structured output a script or an MCP agent reads must
// not.

const fakeDBPath = "/home/operator/.roca/roca.db"

func TestAgentSurfaceJSONDoesNotCarryDatabasePath(t *testing.T) {
	tests := []struct {
		name string
		v    any
	}{
		{
			"InitResult",
			service.InitResult{
				DBPath:     fakeDBPath,
				ConfigPath: "/home/operator/.roca/config.toml",
				BackupPath: "/home/operator/.roca/backups/roca-backup.db",
				Database:   "created", Verdict: "current", Layers: 12,
				Model: &service.InitModel{Ready: true, Provider: "ollama"},
			},
		},
		{
			"DoctorReport",
			service.DoctorReport{
				Version: "1.0.0", DBPath: fakeDBPath,
				ConfigPath: "/home/operator/.roca/config.toml", Memories: 100,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(tt.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			body := string(encoded)
			if strings.Contains(body, fakeDBPath) {
				t.Errorf("the JSON carries the database path %q: %s", fakeDBPath, body)
			}
			if strings.Contains(body, `"db_path"`) {
				t.Errorf("the JSON carries a db_path key: %s", body)
			}
		})
	}
}
