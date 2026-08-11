//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectScenariosRejectsUnknownIDs(t *testing.T) {
	dir := t.TempDir()
	feature := "Feature: example\n\n  Scenario: F01-01 present\n    Given something\n"
	if err := os.WriteFile(filepath.Join(dir, "example.feature"), []byte(feature), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := selectScenarios(dir, []string{"F99-01 "})
	if err == nil || !strings.Contains(err.Error(), "F99-01") {
		t.Fatalf("selectScenarios error = %v, want missing scenario ID", err)
	}
}
