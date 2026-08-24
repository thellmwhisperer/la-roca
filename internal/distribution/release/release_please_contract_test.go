package release

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestReleasePleaseRunsOnlyFromTrustedMainWithLeastPrivilege(t *testing.T) {
	workflow := readRepoFile(t, "../../../.github/workflows/release-please.yml")
	for _, required := range []string{
		"branches: [main]",
		"contents: read",
		"googleapis/release-please-action@5c625bfb5d1ff62eadeeb3772007f7f66fdcf071",
		"secrets.RELEASE_PLEASE_TOKEN",
		"steps.token.outputs.present == 'true'",
		`echo "present=true" >> "$GITHUB_OUTPUT"`,
		`echo "present=false" >> "$GITHUB_OUTPUT"`,
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release-please workflow is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"workflow_dispatch",
		"pull_request_target",
		"actions/checkout",
		"contents: write",
		"issues: write",
		"pull-requests: write",
		"make dist",
		"go build",
		"gh release",
		// Step-level secret comparison still invoked the action with an empty
		// token; the gate must be the env check + output, not this form.
		"if: ${{ secrets.RELEASE_PLEASE_TOKEN != '' }}",
		"if: secrets.RELEASE_PLEASE_TOKEN != ''",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("release-please workflow exposes or duplicates privileged work with %q", forbidden)
		}
	}

	channel := parseReleaseWorkflow(t)
	if want := []string{"v*", "models-v*"}; !slices.Equal(channel.On.Push.Tags, want) {
		t.Fatalf("artefact channel tags = %v, want %v", channel.On.Push.Tags, want)
	}
}

func TestReleasePleaseOwnsOneStableVersion(t *testing.T) {
	var config struct {
		ReleaseType string `json:"release-type"`
		Packages    map[string]struct {
			PackageName string `json:"package-name"`
			ReleaseAs   string `json:"release-as"`
			ExtraFiles  []struct {
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
	if config.ReleaseType != "go" || root.PackageName != "roca" || root.ReleaseAs != "" {
		t.Fatalf("release config = type %q, package %q, release-as %q; want go, roca, no pinned release",
			config.ReleaseType, root.PackageName, root.ReleaseAs)
	}

	var manifest map[string]string
	if err := json.Unmarshal([]byte(readRepoFile(t, "../../../.release-please-manifest.json")), &manifest); err != nil {
		t.Fatalf("release-please manifest is not valid JSON: %v", err)
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(manifest["."]) {
		t.Fatalf("manifest baseline = %q, want a stable semver owned by release-please", manifest["."])
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

	docs := readRepoFile(t, "../../../docs/releases.md")
	for _, required := range []string{"feat:", "BREAKING CHANGE:", "RELEASE_PLEASE_TOKEN", "plugin.json"} {
		if !strings.Contains(docs, required) {
			t.Errorf("release documentation is missing %q", required)
		}
	}
	if strings.Contains(docs, "release-as") {
		t.Error("release documentation still instructs maintainers to pin a release")
	}
}
