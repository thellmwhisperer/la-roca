//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cucumber/godog"
)

var acceptanceDomains = map[string]bool{
	"distribution": true,
	"ingest":       true,
	"provider":     true,
	"store":        true,
}

func catalogFeaturePaths(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() || filepath.Ext(path) != ".feature" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 2 || !acceptanceDomains[parts[0]] {
			return fmt.Errorf("feature outside features/<domain>/*.feature: %s", rel)
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no domain features found under %s", root)
	}
	sort.Strings(paths)
	return paths, nil
}

func loadCatalogFeatures(root string) ([]godog.Feature, error) {
	paths, err := catalogFeaturePaths(root)
	if err != nil {
		return nil, err
	}
	features := make([]godog.Feature, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		features = append(features, godog.Feature{Name: rel, Contents: raw})
	}
	return features, nil
}

// rocaBinary locates the built binary. The suite does not compile it: it tests
// exactly the artefact `make build` produces.
func rocaBinary() (string, error) {
	if path := os.Getenv("ROCA_BIN"); path != "" {
		if filepath.IsAbs(path) {
			return filepath.Clean(path), nil
		}
		root, err := acceptanceRoot()
		if err != nil {
			return "", err
		}
		return filepath.Join(root, path), nil
	}
	root, err := acceptanceRoot()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "bin", "roca")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%s does not exist: run `make build` first", path)
	}
	return path, nil
}

func acceptanceRoot() (string, error) {
	return filepath.Abs(filepath.Join("..", ".."))
}

// acceptanceTempDir creates a scratch directory under the project's ignored
// .tmp/, so a suite never writes outside the repository.
func acceptanceTempDir(prefix string) (string, error) {
	projectRoot, err := acceptanceRoot()
	if err != nil {
		return "", err
	}
	root := filepath.Join(projectRoot, ".tmp")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, prefix)
}
