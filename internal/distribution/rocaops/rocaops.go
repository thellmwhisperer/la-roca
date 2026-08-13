package rocaops

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	_ "modernc.org/sqlite"
)

const (
	Name             = "roca-ops"
	DatabaseFilename = "roca-ops.db"
	BundledSource    = "bundled:roca"

	busyTimeout = 15 * time.Second
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
	if _, statErr := os.Lstat(target); os.IsNotExist(statErr) {
		result, err = manager.Install(candidate)
	} else {
		result, err = manager.UpdateInPlace(candidate)
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
	db, err := openDatabase(path)
	if err != nil {
		return err
	}
	applied, err := schemaApplied(db)
	if err != nil {
		db.Close()
		return err
	}
	if !applied {
		if _, err := db.Exec(schema); err != nil {
			db.Close()
			return fmt.Errorf("apply bundled %s schema: %w", Name, err)
		}
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close bundled %s database: %w", Name, err)
	}
	return os.Chmod(path, 0o600)
}

// openDatabase gives the installer's own connection the lock discipline every
// other connection to this file already has. An installer that refuses the
// moment somebody else holds the write lock is an installer that runs before
// every answer and fails whenever the machine is busy.
func openDatabase(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve the bundled %s database path: %w", Name, err)
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: url.Values{
		"_pragma": {fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds())},
	}.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open bundled %s database: %w", Name, err)
	}
	return db, nil
}

// schemaApplied answers with a read, so the common case never asks for the write
// lock. It looks for the identifier seed because schema.sql writes it last: what
// carries it carries everything the schema declares before it, and a database
// that lost the seed would hand out identifiers that collide with core's.
func schemaApplied(db *sql.DB) (bool, error) {
	var counters int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'sqlite_sequence'`).Scan(&counters); err != nil {
		return false, fmt.Errorf("read the bundled %s schema: %w", Name, err)
	}
	if counters == 0 {
		return false, nil
	}
	var seeded int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_sequence WHERE name = 'memories'").Scan(&seeded); err != nil {
		return false, fmt.Errorf("read the bundled %s identifier seed: %w", Name, err)
	}
	return seeded == 1, nil
}

func resultFromManifest(directory string, manifest plugininstall.Manifest) plugininstall.Result {
	return plugininstall.Result{
		Name: manifest.Name, Version: manifest.Version, Checksum: manifest.Checksum,
		Risk: manifest.Risk, Directory: directory, Executable: manifest.Executable,
	}
}
