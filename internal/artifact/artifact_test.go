package artifact_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

func TestRefreshPreservesTheUserZoneAndGuardsTheSystemZone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	// The documentation teaches operators these markers, so one quoted inside
	// their own lines is content and has to survive every round trip. Reading the
	// first closing marker instead of the last one made that file unreadable from
	// then on, with force the only way back.
	user := "operator line one\n" + artifact.UserEnd + " is what closes my zone\n"
	write(t, path, artifact.Zoned("shipped-v1\n", user))

	out, err := artifact.RefreshFile(artifact.FileRequest{
		Path: path, System: "shipped-v2\n", PreviousSystemSHA256: artifact.Checksum("shipped-v1\n"),
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Changed || out.Diverged || out.Backup == "" {
		t.Fatalf("refresh outcome = %+v", out)
	}
	assertZones(t, path, "shipped-v2\n", user)
	if got := read(t, out.Backup); got != artifact.Zoned("shipped-v1\n", user) {
		t.Fatalf("backup = %q", got)
	}

	write(t, path, artifact.Zoned("operator edited the system\n", user))
	out, err = artifact.RefreshFile(artifact.FileRequest{
		Path: path, System: "shipped-v3\n", PreviousSystemSHA256: artifact.Checksum("shipped-v2\n"),
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Diverged || out.Changed {
		t.Fatalf("edited system outcome = %+v", out)
	}
	assertZones(t, path, "operator edited the system\n", user)

	out, err = artifact.RefreshFile(artifact.FileRequest{
		Path: path, System: "shipped-v3\n", PreviousSystemSHA256: artifact.Checksum("shipped-v2\n"),
		Enabled: true, Force: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Changed || out.Diverged {
		t.Fatalf("forced outcome = %+v", out)
	}
	assertZones(t, path, "shipped-v3\n", user)
}

func TestLegacyAdoptionAndDisabledRefreshAreNonDestructive(t *testing.T) {
	tests := []struct {
		name, previous, wantUser string
	}{
		{name: "recognized shipped content", previous: "## shipped\nv1\n"},
		// Every earlier release carried the signature and none of their bodies
		// are known here, so recognition cannot be an equality test against the
		// bytes this build happens to ship.
		{name: "recognized older release", previous: "## shipped\nv0 said something else\n"},
		{name: "unrecognized content", previous: "operator legacy bytes\n", wantUser: "operator legacy bytes\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "prompt.md")
			write(t, path, test.previous)
			request := artifact.FileRequest{
				Path: path, System: "## shipped\nv2\n", LegacySignature: "## shipped\n",
			}
			out, err := artifact.RefreshFile(request)
			if err != nil {
				t.Fatal(err)
			}
			if out.Changed || !out.Outdated || read(t, path) != test.previous {
				t.Fatalf("disabled refresh mutated legacy content: outcome=%+v body=%q", out, read(t, path))
			}

			request.Enabled = true
			out, err = artifact.RefreshFile(request)
			if err != nil {
				t.Fatal(err)
			}
			if !out.Changed || !out.Adopted {
				t.Fatalf("legacy adoption outcome = %+v", out)
			}
			assertZones(t, path, "## shipped\nv2\n", test.wantUser)
		})
	}
}

func TestRegistryIsVersionedAndFeedsSafeOwnedPaths(t *testing.T) {
	home := t.TempDir()
	registryPath := filepath.Join(home, ".roca", "artifacts.json")
	skillPath := filepath.Join(home, ".codex", "skills", "roca", "SKILL.md")
	write(t, skillPath, artifact.Zoned("system\n", ""))

	registry := artifact.Registry{Entries: []artifact.Entry{{
		Kind: "skill", Runtime: "codex", Path: skillPath,
		InstalledVersion: "v1.2.3", AvailableVersion: "v1.2.3",
		SystemSHA256: artifact.Checksum("system\n"),
	}}}
	if err := artifact.SaveRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	loaded, err := artifact.LoadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Schema != artifact.RegistrySchema || len(loaded.Entries) != 1 {
		t.Fatalf("registry = %+v", loaded)
	}
	owned, err := artifact.OwnedPaths(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{skillPath, registryPath, registryPath + ".lock", registryPath + ".mcp.lock", registryPath + ".hooks.lock", registryPath + ".zcode.lock"} {
		if !slices.Contains(owned, path) {
			t.Fatalf("owned paths do not include %s: %v", path, owned)
		}
	}

	write(t, skillPath, artifact.Zoned("system\n", "mine\n"))
	owned, err = artifact.OwnedPaths(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(owned, skillPath) {
		t.Fatalf("operator-owned zone was claimed: %v", owned)
	}
}

func TestRegistryMigratesOlderSchemasWithRecoveryBackup(t *testing.T) {
	for _, schema := range []int{1, 2} {
		t.Run(fmt.Sprintf("schema-%d", schema), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifacts.json")
			before := fmt.Sprintf(`{"schema":%d,"artifacts":[{"kind":"hook","runtime":"zcode","path":"/tmp/hook","created_root":true,"root_identity":"7:9","created_hooks_enabled":true}]}`, schema)
			write(t, path, before)
			registry, err := artifact.LoadRegistry(path)
			if err != nil {
				t.Fatal(err)
			}
			if registry.Schema != artifact.RegistrySchema || len(registry.Entries) != 1 ||
				!registry.Entries[0].CreatedRoot || !registry.Entries[0].CreatedHooksEnabled {
				t.Fatalf("migrated registry = %+v", registry)
			}
			if err := artifact.SaveRegistry(path, registry); err != nil {
				t.Fatal(err)
			}
			if backup := read(t, path+".roca.bak"); backup != before {
				t.Fatalf("migration backup = %q", backup)
			}
			migrated, err := artifact.LoadRegistry(path)
			if err != nil || migrated.Schema != artifact.RegistrySchema {
				t.Fatalf("saved migration = %+v, err=%v", migrated, err)
			}
		})
	}
}

func TestRegistryRefusesUnknownSchemaWithoutChangingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifacts.json")
	before := `{"schema":4,"artifacts":[]}`
	write(t, path, before)
	if _, err := artifact.LoadRegistry(path); err == nil {
		t.Fatal("newer registry schema was accepted for ownership reads")
	}
	if err := artifact.SaveRegistry(path, artifact.Registry{}); err == nil {
		t.Fatal("newer registry schema was overwritten")
	}
	if after := read(t, path); after != before {
		t.Fatalf("newer registry changed: %q", after)
	}
}

// The three refusals need the same consent and are not the same sentence to an
// operator: a file that is gone cannot be read back, and one no registry entry
// stands behind was never proven to be ours, so neither may be reported as an
// edit somebody has to go looking for.
func TestDivergenceClassesAreToldApart(t *testing.T) {
	for _, test := range []struct {
		name, seeded, previous string
		missing, unregistered  bool
	}{
		{name: "a registered artifact the operator deleted",
			previous: artifact.Checksum("old\n"), missing: true},
		{name: "a zoned artifact with no registry record",
			seeded: artifact.Zoned("someone else's system\n", "mine\n"), unregistered: true},
		{name: "an edited SYSTEM zone",
			seeded:   artifact.Zoned("edited\n", "mine\n"),
			previous: artifact.Checksum("old\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact.md")
			if test.seeded != "" {
				write(t, path, test.seeded)
			}
			request := artifact.FileRequest{
				Path: path, System: "system\n", PreviousSystemSHA256: test.previous, Enabled: true,
			}
			out, err := artifact.RefreshFile(request)
			if err != nil {
				t.Fatal(err)
			}
			if !out.Diverged || out.Changed ||
				out.Missing != test.missing || out.Unregistered != test.unregistered {
				t.Fatalf("refusal = %+v", out)
			}
			request.Force = true
			out, err = artifact.RefreshFile(request)
			if err != nil || !out.Changed || out.Diverged || out.Unregistered || out.Missing {
				t.Fatalf("forced replacement = %+v, err %v", out, err)
			}
		})
	}
}

// An install the operator typed by name is the consent a deleted artifact
// needs. Force is for bytes that are still there to lose, and demanding it here
// turned an explicit install of a file nobody has into a silent no-op.
func TestARestoredMissingArtifactNeedsNoForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.md")
	out, err := artifact.RefreshFile(artifact.FileRequest{
		Path: path, System: "system\n", PreviousSystemSHA256: artifact.Checksum("old\n"),
		Enabled: true, RestoreMissing: true,
	})
	if err != nil || !out.Changed || out.Diverged || out.Missing {
		t.Fatalf("restored artifact = %+v, err %v", out, err)
	}
	assertZones(t, path, "system\n", "")
}

func TestMalformedZoneMarkersAreNeverAdoptedAsUserContent(t *testing.T) {
	for _, body := range []string{
		"<!-- ROCA SYSTEM BEGIN -->\nmissing the remaining markers\n",
		"---\n# ROCA SYSTEM BEGIN\nmissing the remaining markers\n",
		artifact.SystemBegin + "\nsystem\n" + artifact.SystemEnd + "\nunowned bytes\n" +
			artifact.UserBegin + "\nuser\n" + artifact.UserEnd + "\n",
	} {
		path := filepath.Join(t.TempDir(), "artifact.md")
		write(t, path, body)
		_, err := artifact.RefreshFile(artifact.FileRequest{Path: path, System: "system\n", Enabled: true})
		if err == nil || read(t, path) != body {
			t.Fatalf("malformed artifact was adopted: err=%v body=%q", err, read(t, path))
		}
	}
}

// Force is the documented remedy for a broken artifact, so it has to reach the
// one file nothing else can repair: bytes appended after the last marker are
// the most natural way an operator breaks the zones, and before this the file
// could never be installed, refreshed, or withdrawn again.
func TestForceRepairsAFileWhoseMarkersAreBroken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	broken := artifact.Zoned("shipped-v1\n", "mine\n") + "appended after the last marker\n"
	write(t, path, broken)

	out, err := artifact.RefreshFile(artifact.FileRequest{
		Path: path, System: "shipped-v2\n", Enabled: true, Force: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Changed || out.Backup == "" {
		t.Fatalf("forced repair outcome = %+v", out)
	}
	assertZones(t, path, "shipped-v2\n", "")
	if got := read(t, out.Backup); got != broken {
		t.Fatalf("the replaced bytes were not preserved in the backup: %q", got)
	}
}

func assertZones(t *testing.T, path, system, user string) {
	t.Helper()
	zones, err := artifact.Parse(read(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if zones.System != system || zones.User != user {
		t.Fatalf("zones = system %q user %q", zones.System, zones.User)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := securefile.Write(path, []byte(body), 0o600, 0o700); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
