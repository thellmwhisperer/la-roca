//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

// selectScenarios reads the consecrated features and returns a copy with only
// the requested scenarios, keeping header, tags and background.
//
// The files in features/ are versioned contracts; the runner selects the
// implemented subset without editing them.
func selectScenarios(dir string, ids []string) ([]godog.Feature, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.feature"))
	if err != nil {
		return nil, err
	}
	var features []godog.Feature
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		excerpt, scenarios := trim(string(raw), ids)
		if scenarios == 0 {
			continue
		}
		features = append(features, godog.Feature{
			Name:     filepath.Base(path),
			Contents: []byte(excerpt),
		})
	}
	return features, nil
}

// trim keeps the feature's preamble (header and background) and, of the
// scenarios, only the requested ones, with the tags that precede them.
//
// The files in features/ are versioned contracts; the runner selects the
// implemented subset without editing them.
func trim(content string, ids []string) (string, int) {
	lines := strings.Split(content, "\n")

	type scenario struct{ start, heading int }
	var scenarios []scenario
	for i, line := range lines {
		if isScenarioHeading(line) {
			scenarios = append(scenarios, scenario{start: upToItsTags(lines, i), heading: i})
		}
	}
	if len(scenarios) == 0 {
		return "", 0
	}

	chosen := append([]string(nil), lines[:scenarios[0].start]...)
	howMany := 0
	for k, sc := range scenarios {
		end := len(lines)
		if k+1 < len(scenarios) {
			end = scenarios[k+1].start
		}
		if !belongsTo(lines[sc.heading], ids) {
			continue
		}
		chosen = append(chosen, lines[sc.start:end]...)
		howMany++
	}
	if howMany == 0 {
		return "", 0
	}
	return strings.Join(chosen, "\n"), howMany
}

func isScenarioHeading(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "Scenario:") ||
		strings.HasPrefix(trimmed, "Scenario Outline:")
}

// upToItsTags returns the index where a scenario's block starts: its tags
// belong to it, not to the previous scenario.
func upToItsTags(lines []string, heading int) int {
	start := heading
	for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "@") {
		start--
	}
	return start
}

func belongsTo(heading string, ids []string) bool {
	_, title, _ := strings.Cut(heading, ":")
	title = strings.TrimSpace(title)
	for _, id := range ids {
		if strings.HasPrefix(title, id) {
			return true
		}
	}
	return false
}

// rocaBinary locates the built binary. The suite does not compile it: it tests
// exactly the artefact `make build` produces.
func rocaBinary() (string, error) {
	if path := os.Getenv("ROCA_BIN"); path != "" {
		return filepath.Abs(path)
	}
	path, err := filepath.Abs(filepath.Join("..", "..", "bin", "roca"))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%s does not exist: run `make build` first", path)
	}
	return path, nil
}
