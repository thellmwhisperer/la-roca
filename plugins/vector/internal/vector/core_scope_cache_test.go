package vector

import (
	"context"
	"encoding/json"
	"testing"
)

func TestResolveDatabaseScopeRefreshesRepeatedAnswers(t *testing.T) {
	calls := 0
	core := CoreCLI{
		Executable: "/synthetic/roca",
		Run: func(context.Context, string, ...string) ([]byte, error) {
			calls++
			name := "ops"
			if calls > 1 {
				name = "newdb"
			}
			return json.Marshal(DatabaseScope{Databases: []string{name},
				Selected: []DatabaseSelection{{Source: "plugin:roca-ops", Database: name}}})
		},
	}
	first, err := core.ResolveDatabaseScope(context.Background(), "ops")
	if err != nil {
		t.Fatal(err)
	}
	second, err := core.ResolveDatabaseScope(context.Background(), "ops")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("scope subprocess calls = %d, want 2", calls)
	}
	if len(first.Selected) != 1 || first.Selected[0].Database != "ops" || len(second.Selected) != 1 || second.Selected[0].Database != "newdb" {
		t.Fatalf("refreshed scope = %+v / %+v", first, second)
	}
}
