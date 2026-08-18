// Package bundledplugin owns verified installation and refresh for plugins
// shipped inside the Roca binary. Its batch atomicity contract is reasonable
// preflight plus startup convergence: an environmental failure may leave a
// transient mixed version, but the next successful startup repairs it.
package bundledplugin

import (
	"bytes"
	"context"
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
	bundle, err := prepare(root, binDir, version, spec, false)
	if err != nil {
		return plugininstall.Result{}, err
	}
	defer bundle.cleanup()
	return bundle.apply()
}

func EnsureAll(root, binDir, version string, specs ...Spec) ([]plugininstall.Result, error) {
	names, executables := map[string]bool{}, map[string]bool{}
	for _, spec := range specs {
		if err := spec.valid(); err != nil {
			return nil, err
		}
		if names[spec.Name] {
			return nil, fmt.Errorf("bundled plugin %s is declared more than once", spec.Name)
		}
		names[spec.Name] = true
		if spec.Executable != "" && executables[spec.Executable] {
			return nil, fmt.Errorf("bundled executable %s is declared more than once", spec.Executable)
		}
		if spec.Executable != "" {
			executables[spec.Executable] = true
		}
	}
	prepared := make([]preparedBundle, 0, len(specs))
	defer func() {
		for _, bundle := range prepared {
			bundle.cleanup()
		}
	}()
	for _, spec := range specs {
		bundle, err := prepare(root, binDir, version, spec, true)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, bundle)
	}
	results := make([]plugininstall.Result, 0, len(prepared))
	for _, bundle := range prepared {
		result, err := bundle.apply()
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

type bundleAction uint8

const (
	bundleUnchanged bundleAction = iota
	bundleInstall
	bundleUpdateData
	bundleUpdateExecutable
	bundleRepairExecutable
)

type preparedBundle struct {
	action    bundleAction
	target    string
	spec      Spec
	manifest  plugininstall.Manifest
	candidate plugininstall.Candidate
	manager   plugininstall.Manager
	cleanup   func()
}

func prepare(root, binDir, version string, spec Spec, validateUnchanged bool) (preparedBundle, error) {
	if err := spec.valid(); err != nil {
		return preparedBundle{}, err
	}
	if root == "" || binDir == "" {
		return preparedBundle{}, fmt.Errorf("plugin root and executable directory are required")
	}
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	target := filepath.Join(root, spec.Name)
	manager := plugininstall.Manager{PluginRoot: root, BinDir: binDir}
	var installed plugininstall.Manifest
	installedFound := false
	if manifest, err := plugininstall.ReadManifest(target); err == nil {
		if manifest.Name != spec.Name || manifest.Source != spec.Source {
			return preparedBundle{}, fmt.Errorf(
				"the bundled %s plugin collides with an installation from %q", spec.Name, manifest.Source)
		}
		verified, verifyErr := plugininstall.VerifyInstalledPayload(spec.Name, target)
		if verifyErr != nil {
			return preparedBundle{}, fmt.Errorf("verify bundled %s plugin: %w", spec.Name, verifyErr)
		}
		installed, installedFound = verified, true
		if manifest.Version == version && spec.Executable == "" && !validateUnchanged {
			return preparedBundle{action: bundleUnchanged, target: target, spec: spec,
				manifest: verified, manager: manager, cleanup: func() {}}, nil
		}
	} else if _, statErr := os.Lstat(target); statErr == nil {
		return preparedBundle{}, fmt.Errorf(
			"the bundled %s plugin cannot replace an unmanaged directory at %s", spec.Name, target)
	} else if !os.IsNotExist(statErr) {
		return preparedBundle{}, statErr
	}

	candidate, cleanup, err := materialize(root, version, spec)
	if err != nil {
		return preparedBundle{}, err
	}
	fail := func(err error) (preparedBundle, error) {
		cleanup()
		return preparedBundle{}, err
	}
	bundle := preparedBundle{target: target, spec: spec, manifest: installed,
		candidate: candidate, manager: manager, cleanup: cleanup}
	switch {
	case !installedFound:
		bundle.action = bundleInstall
		err = manager.PreflightInstall(candidate)
	case installed.Version == version && spec.Executable == "":
		bundle.action = bundleUnchanged
	case installed.Version == version:
		bundle.action = bundleRepairExecutable
		err = manager.PreflightExecutableRepair(candidate)
	case spec.Executable != "":
		bundle.action = bundleUpdateExecutable
		err = manager.PreflightUpdate(candidate)
	default:
		bundle.action = bundleUpdateData
		err = manager.PreflightUpdateInPlace(candidate)
	}
	if err == nil && validateUnchanged && installedFound && spec.DatabaseFilename != "" {
		err = preflightInstalledSchema(root, target, spec)
	}
	if err != nil {
		return fail(err)
	}
	return bundle, nil
}

func (bundle preparedBundle) apply() (plugininstall.Result, error) {
	switch bundle.action {
	case bundleUnchanged:
		return resultFromManifest(bundle.target, bundle.manifest), nil
	case bundleInstall:
		return bundle.manager.Install(bundle.candidate)
	case bundleUpdateData:
		if err := bundle.spec.ApplySchema(
			filepath.Join(bundle.target, bundle.spec.DatabaseFilename)); err != nil {
			return plugininstall.Result{}, err
		}
		return bundle.manager.UpdateInPlace(bundle.candidate)
	case bundleUpdateExecutable:
		return bundle.manager.Update(bundle.candidate)
	case bundleRepairExecutable:
		return bundle.manager.RepairExecutable(bundle.candidate)
	default:
		return plugininstall.Result{}, fmt.Errorf("unsupported bundled plugin action")
	}
}

func preflightInstalledSchema(root, target string, spec Spec) error {
	directory, err := os.MkdirTemp(root, "."+spec.Name+"-schema-preflight-")
	if err != nil {
		return fmt.Errorf("stage bundled %s schema preflight: %w", spec.Name, err)
	}
	defer os.RemoveAll(directory)
	source := filepath.Join(target, spec.DatabaseFilename)
	if err := probeInstalledDatabase(source, spec.Name); err != nil {
		return err
	}
	clone := filepath.Join(directory, spec.DatabaseFilename)
	db, err := OpenDatabase(source, true)
	if err != nil {
		return fmt.Errorf("open bundled %s schema preflight source: %w", spec.Name, err)
	}
	_, copyErr := db.ExecContext(context.Background(), "VACUUM INTO ?", clone)
	closeErr := db.Close()
	if copyErr != nil {
		return fmt.Errorf("copy bundled %s database for schema preflight: %w", spec.Name, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close bundled %s schema preflight source: %w", spec.Name, closeErr)
	}
	if err := spec.ApplySchema(clone); err != nil {
		return fmt.Errorf("preflight bundled %s schema: %w", spec.Name, err)
	}
	return nil
}

func probeInstalledDatabase(path, name string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect bundled %s database writeability: %w", name, err)
	}
	if info.Mode().Perm()&0o222 == 0 {
		return fmt.Errorf("bundled %s database is not writable", name)
	}
	db, err := openDatabase(path, "rw", busyTimeout)
	if err != nil {
		return fmt.Errorf("open bundled %s database read-write: %w", name, err)
	}
	pingErr := db.PingContext(context.Background())
	closeErr := db.Close()
	if pingErr != nil {
		return fmt.Errorf("open bundled %s database read-write: %w", name, pingErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close bundled %s read-write probe: %w", name, closeErr)
	}
	return nil
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
