//go:build acceptance

package acceptance

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealBinaryDisposableHomeSmoke walks the operator path against the artefact
// `make build` produces: init, ingest, query, plugin install, and plugin update.
// The HOME is a disposable directory. The live operator home is never written.
func TestRealBinaryDisposableHomeSmoke(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}
	home := disposableSmokeHome(t, "roca-e2e-smoke-")

	operatorHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(home) == filepath.Clean(operatorHome) {
		t.Fatal("the disposable home resolved to the operator home")
	}
	pluginName := "harbor-ledger-" + strings.TrimPrefix(filepath.Base(home), "roca-e2e-smoke-")
	livePlugin := filepath.Join(operatorHome, ".roca", "plugins", pluginName)
	if _, err := os.Stat(livePlugin); !os.IsNotExist(err) {
		t.Fatal("the per-run smoke plugin path is not absent from the live operator home")
	}

	m := &world{binary: binary, home: home}
	if err := m.theOperatorsArtefacts(); err != nil {
		t.Fatalf("seed artefacts: %v", err)
	}
	if err := enableExperimentalPlugins(home); err != nil {
		t.Fatalf("enable plugins: %v", err)
	}

	if err := m.mustRun(m.initCommand(true)); err != nil {
		t.Fatalf("init: %v\n%s", err, m.last.stderr)
	}
	if err := m.mustRun("roca ingest --json"); err != nil {
		t.Fatalf("ingest: %v\n%s", err, m.last.stderr)
	}
	var ingest map[string]any
	if err := json.Unmarshal([]byte(m.last.stdout), &ingest); err != nil {
		t.Fatalf("ingest is not JSON: %v\n%s", err, m.last.stdout)
	}
	if _, ok := lookup(ingest, "errors"); !ok {
		t.Fatal("ingest JSON has no errors field")
	}
	if number(ingest, "errors") != 0 {
		t.Fatalf("ingest reported errors")
	}

	if err := m.mustRun("roca query 'what do we know about the ingest matrix' --json"); err != nil {
		t.Fatalf("query: %v\n%s", err, m.last.stderr)
	}
	var answer map[string]any
	if err := json.Unmarshal([]byte(m.last.stdout), &answer); err != nil {
		t.Fatalf("query is not JSON: %v\n%s", err, m.last.stdout)
	}
	engines, ok := answer["engines"].([]any)
	if !ok || len(engines) == 0 {
		t.Fatalf("query named no search engine: %v", answer["engines"])
	}

	source := filepath.Join(home, "src", pluginName)
	if err := writeHarborLedgerPackage(source, pluginName, "1.0.0"); err != nil {
		t.Fatalf("write plugin 1.0.0: %v", err)
	}
	if _, err := m.runWith("roca plugin install --yes --json",
		[]string{"plugin", "install", "--yes", "--json", source}); err != nil {
		t.Fatal(err)
	}
	if m.last.code != 0 {
		t.Fatalf("plugin install: code %d\n%s\n%s", m.last.code, m.last.stdout, m.last.stderr)
	}
	installed := pluginJSON(t, m.last.stdout)
	if installed["action"] != "installed" || installed["name"] != pluginName || installed["version"] != "1.0.0" {
		t.Fatalf("plugin install action=%v name=%v version=%v",
			installed["action"], installed["name"], installed["version"])
	}
	pluginDir := filepath.Join(home, ".roca", "plugins", pluginName)
	if _, err := os.Stat(pluginDir); err != nil {
		t.Fatalf("installed plugin missing from the disposable home: %v", err)
	}

	if err := writeHarborLedgerPackage(source, pluginName, "1.1.0"); err != nil {
		t.Fatalf("write plugin 1.1.0: %v", err)
	}
	if _, err := m.runWith("roca plugin update --yes --json",
		[]string{"plugin", "update", "--yes", "--json", pluginName}); err != nil {
		t.Fatal(err)
	}
	if m.last.code != 0 {
		t.Fatalf("plugin update: code %d\n%s\n%s", m.last.code, m.last.stdout, m.last.stderr)
	}
	updated := pluginJSON(t, m.last.stdout)
	if updated["action"] != "updated" || updated["name"] != pluginName || updated["version"] != "1.1.0" {
		t.Fatalf("plugin update action=%v name=%v version=%v",
			updated["action"], updated["name"], updated["version"])
	}

	if _, err := os.Stat(livePlugin); !os.IsNotExist(err) {
		t.Fatal("the smoke plugin landed outside the disposable home")
	}
}

// TestPublishedReleaseUpdateInitSmoke starts from an actual published binary,
// updates it through the real release protocol to the branch artefact, and
// initializes an otherwise clean disposable HOME with the updated executable.
func TestPublishedReleaseUpdateInitSmoke(t *testing.T) {
	published := strings.TrimSpace(os.Getenv("ROCA_PUBLISHED_BIN"))
	if published == "" {
		t.Skip("set ROCA_PUBLISHED_BIN to run the published update smoke")
	}
	info, err := os.Stat(published)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("published binary %s is not executable: %v", published, err)
	}
	version, err := exec.Command(published, "version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(version), "roca v") || strings.Contains(string(version), "dirty") {
		t.Fatalf("ROCA_PUBLISHED_BIN is not a published release: %v\n%s", err, version)
	}

	binary, err := rocaBinary()
	if err != nil {
		t.Fatal(err)
	}
	home := disposableSmokeHome(t, "roca-published-update-")
	m := &world{binary: binary, home: home, releaseStamped: published}
	t.Cleanup(m.closeTheChannel)
	channel := m.theChannel()
	if err := m.installBinary(); err != nil {
		t.Fatal(err)
	}
	channel.publishNew = true
	updated, err := m.run("roca update")
	if err != nil || updated.code != 0 {
		t.Fatalf("published update: %v, code %d\n%s%s", err, updated.code, updated.stdout, updated.stderr)
	}
	installedVersion, err := exec.Command(m.binaryPath(), "version").CombinedOutput()
	reportedVersion := strings.TrimSpace(string(installedVersion))
	wantVersion := "roca " + theNewVersion
	if err != nil || (reportedVersion != wantVersion && !strings.HasPrefix(reportedVersion, wantVersion+" ")) {
		t.Fatalf("updated executable reports %q, want %s: %v", reportedVersion, wantVersion, err)
	}
	initialized, err := m.run(m.initCommand(true))
	if err != nil || initialized.code != 0 {
		t.Fatalf("init after published update: %v, code %d\n%s%s", err, initialized.code, initialized.stdout, initialized.stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(initialized.stdout), &result); err != nil || result["database"] != "created" {
		t.Fatalf("init did not create the clean home: %v\n%s", err, initialized.stdout)
	}
}

func disposableSmokeHome(t *testing.T, prefix string) string {
	t.Helper()
	home, err := acceptanceTempDir(prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func enableExperimentalPlugins(home string) error {
	path := filepath.Join(home, ".roca", "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.Contains(string(raw), "plugins = true") {
		return nil
	}
	return os.WriteFile(path, append(raw, []byte("\n[features]\nplugins = true\n")...), 0o600)
}

func writeHarborLedgerPackage(directory, name, version string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	manifest := fmt.Sprintf(`{
  "schema": 1,
  "name": %q,
  "version": %q,
  "binary": "roca",
  "databases": [{
    "name": "records",
    "path": "ledger.db",
    "alias": "harbor_ledger",
    "attachment": "on-demand",
    "retention": "Synthetic smoke rows only."
  }],
  "semantic": {"databases": [{
    "database": "records",
    "description": "Synthetic harbor ledger rows.",
    "questions": ["Which harbor ledger rows exist?"],
    "tables": [{
      "name": "entries",
      "description": "One synthetic ledger row.",
      "columns": ["id", "title"]
    }]
  }]}
}
`, name, version)
	if err := os.WriteFile(filepath.Join(directory, "plugin.json"), []byte(manifest), 0o600); err != nil {
		return err
	}
	database := filepath.Join(directory, "ledger.db")
	if err := os.Remove(database); err != nil && !os.IsNotExist(err) {
		return err
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE entries (id INTEGER PRIMARY KEY, title TEXT NOT NULL);
		INSERT INTO entries (title) VALUES ('harbor lantern')`); err != nil {
		db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	return writeNamedChecksums(directory, "plugin.json", "ledger.db")
}

func writeNamedChecksums(directory string, names ...string) error {
	var body strings.Builder
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		fmt.Fprintf(&body, "%x  %s\n", sum, name)
	}
	return os.WriteFile(filepath.Join(directory, "checksums.txt"), []byte(body.String()), 0o600)
}

func pluginJSON(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("plugin envelope is not JSON: %v\n%s", err, stdout)
	}
	return document
}
