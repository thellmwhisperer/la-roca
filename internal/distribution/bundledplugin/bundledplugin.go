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
	LegacyName       string
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
	migration legacyNameMigration
	cleanup   func()
}

type legacyNameMigration struct {
	legacy string
	target string
}

func (migration legacyNameMigration) needed() bool {
	return migration.legacy != ""
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
	migration, err := prepareLegacyNameMigration(root, spec)
	if err != nil {
		return preparedBundle{}, err
	}
	installedTarget := target
	if migration.needed() {
		installedTarget = migration.legacy
	}
	manager := plugininstall.Manager{PluginRoot: root, BinDir: binDir}
	var installed plugininstall.Manifest
	installedFound := false
	if manifest, err := plugininstall.ReadManifest(installedTarget); err == nil {
		validName := manifest.Name == spec.Name ||
			migration.needed() && manifest.Name == spec.LegacyName
		if !validName || manifest.Source != spec.Source {
			return preparedBundle{}, fmt.Errorf(
				"the bundled %s plugin collides with an installation from %q", spec.Name, manifest.Source)
		}
		verified, verifyErr := plugininstall.VerifyInstalledPayload(manifest.Name, installedTarget)
		if verifyErr != nil {
			return preparedBundle{}, fmt.Errorf("verify bundled %s plugin: %w", spec.Name, verifyErr)
		}
		installed, installedFound = verified, true
		if manifest.Version == version && spec.Executable == "" && !validateUnchanged && !migration.needed() {
			return preparedBundle{action: bundleUnchanged, target: target, spec: spec,
				manifest: verified, manager: manager, cleanup: func() {}}, nil
		}
	} else if _, statErr := os.Lstat(installedTarget); statErr == nil {
		return preparedBundle{}, fmt.Errorf(
			"the bundled %s plugin cannot replace an unmanaged directory at %s", spec.Name, installedTarget)
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
		candidate: candidate, manager: manager, migration: migration, cleanup: cleanup}
	switch {
	case !installedFound:
		bundle.action = bundleInstall
		err = manager.PreflightInstall(candidate)
	case installed.Version == version && spec.Executable == "" && !migration.needed():
		bundle.action = bundleUnchanged
	case installed.Version == version && !migration.needed():
		bundle.action = bundleRepairExecutable
		err = manager.PreflightExecutableRepair(candidate)
	case spec.Executable != "":
		bundle.action = bundleUpdateExecutable
		err = manager.PreflightUpdateFrom(candidate, filepath.Base(installedTarget))
	default:
		bundle.action = bundleUpdateData
		err = manager.PreflightUpdateInPlaceFrom(candidate, filepath.Base(installedTarget))
	}
	if err == nil && validateUnchanged && installedFound && spec.DatabaseFilename != "" {
		err = preflightInstalledSchema(root, installedTarget, spec)
	}
	if err != nil {
		return fail(err)
	}
	return bundle, nil
}

func (bundle preparedBundle) apply() (plugininstall.Result, error) {
	if !bundle.migration.needed() {
		return bundle.applyPrepared()
	}
	if err := bundle.migration.apply(bundle.spec.Name); err != nil {
		return plugininstall.Result{}, err
	}
	result, err := bundle.applyPrepared()
	if err == nil {
		return result, nil
	}
	if rollbackErr := bundle.migration.rollback(bundle.spec.LegacyName); rollbackErr != nil {
		return plugininstall.Result{}, fmt.Errorf("%w; restore legacy plugin identity: %v", err, rollbackErr)
	}
	return plugininstall.Result{}, err
}

func (bundle preparedBundle) applyPrepared() (plugininstall.Result, error) {
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
	db, err := openDatabase(path, "rw")
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

func prepareLegacyNameMigration(root string, spec Spec) (legacyNameMigration, error) {
	if spec.LegacyName == "" || spec.LegacyName == spec.Name {
		return legacyNameMigration{}, nil
	}
	legacy := filepath.Join(root, spec.LegacyName)
	target := filepath.Join(root, spec.Name)
	legacyInfo, legacyErr := os.Lstat(legacy)
	_, targetErr := os.Lstat(target)
	legacyExists := legacyErr == nil
	targetExists := targetErr == nil
	if legacyErr != nil && !os.IsNotExist(legacyErr) {
		return legacyNameMigration{}, legacyErr
	}
	if targetErr != nil && !os.IsNotExist(targetErr) {
		return legacyNameMigration{}, targetErr
	}
	if !legacyExists {
		return legacyNameMigration{}, nil
	}
	if targetExists {
		return legacyNameMigration{}, fmt.Errorf(
			"the bundled %s plugin cannot migrate from %s because both %s and %s exist; remove or rename one directory and retry",
			spec.Name, spec.LegacyName, legacy, target)
	}
	if !legacyInfo.IsDir() {
		return legacyNameMigration{}, fmt.Errorf(
			"the bundled %s plugin cannot replace an unmanaged directory at %s", spec.Name, legacy)
	}
	return legacyNameMigration{legacy: legacy, target: target}, nil
}

func (migration legacyNameMigration) apply(name string) error {
	if err := os.Rename(migration.legacy, migration.target); err != nil {
		return fmt.Errorf("migrate bundled %s directory: %w", name, err)
	}
	if err := rewriteInstalledName(migration.target, name); err != nil {
		if rollbackErr := os.Rename(migration.target, migration.legacy); rollbackErr != nil {
			return fmt.Errorf("rewrite bundled %s install name: %w; restore legacy directory: %v",
				name, err, rollbackErr)
		}
		return fmt.Errorf("rewrite bundled %s install name: %w", name, err)
	}
	return nil
}

func (migration legacyNameMigration) rollback(name string) error {
	if err := rewriteInstalledName(migration.target, name); err != nil {
		return err
	}
	if err := os.Rename(migration.target, migration.legacy); err != nil {
		if restoreErr := rewriteInstalledName(migration.target, filepath.Base(migration.target)); restoreErr != nil {
			return fmt.Errorf("%w; restore current plugin identity: %v", err, restoreErr)
		}
		return err
	}
	return nil
}

func rewriteInstalledName(directory, name string) error {
	path := filepath.Join(directory, plugininstall.ManifestFilename)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	manifest["name"] = name
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+plugininstall.ManifestFilename+"-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func (spec Spec) valid() error {
	if spec.Name == "" || spec.Source == "" ||
		(len(spec.Semantic) == 0) == (len(spec.Manifest) == 0) {
		return fmt.Errorf("a bundled plugin needs a name, source, and one manifest format")
	}
	if spec.LegacyName != "" && (spec.LegacyName == spec.Name || filepath.Base(spec.LegacyName) != spec.LegacyName) {
		return fmt.Errorf("bundled plugin %s has an invalid legacy name %q", spec.Name, spec.LegacyName)
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
