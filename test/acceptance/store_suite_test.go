//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cucumber/godog"
)

// TestStoreDomainSuite runs the per-domain STORE acceptance suite against the
// real binary. It is the first wave of the suite rebirth: every scenario here
// is one the approved list froze by title, and they all run green against the
// artefact `make build` produces.
//
// It is black box, like the consecrated suite: no product symbol is imported,
// only `roca` is run and its output and its database are read. The store domain
// has its own curated step vocabulary (registerStoreSteps) so that a green here
// says what it means over this domain and no other.
func TestStoreDomainSuite(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "features", "store", "*.feature"))
	if err != nil {
		t.Fatalf("find the store features: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no store feature found under features/store")
	}

	features := make([]godog.Feature, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		rel, err := filepath.Rel(filepath.Join("..", ".."), path)
		if err != nil {
			rel = filepath.Base(path)
		}
		features = append(features, godog.Feature{Name: rel, Contents: raw})
	}

	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}

	runGodog(t, features, func(ctx *godog.ScenarioContext) {
		registerStoreSteps(ctx, binary)
	})
}
