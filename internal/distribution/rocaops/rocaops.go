package rocaops

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	_ "modernc.org/sqlite"
)

const (
	Name             = "roca-ops"
	DatabaseFilename = "roca-ops.db"
	BundledSource    = "bundled:roca"
)

func Ensure(root, binDir, version string) (plugininstall.Result, error) {
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	target := filepath.Join(root, Name)
	if manifest, err := plugininstall.ReadManifest(target); err == nil {
		if manifest.Name != Name || manifest.Source != BundledSource {
			return plugininstall.Result{}, fmt.Errorf(
				"the bundled %s plugin collides with an installation from %q", Name, manifest.Source)
		}
		if manifest.Version == version {
			if err := applySchema(filepath.Join(target, manifest.Database)); err != nil {
				return plugininstall.Result{}, err
			}
			return resultFromManifest(target, manifest), nil
		}
	} else if _, statErr := os.Lstat(target); statErr == nil {
		return plugininstall.Result{}, fmt.Errorf(
			"the bundled %s plugin cannot replace an unmanaged directory at %s", Name, target)
	} else if !os.IsNotExist(statErr) {
		return plugininstall.Result{}, statErr
	}

	candidate, cleanup, err := materialize(root, version)
	if err != nil {
		return plugininstall.Result{}, err
	}
	defer cleanup()
	manager := plugininstall.Manager{PluginRoot: root, BinDir: binDir}
	var result plugininstall.Result
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		result, err = manager.Install(candidate)
	} else {
		result, err = manager.Update(candidate)
	}
	if err != nil {
		return plugininstall.Result{}, err
	}
	if err := applySchema(filepath.Join(target, DatabaseFilename)); err != nil {
		return plugininstall.Result{}, err
	}
	return result, nil
}

func materialize(root, version string) (plugininstall.Candidate, func(), error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return plugininstall.Candidate{}, func() {}, fmt.Errorf("create plugin directory: %w", err)
	}
	directory, err := os.MkdirTemp(root, ".roca-ops-bundle-")
	if err != nil {
		return plugininstall.Candidate{}, func() {}, fmt.Errorf("stage bundled plugin: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	fail := func(err error) (plugininstall.Candidate, func(), error) {
		cleanup()
		return plugininstall.Candidate{}, func() {}, err
	}

	metadata, err := json.Marshal(map[string]any{"schema": 1, "name": Name, "version": version})
	if err != nil {
		return fail(err)
	}
	metadata = append(metadata, '\n')
	for name, body := range map[string][]byte{
		plugininstall.PackageFilename: metadata,
		"semantic.yaml":               semantic,
	} {
		if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
			return fail(fmt.Errorf("write bundled %s: %w", name, err))
		}
	}
	if err := applySchema(filepath.Join(directory, DatabaseFilename)); err != nil {
		return fail(err)
	}

	files := []string{plugininstall.PackageFilename, "semantic.yaml", DatabaseFilename}
	sort.Strings(files)
	var checksums strings.Builder
	for _, name := range files {
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
	candidate, err := plugininstall.Inspect(BundledSource, directory)
	if err != nil {
		return fail(err)
	}
	return candidate, cleanup, nil
}

func applySchema(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open bundled %s database: %w", Name, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return fmt.Errorf("apply bundled %s schema: %w", Name, err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close bundled %s database: %w", Name, err)
	}
	return os.Chmod(path, 0o600)
}

func resultFromManifest(directory string, manifest plugininstall.Manifest) plugininstall.Result {
	return plugininstall.Result{
		Name: manifest.Name, Version: manifest.Version, Checksum: manifest.Checksum,
		Risk: manifest.Risk, Directory: directory, Executable: manifest.Executable,
	}
}
