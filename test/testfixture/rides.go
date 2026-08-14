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
		plugin.SemanticFilename:       []byte("version: 1\ndescription: Synthetic test plugin.\ntables: []\n"),
		"plugin.db":                   []byte("mutable test database"),
		plugin.RidesFilename:          []byte(rides),
	}
	names := make([]string, 0, len(files))
	for filename := range files {
		names = append(names, filename)
	}
	sort.Strings(names)
	digests := make(map[string]string, len(files))
	var checksums strings.Builder
	for _, filename := range names {
		body := files[filename]
		digest := sha256.Sum256(body)
		digests[filename] = hex.EncodeToString(digest[:])
		fmt.Fprintf(&checksums, "%s  %s\n", digests[filename], filename)
		if err := os.WriteFile(filepath.Join(source, filename), body, 0o600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(source, plugininstall.ChecksumsFilename),
		[]byte(checksums.String()), 0o600); err != nil {
		return err
	}
	packageHash := sha256.New()
	for _, filename := range names {
		fmt.Fprintf(packageHash, "%s\x00%s\n", filename, digests[filename])
	}
	candidate := plugininstall.Candidate{
		Name: name, Version: "test", Source: "synthetic:test", Directory: source,
		Checksum: hex.EncodeToString(packageHash.Sum(nil)), Risk: plugininstall.DataOnly,
		Database: "plugin.db", Files: digests,
	}
	_, err = (plugininstall.Manager{
		PluginRoot: root,
		BinDir:     filepath.Join(filepath.Dir(root), "bin"),
	}).Install(candidate)
	return err
}
