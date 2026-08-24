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
	On struct {
		Push struct {
			Tags []string `yaml:"tags"`
		} `yaml:"push"`
	} `yaml:"on"`
	Jobs map[string]struct {
		Needs    any    `yaml:"needs"`
		RunsOn   any    `yaml:"runs-on"`
		If       string `yaml:"if"`
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
	workflow := parseReleaseWorkflow(t)
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

func TestReleaseWorkflowSeparatesBinaryAndModelJobs(t *testing.T) {
	workflow := parseReleaseWorkflow(t)
	models, ok := workflow.Jobs["publish-models"]
	if !ok {
		t.Fatal("release workflow has no publish-models job")
	}
	modelCondition := "${{ startsWith(inputs.tag || github.ref_name, 'models-v') }}"
	if models.If != modelCondition {
		t.Fatalf("model release condition = %q, want %q", models.If, modelCondition)
	}
	binaryCondition := "${{ !startsWith(inputs.tag || github.ref_name, 'models-v') }}"
	for _, name := range []string{"upgrade-homes", "upgrade-gauntlet", "native-artifacts", "publish"} {
		job, ok := workflow.Jobs[name]
		if !ok {
			t.Fatalf("release workflow has no %s job", name)
		}
		if job.If != binaryCondition {
			t.Fatalf("%s condition = %q, want %q", name, job.If, binaryCondition)
		}
	}
}

func TestPublishModelReleaseKeepsBinaryLatestAndPublishesChecksumsLast(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			root := t.TempDir()
			tools := filepath.Join(root, "tools")
			output := filepath.Join(root, "output")
			logPath := filepath.Join(root, "gh.log")
			if err := os.MkdirAll(tools, 0o700); err != nil {
				t.Fatal(err)
			}
			fakeGo := `#!/usr/bin/env bash
set -euo pipefail
out=
while (( $# )); do
  if [[ "$1" = --out ]]; then out=$2; shift 2; else shift; fi
done
mkdir -p "$out"
printf model > "$out/model.gguf"
printf license > "$out/LICENSE-model.txt"
printf checksums > "$out/checksums.txt"
`
			fakeGH := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\t' "$@" >> "$GH_LOG"
printf '\n' >> "$GH_LOG"
if [[ "$1 $2" = "release view" && "$GH_RELEASE_EXISTS" != true ]]; then exit 1; fi
`
			for name, body := range map[string]string{"go": fakeGo, "gh": fakeGH} {
				path := filepath.Join(tools, name)
				if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command(filepath.Join("..", "..", "..", "scripts", "publish-model-release.sh"),
				"models-v1", output)
			command.Env = append(os.Environ(), "PATH="+tools+":"+os.Getenv("PATH"),
				"GH_LOG="+logPath, fmt.Sprintf("GH_RELEASE_EXISTS=%t", existing))
			if result, err := command.CombinedOutput(); err != nil {
				t.Fatalf("publish model release: %v\n%s", err, result)
			}
			commands := readCommandLog(t, logPath)
			if len(commands) != 4 {
				t.Fatalf("gh commands = %#v, want view, create/edit and two uploads", commands)
			}
			if !slices.Equal(commands[0], []string{"release", "view", "models-v1"}) {
				t.Fatalf("first gh command = %v", commands[0])
			}
			latestCommand := commands[1]
			if existing {
				if !slices.Equal(latestCommand, []string{"release", "edit", "models-v1", "--latest=false"}) {
					t.Fatalf("existing release command = %v", latestCommand)
				}
			} else if len(latestCommand) < 4 || !slices.Equal(latestCommand[:3], []string{"release", "create", "models-v1"}) ||
				!slices.Contains(latestCommand, "--latest=false") {
				t.Fatalf("new release command = %v", latestCommand)
			}
			wantAssetUpload := []string{"release", "upload", "models-v1", "--clobber",
				filepath.Join(output, "model.gguf"), filepath.Join(output, "LICENSE-model.txt")}
			if !slices.Equal(commands[2], wantAssetUpload) {
				t.Fatalf("asset upload = %v, want %v", commands[2], wantAssetUpload)
			}
			wantChecksumUpload := []string{"release", "upload", "models-v1", "--clobber",
				filepath.Join(output, "checksums.txt")}
			if !slices.Equal(commands[3], wantChecksumUpload) {
				t.Fatalf("checksum upload = %v, want %v", commands[3], wantChecksumUpload)
			}
		})
	}
}

func readCommandLog(t *testing.T, path string) [][]string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var commands [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		commands = append(commands, strings.Split(strings.TrimSuffix(line, "\t"), "\t"))
	}
	return commands
}

func parseReleaseWorkflow(t *testing.T) releaseWorkflow {
	t.Helper()
	body := readRepoFile(t, "../../../.github/workflows/release.yml")
	var workflow releaseWorkflow
	if err := yaml.Unmarshal([]byte(body), &workflow); err != nil {
		t.Fatalf("release workflow is not valid YAML: %v", err)
	}
	return workflow
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
