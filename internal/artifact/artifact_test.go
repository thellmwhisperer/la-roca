package artifact_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

func TestRefreshPreservesTheUserZoneAndGuardsTheSystemZone(t *testing.T) {
	// The documentation teaches operators these markers, so one quoted inside
	// their own lines is content and must remain readable after a refused refresh.
	user := "operator line one\n" + artifact.UserEnd + " is what closes my zone\n"
	for _, test := range []struct {
		name, system                string
		force, diverged, unsupported bool
	}{
		{name: "registered system", system: "shipped-v1\n", unsupported: true},
		{name: "edited system", system: "operator edited the system\n", diverged: true},
		{name: "forced edited system", system: "operator edited the system\n", force: true, diverged: true, unsupported: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "SKILL.md")
			previous := artifact.Zoned(test.system, user)
			write(t, path, previous)
			original, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}

			out, err := artifact.RefreshFile(artifact.FileRequest{
				Path: path, System: "shipped-v2\n", PreviousSystemSHA256: artifact.Checksum("shipped-v1\n"),
				Enabled: true, Force: test.force,
			})
			// Force overrides the divergence guard, not the publication guarantee.
			if test.unsupported {
				if !errors.Is(err, securefile.ErrConditionalReplaceUnsupported) {
					t.Fatalf("refresh error = %v, want conditional replacement refusal", err)
				}
				if out.Backup == "" || read(t, out.Backup) != previous {
					t.Fatalf("refusal did not report the preserved backup: %+v", out)
				}
			} else if err != nil || out.Backup != "" {
				t.Fatalf("divergence guard = %+v, err %v", out, err)
			}
			if out.Changed || !out.Outdated || out.Diverged != test.diverged {
				t.Fatalf("refused refresh outcome = %+v", out)
			}
			if got := read(t, path); got != previous {
				t.Fatalf("refusal changed the live artifact: %q", got)
			}
			current, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(original, current) || original.Mode() != current.Mode() {
				t.Fatal("refusal changed the live artifact's identity or permissions")
			}
			assertZones(t, path, test.system, user)
		})
	}
}

func TestLegacyAdoptionAndDisabledRefreshAreNonDestructive(t *testing.T) {
	tests := []struct {
		name, previous string
	}{
		{name: "recognized shipped content", previous: "## shipped\nv1\n"},
		// Every earlier release carried the signature and none of their bodies
		// are known here, so recognition cannot be an equality test against the
		// bytes this build happens to ship.
		{name: "recognized older release", previous: "## shipped\nv0 said something else\n"},
		{name: "unrecognized content", previous: "operator legacy bytes\n"},
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
			assertRefusedArtifact(t, path, test.previous, out, err)
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
	if strings.Join(owned, "\n") != strings.Join([]string{skillPath, registryPath}, "\n") {
		t.Fatalf("owned paths = %v", owned)
	}

	write(t, skillPath, artifact.Zoned("system\n", "mine\n"))
	owned, err = artifact.OwnedPaths(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 1 || owned[0] != registryPath {
		t.Fatalf("operator-owned zone was claimed: %v", owned)
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
			if err != nil || !out.Diverged || out.Changed ||
				out.Missing != test.missing || out.Unregistered != test.unregistered {
				t.Fatalf("divergence guard = %+v, err %v", out, err)
			}
			request.Force = true
			out, err = artifact.RefreshFile(request)
			if test.missing {
				if err != nil || !out.Changed || out.Diverged || out.Unregistered || out.Missing {
					t.Fatalf("forced creation = %+v, err %v", out, err)
				}
				assertZones(t, path, "system\n", "")
			} else {
				assertRefusedArtifact(t, path, test.seeded, out, err)
				if !out.Diverged || out.Unregistered != test.unregistered {
					t.Fatalf("refusal lost divergence class: %+v", out)
				}
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

// Force may pass the zone guard but cannot weaken conditional publication.
func TestForceRefusesAFileWhoseMarkersAreBroken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	broken := artifact.Zoned("shipped-v1\n", "mine\n") + "appended after the last marker\n"
	write(t, path, broken)

	out, err := artifact.RefreshFile(artifact.FileRequest{
		Path: path, System: "shipped-v2\n", Enabled: true, Force: true,
	})
	assertRefusedArtifact(t, path, broken, out, err)
}

func assertRefusedArtifact(t *testing.T, path, previous string, out artifact.FileOutcome, err error) {
	t.Helper()
	if !errors.Is(err, securefile.ErrConditionalReplaceUnsupported) || out.Changed || !out.Outdated {
		t.Fatalf("refused artifact = %+v, err %v", out, err)
	}
	if read(t, path) != previous || out.Backup == "" || read(t, out.Backup) != previous {
		t.Fatalf("refusal did not preserve live bytes and backup: %+v", out)
	}
	for _, file := range []string{path, out.Backup} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("permissions for %s: %v, err %v", file, info, err)
		}
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
