package release

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func TestReleaseLaneArchivePreservesActualArtifactSetAndModes(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "output")
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	lanes := [][]string{
		{releaseArtifactSet[0], releaseArtifactSet[4]},
		{releaseArtifactSet[1], releaseArtifactSet[5]},
		{releaseArtifactSet[2], releaseArtifactSet[3], releaseArtifactSet[6], releaseArtifactSet[7]},
	}
	for lane, artifacts := range lanes {
		input := filepath.Join(root, fmt.Sprintf("input-%d", lane))
		if err := os.MkdirAll(input, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range artifacts {
			mode := os.FileMode(0o644)
			if !strings.HasSuffix(name, ".tar.gz") {
				mode = 0o755
			}
			if err := os.WriteFile(filepath.Join(input, name), []byte(name), mode); err != nil {
				t.Fatal(err)
			}
		}
		archive := filepath.Join(root, fmt.Sprintf("release-%d.tar.gz", lane))
		command := exec.Command(filepath.Join("..", "..", "..", "scripts", "archive-release-lane.sh"),
			input, archive)
		if packed, err := command.CombinedOutput(); err != nil {
			t.Fatalf("package release lane: %v\n%s", err, packed)
		}
		command = exec.Command("tar", "-C", output, "-xzf", archive)
		if extracted, err := command.CombinedOutput(); err != nil {
			t.Fatalf("extract release lane: %v\n%s", err, extracted)
		}
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	slices.Sort(got)
	wantSorted := slices.Clone(releaseArtifactSet)
	slices.Sort(wantSorted)
	if !slices.Equal(got, wantSorted) {
		t.Fatalf("extracted artifacts = %v, want %v", got, wantSorted)
	}
	for _, artifact := range releaseArtifactSet {
		if strings.HasSuffix(artifact, ".tar.gz") {
			continue
		}
		info, err := os.Stat(filepath.Join(output, artifact))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode = %o, want 755", artifact, info.Mode().Perm())
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
	archiveUpload := false
	for _, step := range native.Steps {
		if step.Uses == "actions/upload-artifact@v4" &&
			step.With["path"] == "release-${{ matrix.target }}.tar.gz" {
			archiveUpload = true
		}
	}
	if !archiveUpload {
		t.Fatal("native lanes do not upload mode-preserving release archives")
	}
	publish, ok := workflow.Jobs["publish"]
	if !ok {
		t.Fatal("release workflow has no publish job")
	}
	aggregated := false
	for _, step := range publish.Steps {
		if step.Uses == "actions/download-artifact@v4" && step.With["pattern"] == "release-*" &&
			step.With["merge-multiple"] == true && step.With["path"] == ".tmp/release-lanes" {
			aggregated = true
		}
	}
	if !aggregated {
		t.Fatal("publish does not aggregate every native artifact lane")
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
