// Package bundledplugin owns the verified install and in-place update shared
// by the data plugins compiled into the Roca binary.
package bundledplugin

import (
	"bytes"
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

type Spec struct {
	Name             string
	DatabaseFilename string
	Source           string
	Semantic         []byte
	Manifest         []byte
	ApplySchema      func(string) error
}

func Ensure(root, binDir, version string, spec Spec) (plugininstall.Result, error) {
	if err := spec.valid(); err != nil {
		return plugininstall.Result{}, err
	}
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	target := filepath.Join(root, spec.Name)
	if manifest, err := plugininstall.ReadManifest(target); err == nil {
		if manifest.Name != spec.Name || manifest.Source != spec.Source {
			return plugininstall.Result{}, fmt.Errorf(
				"the bundled %s plugin collides with an installation from %q", spec.Name, manifest.Source)
		}
		if manifest.Version == version {
			return resultFromManifest(target, manifest), nil
		}
	} else if _, statErr := os.Lstat(target); statErr == nil {
		return plugininstall.Result{}, fmt.Errorf(
			"the bundled %s plugin cannot replace an unmanaged directory at %s", spec.Name, target)
	} else if !os.IsNotExist(statErr) {
		return plugininstall.Result{}, statErr
	}

	candidate, cleanup, err := materialize(root, version, spec)
	if err != nil {
		return plugininstall.Result{}, err
	}
	defer cleanup()
	// The staged candidate already carries its declaration, so only an update
	// has a live database to upgrade. It is upgraded before the manifest records
	// the new version: a manifest that ran ahead of its schema would short-circuit
	// every later run and leave the interrupted upgrade unfinished forever.
	manager := plugininstall.Manager{PluginRoot: root, BinDir: binDir}
	var result plugininstall.Result
	if _, statErr := os.Lstat(target); os.IsNotExist(statErr) {
		result, err = manager.Install(candidate)
	} else {
		if err := spec.ApplySchema(filepath.Join(target, spec.DatabaseFilename)); err != nil {
			return plugininstall.Result{}, err
		}
		result, err = manager.UpdateInPlace(candidate)
	}
	if err != nil {
		return plugininstall.Result{}, err
	}
	return result, nil
}

func (spec Spec) valid() error {
	if spec.Name == "" || spec.DatabaseFilename == "" || spec.Source == "" || spec.ApplySchema == nil ||
		(len(spec.Semantic) == 0) == (len(spec.Manifest) == 0) {
		return fmt.Errorf("a bundled plugin needs a name, database, source, one manifest format, and schema owner")
	}
	return nil
}

func materialize(root, version string, spec Spec) (plugininstall.Candidate, func(), error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return plugininstall.Candidate{}, func() {}, fmt.Errorf("create plugin directory: %w", err)
	}
	directory, err := os.MkdirTemp(root, "."+spec.Name+"-bundle-")
	if err != nil {
		return plugininstall.Candidate{}, func() {}, fmt.Errorf("stage bundled plugin: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	fail := func(err error) (plugininstall.Candidate, func(), error) {
		cleanup()
		return plugininstall.Candidate{}, func() {}, err
	}

	var metadata []byte
	if len(spec.Manifest) > 0 {
		manifest, decodeErr := plugin.DecodeUnvalidatedManifest(bytes.NewReader(spec.Manifest))
		if decodeErr != nil {
			return fail(fmt.Errorf("parse bundled %s: %w", plugin.PackageFilename, decodeErr))
		}
		manifest.Name, manifest.Version = spec.Name, version
		if err := manifest.Valid(); err != nil {
			return fail(err)
		}
		metadata, err = json.MarshalIndent(manifest, "", "  ")
	} else {
		metadata, err = json.Marshal(map[string]any{"schema": 1, "name": spec.Name, "version": version})
	}
	if err != nil {
		return fail(err)
	}
	payload := map[string][]byte{plugininstall.PackageFilename: append(metadata, '\n')}
	if len(spec.Semantic) > 0 {
		payload[plugin.SemanticFilename] = spec.Semantic
	}
	for name, body := range payload {
		if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
			return fail(fmt.Errorf("write bundled %s: %w", name, err))
		}
	}
	if err := spec.ApplySchema(filepath.Join(directory, spec.DatabaseFilename)); err != nil {
		return fail(err)
	}

	names := []string{plugininstall.PackageFilename, spec.DatabaseFilename}
	if len(spec.Semantic) > 0 {
		names = append(names, plugin.SemanticFilename)
	}
	sort.Strings(names)
	var checksums strings.Builder
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return fail(err)
		}
		digest := sha256.Sum256(body)
		fmt.Fprintf(&checksums, "%s  %s\n", hex.EncodeToString(digest[:]), name)
	}
	if err := os.WriteFile(filepath.Join(directory, plugininstall.ChecksumsFilename),
		[]byte(checksums.String()), 0o600); err != nil {
		return fail(err)
	}
	candidate, err := plugininstall.Inspect(spec.Source, directory)
	if err != nil {
		return fail(err)
	}
	return candidate, cleanup, nil
}

func resultFromManifest(directory string, manifest plugininstall.Manifest) plugininstall.Result {
	return plugininstall.Result{
		Name: manifest.Name, Version: manifest.Version, Checksum: manifest.Checksum,
		Risk: manifest.Risk, Directory: directory, Executable: manifest.Executable,
	}
}
