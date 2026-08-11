//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/release"
)

func (w *distributionWorld) compareArtefactNames() error {
	script, err := os.ReadFile(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		return err
	}
	text := string(script)
	if !strings.Contains(text, `ARTEFACT="$BINARY-$TAG-$PLATFORM"`) {
		return fmt.Errorf("install.sh does not construct the versioned artefact name")
	}
	if strings.Contains(text, `ARTEFACT="$BINARY-$PLATFORM"`) {
		return fmt.Errorf("install.sh declares an unversioned artefact the Go release code does not")
	}
	platforms := []struct{ goos, goarch, platform string }{
		{"darwin", "arm64", "darwin-arm64"}, {"linux", "amd64", "linux-x64"},
		{"linux", "arm64", "linux-arm64"}, {"windows", "amd64", "windows-x64"},
	}
	names := map[string]string{}
	for _, item := range platforms {
		platform, err := release.Platform(item.goos, item.goarch)
		if err != nil || platform != item.platform {
			return fmt.Errorf("release platform for %s/%s = %q: %v", item.goos, item.goarch, platform, err)
		}
		name := release.ArtefactName("v9.8.7", platform)
		expected := "roca-v9.8.7-" + item.platform
		if item.goos == "windows" {
			expected += ".exe"
			if !strings.Contains(text, "roca-<version>-windows-x64.exe") {
				return fmt.Errorf("install.sh does not name the Windows artefact")
			}
		} else if !strings.Contains(text, item.platform) {
			return fmt.Errorf("install.sh does not name platform %s", item.platform)
		}
		if name != expected {
			return fmt.Errorf("Go artefact %q disagrees with installer name %q", name, expected)
		}
		names[item.platform] = name
	}
	w.state["artefactNames"] = names
	return nil
}

func (w *distributionWorld) artefactNamesAgree() error {
	names := w.state["artefactNames"].(map[string]string)
	unique := map[string]bool{}
	for _, name := range names {
		unique[name] = true
	}
	if len(names) != 4 || len(unique) != 4 {
		return fmt.Errorf("artefact catalogue is incomplete or ambiguous: %v", names)
	}
	return nil
}
