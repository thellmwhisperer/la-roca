package artifact_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

func TestRefreshPreservesTheUserZoneAndGuardsTheSystemZone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	user := "operator line one\noperator line two\n"
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
		{name: "recognized shipped content", previous: "shipped-v1\n"},
		{name: "unrecognized content", previous: "operator legacy bytes\n", wantUser: "operator legacy bytes\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "prompt.md")
			write(t, path, test.previous)
			request := artifact.FileRequest{
				Path: path, System: "shipped-v2\n", LegacySystems: []string{"shipped-v1\n"},
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
			assertZones(t, path, "shipped-v2\n", test.wantUser)
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

func TestADeletedRegisteredArtifactIsDivergence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.md")
	request := artifact.FileRequest{
		Path: path, System: "system\n", PreviousSystemSHA256: artifact.Checksum("old\n"), Enabled: true,
	}
	out, err := artifact.RefreshFile(request)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Diverged || out.Changed {
		t.Fatalf("deleted registered artifact outcome = %+v", out)
	}
	request.Force = true
	out, err = artifact.RefreshFile(request)
	if err != nil || !out.Changed {
		t.Fatalf("forced recreation = %+v, err %v", out, err)
	}
}

func TestMalformedZoneMarkersAreNeverAdoptedAsUserContent(t *testing.T) {
	for _, body := range []string{
		"<!-- ROCA SYSTEM BEGIN -->\nmissing the remaining markers\n",
		"---\n# ROCA SYSTEM BEGIN\nmissing the remaining markers\n",
	} {
		path := filepath.Join(t.TempDir(), "artifact.md")
		write(t, path, body)
		_, err := artifact.RefreshFile(artifact.FileRequest{Path: path, System: "system\n", Enabled: true})
		if err == nil || read(t, path) != body {
			t.Fatalf("malformed artifact was adopted: err=%v body=%q", err, read(t, path))
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
