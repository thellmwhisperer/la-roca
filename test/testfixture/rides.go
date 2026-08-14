package testfixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

// InstallRidePlugin builds the smallest installer-owned plugin fixture that
// can contribute a checksum-verified ride manifest.
func InstallRidePlugin(root, name, rides string) error {
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return err
	}
	source, err := os.MkdirTemp(filepath.Dir(root), ".ride-fixture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(source)
	metadata, err := json.Marshal(map[string]any{"schema": 1, "name": name, "version": "test"})
	if err != nil {
		return err
	}
	files := map[string][]byte{
		plugininstall.PackageFilename: append(metadata, '\n'),
		plugin.SemanticFilename: []byte("version: 1\ndescription: Synthetic test plugin.\n" +
			"questions:\n  - Which synthetic rides exist?\n" +
			"tables:\n  - name: records\n    description: Synthetic records only.\n    columns: [id, value]\n"),
		"plugin.db":          []byte("mutable test database"),
		plugin.RidesFilename: []byte(rides),
	}
	names := make([]string, 0, len(files))
	for filename := range files {
		names = append(names, filename)
	}
	sort.Strings(names)
	var checksums strings.Builder
	for _, filename := range names {
		body := files[filename]
		digest := sha256.Sum256(body)
		fmt.Fprintf(&checksums, "%s  %s\n", hex.EncodeToString(digest[:]), filename)
		if err := os.WriteFile(filepath.Join(source, filename), body, 0o600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(source, plugininstall.ChecksumsFilename),
		[]byte(checksums.String()), 0o600); err != nil {
		return err
	}
	// The fixture inspects its own package so an installed ride carries the
	// classification and consent the installer would have recorded for it.
	candidate, err := plugininstall.Inspect("synthetic:test", source)
	if err != nil {
		return err
	}
	_, err = (plugininstall.Manager{
		PluginRoot: root,
		BinDir:     filepath.Join(filepath.Dir(root), "bin"),
	}).Install(candidate)
	return err
}
