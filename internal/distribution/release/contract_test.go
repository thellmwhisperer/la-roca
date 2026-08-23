package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var releaseArtifactSet = []string{
	"roca-v-test-darwin-arm64",
	"roca-v-test-linux-arm64",
	"roca-v-test-linux-x64",
	"roca-v-test-windows-x64.exe",
	"roca-vector-v-test-darwin-arm64.tar.gz",
	"roca-vector-v-test-linux-arm64.tar.gz",
	"roca-vector-v-test-linux-x64.tar.gz",
	"roca-vector-v-test-windows-x64.tar.gz",
}

type releaseWorkflow struct {
	Jobs map[string]struct {
		Needs    any `yaml:"needs"`
		RunsOn   any `yaml:"runs-on"`
		Strategy struct {
			Matrix struct {
				Include []map[string]any `yaml:"include"`
			} `yaml:"matrix"`
		} `yaml:"strategy"`
		Steps []struct {
			Name string         `yaml:"name"`
			Uses string         `yaml:"uses"`
			Run  string         `yaml:"run"`
			With map[string]any `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func TestNativeReleaseLanesProduceThePublishedArtifactSet(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	targets := []struct {
		name, goos, arch string
	}{
		{"darwin-arm64", "darwin", "arm64"},
		{"linux-amd64", "linux", "amd64"},
		{"linux-arm64", "linux", "arm64"},
		{"windows-amd64", "linux", "amd64"},
	}
	seen := map[string]bool{}
	artifactPattern := regexp.MustCompile(`(?:bin|DIST\))/((?:roca|roca-vector)-v-test-[A-Za-z0-9.-]+)`)
	for _, target := range targets {
		command := exec.Command("make", "-n", target.name, "VERSION=v-test",
			"HOST_OS="+target.goos, "HOST_ARCH="+target.arch)
		command.Dir = root
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("make -n %s: %v\n%s", target.name, err, output)
		}
		for _, match := range artifactPattern.FindAllStringSubmatch(string(output), -1) {
			seen[match[1]] = true
		}
	}
	for _, artifact := range releaseArtifactSet {
		if !seen[artifact] {
			t.Errorf("native make targets did not produce %s; got %v", artifact, sortedKeys(seen))
		}
	}
}

func TestReleaseWorkflowBuildsNativelyAndAggregatesBeforePublishing(t *testing.T) {
	body := readRepoFile(t, "../../../.github/workflows/release.yml")
	var workflow releaseWorkflow
	if err := yaml.Unmarshal([]byte(body), &workflow); err != nil {
		t.Fatalf("release workflow is not valid YAML: %v", err)
	}
	native, ok := workflow.Jobs["native-artifacts"]
	if !ok {
		t.Fatal("release workflow has no native-artifacts job")
	}
	wantTargets := []string{"darwin-arm64", "linux-amd64", "linux-arm64"}
	var targets []string
	darwinRunner := ""
	for _, lane := range native.Strategy.Matrix.Include {
		target, _ := lane["target"].(string)
		runner, _ := lane["runner"].(string)
		targets = append(targets, target)
		if target == "darwin-arm64" {
			darwinRunner = runner
		}
	}
	slices.Sort(targets)
	slices.Sort(wantTargets)
	if !slices.Equal(targets, wantTargets) {
		t.Fatalf("native release targets = %v, want %v", targets, wantTargets)
	}
	if !strings.HasPrefix(darwinRunner, "macos-") {
		t.Fatalf("darwin artifact runner = %q, want native macOS", darwinRunner)
	}
	publish, ok := workflow.Jobs["publish"]
	if !ok {
		t.Fatal("release workflow has no publish job")
	}
	aggregated := false
	restoredModes := false
	for _, step := range publish.Steps {
		if step.Uses == "actions/download-artifact@v4" && step.With["pattern"] == "release-*" &&
			step.With["merge-multiple"] == true {
			aggregated = true
		}
		if step.Name == "Restore executable permissions" && strings.Contains(step.Run, "chmod 0755") {
			restoredModes = true
		}
	}
	if !aggregated {
		t.Fatal("publish does not aggregate every native artifact lane")
	}
	if !restoredModes {
		t.Fatal("publish does not restore executable modes after artifact download")
	}
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
