//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogRejectsFeatureOutsideADomain(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "numbered.feature"), []byte("Feature: misplaced\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := catalogFeaturePaths(root)
	if err == nil || !strings.Contains(err.Error(), "features/<domain>/*.feature") {
		t.Fatalf("catalogFeaturePaths error = %v, want domain layout error", err)
	}
}
