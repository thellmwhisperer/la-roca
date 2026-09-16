package mcpplug

import "testing"

func TestVectorQueryPreservesNegativeLimitAlias(t *testing.T) {
	if got := (vectorQueryArgs{Limit: -1}).hitCount(); got != -1 {
		t.Fatalf("negative vector limit became %d", got)
	}
}
