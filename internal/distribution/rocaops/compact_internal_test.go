package rocaops

import (
	"strings"
	"testing"
)

func TestMemoryFTSTriggerSQLMatchesSchema(t *testing.T) {
	got := memoryFTSTriggerSQL(schema)
	if len(got) != 3 {
		t.Fatalf("trigger statements = %d, want 3", len(got))
	}
	for _, statement := range got {
		if !strings.Contains(schema, statement) {
			t.Fatalf("trigger is not taken from schema.sql:\n%s", statement)
		}
	}
}
