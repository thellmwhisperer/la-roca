package cli

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/lifecycle"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	_ "modernc.org/sqlite"
)

func TestPluginInstallerIsInertBeforeTheExperimentalFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var output, warnings strings.Builder
	env := &cliEnv{out: &output, errOut: &warnings}
	code, err := executeWithEnv(env, []string{"plugin", "install", filepath.Join(home, "absent")}, strings.NewReader("yes\n"))
	if err == nil || code != ExitError || !strings.Contains(err.Error(), "features.plugins") {
		t.Fatalf("disabled install = code %d err %v", code, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".roca", "plugins")); !os.IsNotExist(err) {
		t.Fatalf("disabled installer touched the plugin directory: %v", err)
	}
}

// A plugin package is code, and a purge that leaves code behind on a machine La
// Roca was removed from is the residue this command exists to prevent. What the
// installer never wrote stays, like any other path the operator put there.
func TestThePurgeOwnsInstalledPluginPackagesAndNothingBesideThem(t *testing.T) {
	home := t.TempDir()
	paths := resolvedIn(t, home)
	directory, executable := installedPluginFixture(t, paths, "synthetic")
	theirs := filepath.Join(pluginRoot(paths), "handmade", "notes.txt")
	writeFile(t, theirs, "mine")

	purgeOwnedPaths(t, paths)
	for _, path := range []string{directory, executable} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("the purge kept plugin code at %s: %v", path, err)
		}
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Fatalf("the purge deleted a directory the installer never wrote: %v", err)
	}
}

func TestThePurgeOwnsExecutableOnlyStateDeclaredByTheManifest(t *testing.T) {
	home := t.TempDir()
	paths := resolvedIn(t, home)
	directory := filepath.Join(pluginRoot(paths), "synthetic-exec")
	executableFile := "roca-synthetic-exec"
	executable := filepath.Join(paths.Home, ".local", "bin", executableFile)
	payload := "#!/bin/sh\nexit 0\n"
	writeFile(t, filepath.Join(directory, plugininstall.PackageFilename),
		`{"schema":1,"name":"synthetic-exec","version":"1.0.0","kind":"executable","state_directory":"state"}`)
	writeFile(t, filepath.Join(directory, executableFile), payload)
	writeFile(t, executable, payload)
	writeFile(t, filepath.Join(directory, plugininstall.ChecksumsFilename), "listed in the manifest")
	state := filepath.Join(directory, "state", "nested", "index.db")
	writeFile(t, state, "derived index")
	digest := sha256.Sum256([]byte(payload))
	manifest, err := json.Marshal(plugininstall.Manifest{
		Schema: 1, Name: "synthetic-exec", Source: "owner/synthetic-exec", Version: "1.0.0",
		Checksum: strings.Repeat("c", 64), Kind: plugininstall.ExecutablePackage,
		Risk: plugininstall.Executable, Executable: executable, ExecutableFile: executableFile,
		StateDir: "state", Files: map[string]string{
			plugininstall.PackageFilename: strings.Repeat("a", 64),
			executableFile:                hex.EncodeToString(digest[:]),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(directory, plugininstall.ManifestFilename), string(manifest))

	report := lifecycle.Plan{Owned: ownedPaths(paths), DataDir: dirOf(paths.DB)}.Apply()
	if !report.Purged {
		t.Fatalf("purge = %+v", report)
	}
	for _, path := range []string{state, directory, executable} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("manifest-owned path remains at %s: %v", path, err)
		}
	}
}

// The archives exist because a plugin uninstall refused to delete custodial data
// as a plain folder. The flag the operator passed for La Roca's own artefacts is
// not the answer to that question, so this data leaves only on its own yes.
func TestArchivedPluginDataLeavesOnlyOnItsOwnYes(t *testing.T) {
	for _, test := range []struct {
		name    string
		answer  string
		json    bool
		deleted bool
	}{
		{name: "consented", answer: "y\n", deleted: true},
		{name: "declined", answer: "\n"},
		{name: "nobody at the terminal", answer: ""},
		{name: "json never prompts", answer: "y\n", json: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			paths := resolvedIn(t, home)
			archive := filepath.Join(custodyRoot(paths), "custodial-20260813T210500Z")
			writeFile(t, filepath.Join(archive, "custodial.sqlite"), "user data")

			var narration strings.Builder
			env := &cliEnv{out: &narration, errOut: &narration, json: test.json}
			owned, kept := env.consentToCustody(strings.NewReader(test.answer), paths)
			dataDir := dirOf(paths.DB)
			report := applyPurge(dataDir, func() lifecycle.Plan {
				return lifecycle.Plan{Owned: append(ownedPaths(paths), owned...), DataDir: dataDir}
			})
			requireSuccessfulPurge(t, report)

			_, err := os.Stat(archive)
			if test.deleted != os.IsNotExist(err) {
				t.Fatalf("archive still there = %t, want %t: %v", !os.IsNotExist(err), !test.deleted, err)
			}
			if test.deleted {
				return
			}
			if !slices.ContainsFunc(kept, func(survivor lifecycle.Kept) bool {
				return survivor.Path == archive && strings.Contains(survivor.Reason, "9 bytes")
			}) {
				t.Fatalf("the surviving archive was not named with its weight: %+v", kept)
			}
			if !test.json && !strings.Contains(narration.String(), archive+" (9 bytes)") {
				t.Fatalf("the question did not enumerate the archive:\n%s", narration.String())
			}
			if slices.ContainsFunc(exceptCustodyTree(report.Kept, custodyRoot(paths)),
				func(survivor lifecycle.Kept) bool {
					return strings.HasPrefix(survivor.Path, custodyRoot(paths))
				}) {
				t.Fatalf("the walk repeated the archive as somebody else's: %+v", report.Kept)
			}
		})
	}
}

// installedPluginFixture writes what the installer leaves on disk: the payload
// files, the executable it placed on PATH, and the generated manifest that
// declares both with their checksums.
func installedPluginFixture(t *testing.T, paths config.Paths, name string) (string, string) {
	t.Helper()
	directory := filepath.Join(pluginRoot(paths), name)
	executableFile := "roca-" + name
	executable := filepath.Join(paths.Home, ".local", "bin", executableFile)
	database := name + ".sqlite"
	files := map[string]string{}
	for file, body := range map[string]string{
		plugininstall.PackageFilename: `{"schema":1,"name":"` + name + `","version":"1.0.0"}`,
		"semantic.yaml":               "version: 1\n",
		database:                      "synthetic database bytes",
		executableFile:                "#!/bin/sh\nexit 0\n",
	} {
		writeFile(t, filepath.Join(directory, file), body)
		digest := sha256.Sum256([]byte(body))
		files[file] = hex.EncodeToString(digest[:])
		if file == executableFile {
			writeFile(t, executable, body)
		}
	}
	writeFile(t, filepath.Join(directory, plugininstall.ChecksumsFilename), "listed in the manifest")
	manifest, err := json.Marshal(plugininstall.Manifest{
		Schema: 1, Name: name, Source: "owner/" + name, Version: "1.0.0",
		Checksum: strings.Repeat("c", 64), Risk: plugininstall.Executable,
		Database: database, Executable: executable,
		ExecutableFile: executableFile, Files: files,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(directory, plugininstall.ManifestFilename), string(manifest))
	return directory, executable
}

func TestUninstallConsentDescribesTheInstalledPackageItWouldRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	paths := resolvedIn(t, home)
	installedPluginFixture(t, paths, "synthetic")
	writeFile(t, paths.Config, "[features]\nplugins = true\n")

	var output, narration strings.Builder
	env := &cliEnv{out: &output, errOut: &narration}
	code, err := executeWithEnv(env, []string{"plugin", "uninstall", "synthetic"}, strings.NewReader("no\n"))
	if err != nil || code != ExitOK {
		t.Fatalf("declined uninstall = code %d err %v", code, err)
	}
	if !strings.Contains(narration.String(),
		"EXECUTABLE: FULL TRUST; it runs code with your user privileges") {
		t.Fatalf("uninstall consent misreads the installed package:\n%s", narration.String())
	}
}

func TestPluginConsentDistinguishesDataFromCodeAndNamesTheReplacedChecksum(t *testing.T) {
	checksum, recorded := strings.Repeat("a", 64), strings.Repeat("b", 64)
	tests := []struct {
		action     string
		risk       plugininstall.Risk
		executable string
		trusted    string
		want       []string
	}{
		{"install", plugininstall.DataOnly, "", "",
			[]string{"DATA-ONLY: near-harmless; its worst case is lying content", "sha256:" + checksum}},
		{"install", plugininstall.Executable, "roca-synthetic", "",
			[]string{"EXECUTABLE: FULL TRUST; it runs code with your user privileges"}},
		{"install", plugininstall.Executable, "", "",
			[]string{"EXECUTABLE: FULL TRUST; its cron rides run commands with your user privileges"}},
		{"update", plugininstall.DataOnly, "", recorded,
			[]string{"sha256:" + checksum, "replaces the recorded sha256:" + recorded}},
		{"update", plugininstall.DataOnly, "", checksum,
			[]string{"unchanged since the recorded install"}},
	}
	for _, test := range tests {
		candidate := plugininstall.Candidate{
			Name: "synthetic", Version: "1.2.3", Source: "owner/synthetic",
			Checksum: checksum, Risk: test.risk, Executable: test.executable,
		}
		text := pluginConsentText(test.action, candidate, test.trusted)
		wanted := append([]string{"source: owner/synthetic", "version: 1.2.3"}, test.want...)
		for _, want := range wanted {
			if !strings.Contains(text, want) {
				t.Errorf("%s %s consent lacks %q:\n%s", test.action, test.risk, want, text)
			}
		}
	}
}

func TestPluginUpdateAcceptsAnExplicitSourceToRebase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	paths := resolvedIn(t, home)
	writeFile(t, paths.Config, "[features]\nplugins = true\n")
	first := writeCLIPluginPackage(t, filepath.Join(t.TempDir(), "first"), "synthetic-release", "1.0.0")
	second := writeCLIPluginPackage(t, filepath.Join(t.TempDir(), "second"), "synthetic-release", "2.0.0")

	var output strings.Builder
	env := &cliEnv{out: &output, errOut: &output}
	code, err := executeWithEnv(env, []string{"plugin", "--yes", "install", first}, strings.NewReader(""))
	if err != nil || code != ExitOK {
		t.Fatalf("install = code %d err %v output %q", code, err, output.String())
	}

	output.Reset()
	code, err = executeWithEnv(env,
		[]string{"plugin", "--yes", "update", "synthetic-release", second}, strings.NewReader(""))
	if err != nil || code != ExitOK {
		t.Fatalf("update with source = code %d err %v output %q", code, err, output.String())
	}
	manifest, err := plugininstall.ReadManifest(filepath.Join(pluginRoot(paths), "synthetic-release"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "2.0.0" {
		t.Fatalf("updated version = %q", manifest.Version)
	}
	if manifest.Source != second {
		t.Fatalf("rebased source = %q, want %q", manifest.Source, second)
	}
}

func TestPluginInstallOverAnExistingPluginNamesTheUpdateWithThatSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	paths := resolvedIn(t, home)
	writeFile(t, paths.Config, "[features]\nplugins = true\n")
	first := writeCLIPluginPackage(t, filepath.Join(t.TempDir(), "first"), "synthetic-release", "1.0.0")
	newer := writeCLIPluginPackage(t, filepath.Join(t.TempDir(), "newer"), "synthetic-release", "2.0.0")

	var output strings.Builder
	env := &cliEnv{out: &output, errOut: &output}
	code, err := executeWithEnv(env, []string{"plugin", "--yes", "install", first}, strings.NewReader(""))
	if err != nil || code != ExitOK {
		t.Fatalf("install = code %d err %v output %q", code, err, output.String())
	}

	code, err = executeWithEnv(env, []string{"plugin", "--yes", "install", newer}, strings.NewReader(""))
	if err == nil || code == ExitOK {
		t.Fatalf("second install succeeded: code %d err %v", code, err)
	}
	want := "run `roca plugin update synthetic-release " + newer + "`"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("already-installed error = %v, want %q", err, want)
	}
}

func writeCLIPluginPackage(t *testing.T, directory, name, version string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(directory, plugininstall.PackageFilename), fmt.Sprintf(`{
  "schema": 1,
  "name": %q,
  "version": %q,
  "binary": "roca",
  "databases": [{
    "name": "records",
    "path": "records.db",
    "alias": "synthetic_records",
    "attachment": "resident",
    "custody": true,
    "retention": "Keep operator records until the plugin is uninstalled."
  }],
  "semantic": {"databases": [{
    "database": "records",
    "description": "Synthetic custodial records.",
    "questions": ["Which synthetic records exist?"],
    "tables": [{"name": "entries", "description": "One synthetic row.", "columns": ["id", "value"]}]
  }]},
  "verbs": [],
  "capabilities": []
}`, name, version))
	db, err := sql.Open("sqlite", filepath.Join(directory, "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE entries (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var checksums strings.Builder
	for _, file := range []string{plugininstall.PackageFilename, "records.db"} {
		body, err := os.ReadFile(filepath.Join(directory, file))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		fmt.Fprintf(&checksums, "%x  %s\n", digest, file)
	}
	writeFile(t, filepath.Join(directory, plugininstall.ChecksumsFilename), checksums.String())
	return directory
}
