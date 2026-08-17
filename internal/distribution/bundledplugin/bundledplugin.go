// Package bundledplugin owns verified installation and refresh for plugins
// shipped inside the Roca binary.
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
	Executable       string
	Source           string
	Semantic         []byte
	Manifest         []byte
	ApplySchema      func(string) error
	Payload          func() ([]byte, error)
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
	manager := plugininstall.Manager{PluginRoot: root, BinDir: binDir}
	var result plugininstall.Result
	if _, statErr := os.Lstat(target); os.IsNotExist(statErr) {
		result, err = manager.Install(candidate)
	} else if spec.Executable != "" {
		result, err = manager.Update(candidate)
	} else {
		// The staged candidate already carries its declaration, so only a data
		// update has a live database to upgrade. It is upgraded before the
		// manifest records the new version: a manifest that ran ahead of its
		// schema would short-circuit every later run and leave the interrupted
		// upgrade unfinished forever.
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

// Manifest loads one bundled declaration and stamps the running build version
// through the same validation path used when the installer materializes it.
func Manifest(raw []byte, version string) (plugin.Manifest, error) {
	declaration, err := plugin.DecodeManifest(bytes.NewReader(raw))
	if err != nil {
		return plugin.Manifest{}, err
	}
	if strings.TrimSpace(version) != "" {
		declaration.Version = version
	}
	if err := declaration.Valid(); err != nil {
		return plugin.Manifest{}, err
	}
	return declaration, nil
}

func (spec Spec) valid() error {
	if spec.Name == "" || spec.Source == "" ||
		(len(spec.Semantic) == 0) == (len(spec.Manifest) == 0) {
		return fmt.Errorf("a bundled plugin needs a name, source, and one manifest format")
	}
	data := spec.DatabaseFilename != "" && spec.ApplySchema != nil &&
		spec.Executable == "" && spec.Payload == nil
	executable := spec.DatabaseFilename == "" && spec.ApplySchema == nil &&
		spec.Executable != "" && spec.Payload != nil && len(spec.Manifest) > 0
	if !data && !executable {
		return fmt.Errorf("a bundled plugin needs either a database schema owner or an executable payload")
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
	if spec.Executable != "" {
		var declaration map[string]any
		if decodeErr := json.Unmarshal(spec.Manifest, &declaration); decodeErr != nil {
			return fail(fmt.Errorf("parse bundled %s: %w", plugin.PackageFilename, decodeErr))
		}
		declaration["name"], declaration["version"] = spec.Name, version
		metadata, err = json.MarshalIndent(declaration, "", "  ")
	} else if len(spec.Manifest) > 0 {
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
	if spec.Executable != "" {
		executable, payloadErr := spec.Payload()
		if payloadErr != nil {
			return fail(fmt.Errorf("read bundled %s executable: %w", spec.Name, payloadErr))
		}
		if len(executable) == 0 {
			return fail(fmt.Errorf("bundled %s executable is empty", spec.Name))
		}
		payload[spec.Executable] = executable
	}
	for name, body := range payload {
		mode := os.FileMode(0o600)
		if name == spec.Executable {
			mode = 0o700
		}
		if err := os.WriteFile(filepath.Join(directory, name), body, mode); err != nil {
			return fail(fmt.Errorf("write bundled %s: %w", name, err))
		}
	}
	if spec.ApplySchema != nil {
		if err := spec.ApplySchema(filepath.Join(directory, spec.DatabaseFilename)); err != nil {
			return fail(err)
		}
	}

	names := []string{plugininstall.PackageFilename}
	if spec.DatabaseFilename != "" {
		names = append(names, spec.DatabaseFilename)
	}
	if len(spec.Semantic) > 0 {
		names = append(names, plugin.SemanticFilename)
	}
	if spec.Executable != "" {
		names = append(names, spec.Executable)
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
