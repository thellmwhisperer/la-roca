package release

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type releasePleaseWorkflow struct {
	Name        string                       `yaml:"name"`
	On          map[string]releasePleasePush `yaml:"on"`
	Permissions map[string]string            `yaml:"permissions"`
	Jobs        map[string]releasePleaseJob  `yaml:"jobs"`
}

type releasePleasePush struct {
	Branches []string `yaml:"branches"`
}

type releasePleaseJob struct {
	RunsOn string              `yaml:"runs-on"`
	Steps  []releasePleaseStep `yaml:"steps"`
}

type releasePleaseStep struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With map[string]string `yaml:"with"`
}

func TestReleasePleaseReplicatesTheTrustedMainControlPlane(t *testing.T) {
	workflow := parseReleasePleaseWorkflow(t)
	if workflow.Name != "release-please" {
		t.Fatalf("workflow name = %q, want release-please", workflow.Name)
	}
	if len(workflow.On) != 1 || !slices.Equal(workflow.On["push"].Branches, []string{"main"}) {
		t.Fatalf("workflow triggers = %#v, want only pushes to main", workflow.On)
	}
	wantPermissions := map[string]string{"contents": "write", "pull-requests": "write"}
	if len(workflow.Permissions) != len(wantPermissions) {
		t.Fatalf("workflow permissions = %#v, want %#v", workflow.Permissions, wantPermissions)
	}
	for permission, want := range wantPermissions {
		if workflow.Permissions[permission] != want {
			t.Fatalf("workflow permission %s = %q, want %q", permission, workflow.Permissions[permission], want)
		}
	}
	if len(workflow.Jobs) != 1 {
		t.Fatalf("workflow jobs = %#v, want one release-please job", workflow.Jobs)
	}
	job, ok := workflow.Jobs["release-please"]
	if !ok || job.RunsOn != "ubuntu-latest" || len(job.Steps) != 3 {
		t.Fatalf("release-please job = %#v", job)
	}

	validation, action, automerge := job.Steps[0], job.Steps[1], job.Steps[2]
	if validation.Name != "Validate RELEASE_PLEASE_TOKEN is set" ||
		validation.Env["TOKEN"] != "${{ secrets.RELEASE_PLEASE_TOKEN }}" {
		t.Fatalf("token validation step = %#v", validation)
	}
	if action.Uses != "googleapis/release-please-action@8b8fd2cc23b2e18957157a9d923d75aa0c6f6ad5" ||
		action.ID != "release" || action.With["token"] != "${{ secrets.RELEASE_PLEASE_TOKEN }}" {
		t.Fatalf("release-please action step = %#v", action)
	}
	if automerge.Name != "Enable auto-merge for release PR" ||
		automerge.If != "${{ steps.release.outputs.pr }}" ||
		automerge.Env["GH_TOKEN"] != "${{ secrets.RELEASE_PLEASE_TOKEN }}" ||
		automerge.Env["GH_REPO"] != "${{ github.repository }}" ||
		automerge.Env["RELEASE_PR"] != "${{ steps.release.outputs.pr }}" {
		t.Fatalf("release PR auto-merge step = %#v", automerge)
	}

	channel := parseReleaseWorkflow(t)
	if want := []string{"v*", "models-v*"}; !slices.Equal(channel.On.Push.Tags, want) {
		t.Fatalf("artefact channel tags = %v, want %v", channel.On.Push.Tags, want)
	}
}

func TestReleasePleaseTokenValidationFailsClosed(t *testing.T) {
	validation := parseReleasePleaseWorkflow(t).Jobs["release-please"].Steps[0]
	missing := exec.Command("bash", "-eu", "-o", "pipefail", "-c", validation.Run)
	missing.Env = append(os.Environ(), "TOKEN=")
	output, err := missing.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "RELEASE_PLEASE_TOKEN secret is not set") {
		t.Fatalf("missing token result = %v\n%s", err, output)
	}

	present := exec.Command("bash", "-eu", "-o", "pipefail", "-c", validation.Run)
	present.Env = append(os.Environ(), "TOKEN=present")
	if output, err := present.CombinedOutput(); err != nil {
		t.Fatalf("present token failed: %v\n%s", err, output)
	}
}

func TestReleasePleaseArmsAutoMergeForTheReturnedPR(t *testing.T) {
	automerge := parseReleasePleaseWorkflow(t).Jobs["release-please"].Steps[2]
	tools := t.TempDir()
	logPath := filepath.Join(tools, "gh.log")
	writeExecutable(t, filepath.Join(tools, "jq"), "#!/bin/sh\nprintf '%s\\n' 73\n")
	writeExecutable(t, filepath.Join(tools, "gh"), "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$GH_LOG\"\n")

	command := exec.Command("bash", "-eu", "-o", "pipefail", "-c", automerge.Run)
	command.Env = append(os.Environ(),
		"PATH="+tools+":"+os.Getenv("PATH"),
		"GH_LOG="+logPath,
		"GH_TOKEN=token",
		"GH_REPO=thellmwhisperer/la-roca",
		`RELEASE_PR={"number":73}`,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("auto-merge script failed: %v\n%s", err, output)
	}
	invocation := strings.TrimSpace(readRepoFile(t, logPath))
	if invocation != "pr merge --merge --auto 73" {
		t.Fatalf("gh invocation = %q", invocation)
	}
}

func TestReleasePleaseOwnsOneStableVersion(t *testing.T) {
	var config struct {
		BumpMinorPreMajor    bool `json:"bump-minor-pre-major"`
		BumpPatchForMinorPre bool `json:"bump-patch-for-minor-pre-major"`
		Packages             map[string]struct {
			ReleaseType           string `json:"release-type"`
			PackageName           string `json:"package-name"`
			IncludeVInTag         bool   `json:"include-v-in-tag"`
			IncludeComponentInTag bool   `json:"include-component-in-tag"`
			ReleaseAs             string `json:"release-as"`
			ExtraFiles            []struct {
				Type     string `json:"type"`
				Path     string `json:"path"`
				JSONPath string `json:"jsonpath"`
			} `json:"extra-files"`
		} `json:"packages"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "../../../release-please-config.json")), &config); err != nil {
		t.Fatalf("release-please config is not valid JSON: %v", err)
	}
	root, ok := config.Packages["."]
	if !ok {
		t.Fatal("release-please does not declare the repository root as its Go package")
	}
	if !config.BumpMinorPreMajor || !config.BumpPatchForMinorPre || root.ReleaseType != "go" ||
		root.PackageName != "roca" || !root.IncludeVInTag || root.IncludeComponentInTag || root.ReleaseAs != "" {
		t.Fatalf("release config = %#v, want the authoritative root Go package without a pinned release", config)
	}

	var manifest map[string]string
	if err := json.Unmarshal([]byte(readRepoFile(t, "../../../.release-please-manifest.json")), &manifest); err != nil {
		t.Fatalf("release-please manifest is not valid JSON: %v", err)
	}
	if len(manifest) != 1 || !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(manifest["."]) {
		t.Fatalf("manifest baseline = %#v, want one stable root semver owned by release-please", manifest)
	}

	var plugin struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "../../../plugin.json")), &plugin); err != nil {
		t.Fatalf("plugin manifest is not valid JSON: %v", err)
	}
	if plugin.Version != manifest["."] {
		t.Fatalf("plugin version = %q, want manifest version %q", plugin.Version, manifest["."])
	}
	if len(root.ExtraFiles) != 1 || root.ExtraFiles[0].Type != "json" ||
		root.ExtraFiles[0].Path != "plugin.json" || root.ExtraFiles[0].JSONPath != "$.version" {
		t.Fatalf("plugin version is not owned by release-please: extra-files = %#v", root.ExtraFiles)
	}

}

func parseReleasePleaseWorkflow(t *testing.T) releasePleaseWorkflow {
	t.Helper()
	body := readRepoFile(t, "../../../.github/workflows/release-please.yml")
	var workflow releasePleaseWorkflow
	if err := yaml.Unmarshal([]byte(body), &workflow); err != nil {
		t.Fatalf("release-please workflow is not valid YAML: %v", err)
	}
	return workflow
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
