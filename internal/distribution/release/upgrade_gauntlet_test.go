package release

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// frozenUpgradeVersions reads the same list the runner and both workflows do,
// so a committed archive that nobody added to it, or a list entry with no
// archive, fails here instead of going unvalidated.
func frozenUpgradeVersions(t *testing.T) []string {
	t.Helper()
	var versions []string
	listed := readRepoFile(t, filepath.Join("testdata", "upgrade", "versions.txt"))
	for _, line := range strings.Split(listed, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			versions = append(versions, line)
		}
	}
	if len(versions) == 0 {
		t.Fatal("no frozen upgrade home is listed, so the gauntlet would pass over nothing")
	}
	return versions
}

func TestUpgradeGauntletOwnsReleasedHomes(t *testing.T) {
	for _, version := range frozenUpgradeVersions(t) {
		t.Run(version, func(t *testing.T) {
			fixture := filepath.Join("testdata", "upgrade", "homes", version+".tar.gz")
			files := archiveFiles(t, fixture)
			for _, name := range []string{".roca/roca.db", ".roca/config.toml", ".roca/prompt.md"} {
				if _, ok := files[name]; !ok {
					t.Errorf("frozen archive has no regular %s", name)
				}
			}

			var origin struct {
				Release string `json:"release"`
				Asset   string `json:"asset"`
				SHA256  string `json:"sha256"`
			}
			if err := json.Unmarshal(files["origin.json"], &origin); err != nil {
				t.Fatal(err)
			}
			if origin.Release != version || !strings.Contains(origin.Asset, version) || len(origin.SHA256) != 64 {
				t.Fatalf("origin = %#v, want a pinned %s release asset", origin, version)
			}
		})
	}
}

func TestUpgradeGauntletExecutesAgainstAnIsolatedFixture(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "roca")
	build := exec.Command("go", "build", "-o", binary, "../../../cmd/roca")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build current binary: %v\n%s", err, output)
	}

	run := exec.Command("bash", "../../../scripts/upgrade-gauntlet.sh", binary, "v1.84.2")
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("run upgrade gauntlet: %v\n%s", err, output)
	}
}

func archiveFiles(t *testing.T, path string) map[string][]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()

	files := map[string][]byte{}
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		name := strings.TrimPrefix(header.Name, "./")
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = body
	}
	return files
}
