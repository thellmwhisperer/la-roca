package vector

import (
	"context"
	"encoding/json"
	"testing"
)

func TestResolveDatabaseScopeCachesRepeatedAnswers(t *testing.T) {
	calls := 0
	core := CoreCLI{
		Executable: "/synthetic/roca",
		Run: func(context.Context, string, ...string) ([]byte, error) {
			calls++
			return json.Marshal(DatabaseScope{Databases: []string{"ops"},
				Selected: []DatabaseSelection{{Source: "plugin:roca-ops", Database: "ops"}}})
		},
	}
	core.SetScopeCache(NewScopeCache())
	first, err := core.ResolveDatabaseScope(context.Background(), "ops")
	if err != nil {
		t.Fatal(err)
	}
	second, err := core.ResolveDatabaseScope(context.Background(), "ops")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("scope subprocess calls = %d, want 1", calls)
	}
	if len(first.Selected) != 1 || first.Selected[0].Database != second.Selected[0].Database {
		t.Fatalf("cached scope = %+v / %+v", first, second)
	}
}
