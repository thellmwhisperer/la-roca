//go:build acceptance

package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
)

// The channel serves the release document the way GitHub actually serves it:
// pretty-printed, with every field on its own line. The installer's asset
// parser exists to survive that shape, and this test is the in-repo guard that
// it does. Without it the only evidence was the `/tmp` transcripts in the PR
// body, which are not a regression guard: if the parser were ever "simplified"
// back to a splitter that only copes with compact JSON, nothing here would go
// red.
//
// The harness knob (`installWorld.prettyJSON`) makes `writeRelease` emit
// MarshalIndent, and this test is what exercises it.
func TestTheInstallerResolvesAPrettyPrintedReleaseDocument(t *testing.T) {
	m := releaseInstallerWorld(t)
	channel := m.theChannel()
	channel.prettyJSON = true

	if err := m.iRunTheInstaller(); err != nil {
		t.Fatalf("the installer could not be run: %v", err)
	}
	if m.last.code != 0 {
		t.Fatalf("the installer exited %d against a pretty-printed release:\n%s%s",
			m.last.code, m.last.stdout, m.last.stderr)
	}
	if err := m.onlyRocaExecutables(); err != nil {
		t.Fatalf("the artefact did not land: %v", err)
	}
	if err := m.versionExitsWith(0); err != nil {
		t.Fatalf("the installed binary does not answer --version: %v", err)
	}
}

func TestTheInstallerPlacesBundledVectorBesideACustomPrefix(t *testing.T) {
	m := releaseInstallerWorld(t)
	channel := m.theChannel()
	prefix := filepath.Join(m.home, "custom", "bin")
	command := exec.Command("sh", theInstallerPath(),
		"--repo", channel.repo, "--api", channel.server.URL, "--prefix", prefix)
	command.Env = m.environment()
	if err := m.record("installer with custom prefix", command); err != nil {
		t.Fatal(err)
	}
	if m.last.code != 0 {
		t.Fatalf("installer exited %d: %s%s", m.last.code, m.last.stdout, m.last.stderr)
	}
	for _, name := range []string{"roca", "roca-vector"} {
		if info, err := os.Stat(filepath.Join(prefix, name)); err != nil || info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s was not executable in the custom prefix: %v", name, err)
		}
	}
	manifest, err := plugininstall.ReadManifest(
		filepath.Join(m.home, ".roca", "plugins", "roca-vector"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Executable != filepath.Join(prefix, "roca-vector") {
		t.Fatalf("vector executable = %q, want custom prefix %q", manifest.Executable, prefix)
	}
}

func TestTheInstallerUpdatesWhenAZeroByteLockIsPresent(t *testing.T) {
	m := releaseInstallerWorld(t)
	requireInitialInstall(t, m)
	lock := filepath.Join(m.home, ".roca", "plugins", "roca-vector", "state", "vector.db.index.lock")
	if err := writeFixture(lock, ""); err != nil {
		t.Fatal(err)
	}
	lockInfo, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}

	if err := m.iRunTheInstallerOfTheNewVersion(); err != nil {
		t.Fatal(err)
	}
	if m.last.code != 0 {
		t.Fatalf("upgrade with a 0-byte lock exited %d:\n%s%s", m.last.code, m.last.stdout, m.last.stderr)
	}
	upgrade := m.last
	if err := m.theVersionIsTheNewOne(); err != nil {
		t.Fatalf("the binary was not updated: %v", err)
	}
	installerOutput := strings.ReplaceAll(upgrade.stdout+upgrade.stderr, m.home, "$HOME")
	t.Logf("zero-byte lock size: %d bytes\ninstaller output:\n%sinstalled version:\n%s",
		lockInfo.Size(), installerOutput, m.last.stdout)
}

func TestIssue401LiveUpdatePreservesOperatorRide(t *testing.T) {
	m := releaseInstallerWorld(t)
	if err := m.installedAtAnEarlierReleaseVersion(); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(m.home, ".roca", "config.toml")
	config := []byte(`[features]
cron = true

[ride.vector_delta]
command = "echo UPDATE_RIDE_EXECUTED"
gate = "after_ingest"
`)
	if err := os.WriteFile(configPath, config, 0o666); err != nil {
		t.Fatal(err)
	}

	updated, err := m.run("roca update")
	if err != nil {
		t.Fatal(err)
	}
	if updated.code != 0 {
		t.Fatalf("update exited %d:\n%s%s", updated.code, updated.stdout, updated.stderr)
	}
	version, err := m.run("roca --version")
	if err != nil {
		t.Fatal(err)
	}
	if version.code != 0 || !strings.Contains(version.stdout, theNewVersion) {
		t.Fatalf("updated version exited %d:\n%s%s", version.code, version.stdout, version.stderr)
	}
	kept, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != string(config) {
		t.Fatalf("update changed operator configuration:\n%s", kept)
	}

	run, err := m.run("roca cron run nightly")
	if err != nil {
		t.Fatal(err)
	}
	output := run.stdout + run.stderr
	if run.code != 0 || !strings.Contains(output, "UPDATE_RIDE_EXECUTED") ||
		!strings.Contains(output, "operator\tvector_delta\tafter_ingest_ok\texit=0") ||
		!strings.Contains(output, "train nightly: 2 rides, 0 failed, 0 deferred") {
		t.Fatalf("post-update cron exited %d:\n%s", run.code, output)
	}
}

func TestTheInstallerRestoresThePreviousBinaryWhenBundledPlacementFails(t *testing.T) {
	m := releaseInstallerWorld(t)
	requireInitialInstall(t, m)
	manifest := filepath.Join(m.home, ".roca", "plugins", "roca-vector", ".roca-plugin.json")
	body, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(body), `"source": "bundled:roca"`, `"source": "fixture:collision"`, 1)
	if changed == string(body) {
		t.Fatal("the installed manifest did not declare the bundled source")
	}
	if err := os.WriteFile(manifest, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.iRunTheInstallerOfTheNewVersion(); err != nil {
		t.Fatal(err)
	}
	failure := m.last.stdout + m.last.stderr
	if m.last.code == 0 || !strings.Contains(failure, "bundled plugins could not be placed") {
		t.Fatalf("bundled placement failure was not reported: code %d\n%s", m.last.code, failure)
	}
	if err := m.theVersionIsStillTheBuiltOne(); err != nil {
		t.Fatalf("the prior binary was not restored: %v", err)
	}
	if !strings.Contains(failure, "previous binary is back") {
		t.Fatalf("the rollback was not reported:\n%s", failure)
	}
}

func releaseInstallerWorld(t *testing.T) *world {
	t.Helper()
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := &world{binary: binary, home: home}
	t.Cleanup(m.closeTheChannel)
	return m
}

func requireInitialInstall(t *testing.T, m *world) {
	t.Helper()
	if err := m.iRunTheInstaller(); err != nil || m.last.code != 0 {
		t.Fatalf("initial install: %v, code %d:\n%s%s", err, m.last.code, m.last.stdout, m.last.stderr)
	}
}
