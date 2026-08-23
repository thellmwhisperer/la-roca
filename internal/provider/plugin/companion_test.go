package plugin_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

func TestCompanionDeclarationIsOptionalAndVersionedOnSchemaOne(t *testing.T) {
	base := companionManifest("synthetic", "", nil)
	manifest, err := plugin.DecodeManifest(strings.NewReader(base))
	if err != nil {
		t.Fatalf("schema 1 without companion: %v", err)
	}
	if manifest.Companion != nil {
		t.Fatalf("absent companion decoded as %+v", manifest.Companion)
	}

	withCompanion := companionManifest("synthetic", "roca-synthetic", []string{"watch", "--poll"})
	manifest, err = plugin.DecodeManifest(strings.NewReader(withCompanion))
	if err != nil {
		t.Fatalf("schema 1 with companion: %v", err)
	}
	if manifest.Companion == nil || manifest.Companion.Executable != "roca-synthetic" ||
		!slices.Equal(manifest.Companion.Args, []string{"watch", "--poll"}) {
		t.Fatalf("companion = %+v", manifest.Companion)
	}
}

func TestCompanionDeclarationRejectsUnsafeExecutablesAndEmptyArgs(t *testing.T) {
	tests := []struct {
		name string
		edit func(string) string
		want string
	}{
		{"unknown companion field", func(raw string) string {
			return strings.Replace(raw, `"executable": "roca-synthetic"`,
				`"executable": "roca-synthetic", "mystery": true`, 1)
		}, "unknown field"},
		{"path executable", func(raw string) string {
			return strings.Replace(raw, `"executable": "roca-synthetic"`, `"executable": "../roca-synthetic"`, 1)
		}, "invalid companion executable"},
		{"absolute executable", func(raw string) string {
			return strings.Replace(raw, `"executable": "roca-synthetic"`, `"executable": "/bin/sh"`, 1)
		}, "invalid companion executable"},
		{"empty executable", func(raw string) string {
			return strings.Replace(raw, `"executable": "roca-synthetic"`, `"executable": ""`, 1)
		}, "invalid companion executable"},
		{"empty arg", func(raw string) string {
			return strings.Replace(raw, `"args": ["watch"]`, `"args": ["watch", ""]`, 1)
		}, "empty companion argument"},
	}
	valid := companionManifest("synthetic", "roca-synthetic", []string{"watch"})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := plugin.DecodeManifest(strings.NewReader(test.edit(valid)))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSessionCompanionsReadInstalledPluginDeclarationsOnly(t *testing.T) {
	root := t.TempDir()
	writeManifestPackage(t, root, "mirror", companionManifest("mirror", "roca-mirror", []string{"watch"}))
	writeManifestPackage(t, root, "quiet", companionManifest("quiet", "", nil))
	executable := filepath.Join(root, "worker")
	if err := os.Mkdir(executable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(executable, plugin.PackageFilename), []byte(`{
  "schema": 1,
  "name": "worker",
  "version": "1.0.0",
  "kind": "executable",
  "companion": {"executable": "roca-worker", "args": ["serve"]}
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	found, warnings := plugin.SessionCompanions(root)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	names := make([]string, 0, len(found))
	byName := map[string]plugin.SessionCompanion{}
	for _, spec := range found {
		names = append(names, spec.Plugin)
		byName[spec.Plugin] = spec
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"mirror", "worker"}) {
		t.Fatalf("companions = %v, want mirror and worker", names)
	}
	if byName["mirror"].Executable != "roca-mirror" ||
		!slices.Equal(byName["mirror"].Args, []string{"watch"}) {
		t.Fatalf("mirror companion = %+v", byName["mirror"])
	}
	if byName["worker"].Executable != "roca-worker" ||
		!slices.Equal(byName["worker"].Args, []string{"serve"}) {
		t.Fatalf("worker companion = %+v", byName["worker"])
	}
}

func companionManifest(name, executable string, args []string) string {
	companion := ""
	if executable != "" {
		encodedArgs := "[]"
		if len(args) > 0 {
			quoted := make([]string, len(args))
			for i, arg := range args {
				quoted[i] = `"` + arg + `"`
			}
			encodedArgs = "[" + strings.Join(quoted, ", ") + "]"
		}
		companion = `,
  "companion": {"executable": "` + executable + `", "args": ` + encodedArgs + `}`
	}
	return manifestFixture(`{
  "schema": 1,
  "name": "` + name + `",
  "version": "1.0.0",
  "binary": "roca-` + name + `",
  "databases": [{
    "name": "records",
    "path": "records.db",
    "alias": "plugin_synthetic_records",
    "attachment": "resident",
    "retention": "The plugin retains every synthetic record."
  }],
  "semantic": {"databases": [{
    "database": "records",
    "description": "Synthetic records.",
    "questions": ["Which synthetic records exist?"],
    "tables": [{"name": "records", "description": "One synthetic record.", "columns": ["id", "value"]}]
  }]},
  "verbs": [],
  "capabilities": []
` + companion + `
}`)
}
