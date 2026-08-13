package release

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var frozenUpgradeVersions = []string{"v1.3.1", "v1.6.0", "v1.8.3", "v1.9.2"}

func TestUpgradeGauntletOwnsReleasedHomesAndBothDeliveryPaths(t *testing.T) {
	for _, version := range frozenUpgradeVersions {
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

	generator := readRepoFile(t, "../../../scripts/freeze-upgrade-home.sh")
	if !strings.Contains(generator, "gh release download") {
		t.Error("the fixture helper does not download actual GitHub release binaries")
	}
	runner := readRepoFile(t, "../../../scripts/upgrade-gauntlet.sh")
	for _, required := range []string{"ingest", "exec", "doctor", "health"} {
		if !strings.Contains(runner, required) {
			t.Errorf("the gauntlet does not run %s", required)
		}
	}
	if strings.Contains(runner, "release download") {
		t.Error("CI rebuilds fixtures instead of consuming the committed frozen homes")
	}

	ci := readRepoFile(t, "../../../.github/workflows/ci.yml")
	release := readRepoFile(t, "../../../.github/workflows/release.yml")
	for name, workflow := range map[string]string{"pull requests": ci, "releases": release} {
		if !strings.Contains(workflow, "upgrade-gauntlet") {
			t.Errorf("%s do not run the upgrade gauntlet", name)
		}
	}
	docs := strings.ToLower(readRepoFile(t, "../../../docs/releases.md"))
	if !strings.Contains(docs, "schema migration") || !strings.Contains(docs, "frozen upgrade home") {
		t.Error("release documentation does not make a new frozen home part of schema migration delivery")
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
