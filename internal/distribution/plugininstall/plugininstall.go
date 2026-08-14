// Package plugininstall owns verified plugin package lifecycle operations.
package plugininstall

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

const (
	PackageFilename   = "plugin.json"
	ChecksumsFilename = "checksums.txt"
	ManifestFilename  = ".roca-plugin.json"
	manifestSchema    = 1
)

type Risk string

const (
	DataOnly   Risk = "data-only"
	Executable Risk = "executable"
)

type Candidate struct {
	Name       string
	Version    string
	Source     string
	Directory  string
	Checksum   string
	Risk       Risk
	Custody    bool
	Database   string
	Executable string
	Files      map[string]string
}

type Manifest struct {
	Schema         int               `json:"schema"`
	Name           string            `json:"name"`
	Source         string            `json:"source"`
	Version        string            `json:"version"`
	Checksum       string            `json:"checksum"`
	Risk           Risk              `json:"risk"`
	Custody        bool              `json:"custody"`
	Database       string            `json:"database"`
	Executable     string            `json:"executable,omitempty"`
	ExecutableFile string            `json:"executable_file,omitempty"`
	Files          map[string]string `json:"files"`
}

type Result struct {
	Name       string `json:"name"`
	Version    string `json:"version,omitempty"`
	Checksum   string `json:"checksum,omitempty"`
	Risk       Risk   `json:"risk,omitempty"`
	Directory  string `json:"directory,omitempty"`
	Executable string `json:"executable,omitempty"`
	ArchivedTo string `json:"archived_to,omitempty"`
}

type Manager struct {
	PluginRoot  string
	BinDir      string
	ArchiveRoot string
	Now         func() time.Time
}

type Resolved struct {
	Reference string
	Directory string
}

type packageMetadata struct {
	Schema  int    `json:"schema"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

func Resolve(ctx context.Context, reference, scratchRoot string) (Resolved, func(), error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return Resolved{}, func() {}, fmt.Errorf("plugin source is empty")
	}
	if info, err := os.Stat(reference); err == nil {
		if !info.IsDir() {
			return Resolved{}, func() {}, fmt.Errorf("plugin source %s is not a directory", reference)
		}
		absolute, err := filepath.Abs(reference)
		if err != nil {
			return Resolved{}, func() {}, fmt.Errorf("resolve plugin source: %w", err)
		}
		return Resolved{Reference: absolute, Directory: absolute}, func() {}, nil
	}

	cloneSource := reference
	if repository, ok := RepositoryURL(reference); ok {
		cloneSource = repository
	} else if !sourceURL(reference) {
		return Resolved{}, func() {}, fmt.Errorf(
			"plugin source %q is neither a directory, URL, nor owner/repo", reference)
	}
	if strings.HasPrefix(cloneSource, "-") {
		return Resolved{}, func() {}, fmt.Errorf("plugin source may not begin with '-'")
	}
	if err := os.MkdirAll(scratchRoot, 0o700); err != nil {
		return Resolved{}, func() {}, fmt.Errorf("create plugin download directory: %w", err)
	}
	directory, err := os.MkdirTemp(scratchRoot, "source-")
	if err != nil {
		return Resolved{}, func() {}, fmt.Errorf("create plugin download: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	command := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--", cloneSource, directory)
	if output, err := command.CombinedOutput(); err != nil {
		cleanup()
		return Resolved{}, func() {}, fmt.Errorf("clone plugin source %s: %w: %s",
			reference, err, strings.TrimSpace(string(output)))
	}
	return Resolved{Reference: reference, Directory: directory}, cleanup, nil
}

func RepositoryURL(reference string) (string, bool) {
	parts := strings.Split(reference, "/")
	if len(parts) != 2 || !safeName(parts[0]) || !safeName(parts[1]) {
		return "", false
	}
	return "https://github.com/" + reference + ".git", true
}

func sourceURL(reference string) bool {
	for _, prefix := range []string{"https://", "http://", "ssh://", "git://", "file://", "git@"} {
		if strings.HasPrefix(reference, prefix) {
			return true
		}
	}
	return false
}

func Inspect(source, directory string) (Candidate, error) {
	checksums, err := readChecksums(filepath.Join(directory, ChecksumsFilename))
	if err != nil {
		return Candidate{}, err
	}
	if err := verifyChecksummedFiles(directory, checksums); err != nil {
		return Candidate{}, err
	}
	metadataPath := filepath.Join(directory, PackageFilename)
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		return Candidate{}, fmt.Errorf("read %s: %w", PackageFilename, err)
	}
	var metadata packageMetadata
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return Candidate{}, fmt.Errorf("parse %s: %w", PackageFilename, err)
	}
	if metadata.Schema != 1 || !safeName(metadata.Name) || strings.TrimSpace(metadata.Version) == "" {
		return Candidate{}, fmt.Errorf(
			"%s needs schema 1, a safe name, and a version", PackageFilename)
	}
	descriptor, err := plugin.Inspect(metadata.Name, directory)
	if err != nil {
		return Candidate{}, fmt.Errorf("inspect plugin package: %w", err)
	}
	rides, err := plugin.InspectRides(metadata.Name, directory)
	if err != nil {
		return Candidate{}, fmt.Errorf("inspect plugin package: %w", err)
	}

	executable := ""
	for _, name := range executableNames(metadata.Name) {
		path := filepath.Join(directory, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
				return Candidate{}, fmt.Errorf("plugin executable %s is not executable", name)
			}
			executable = name
			break
		}
	}
	required := []string{PackageFilename, plugin.SemanticFilename, filepath.Base(descriptor.Database)}
	risk := DataOnly
	// A ride manifest is an execution surface as much as a shipped binary is:
	// the cron train hands every declared command to a shell under the
	// operator's own privileges, so such a package is never data-only.
	if len(rides) > 0 {
		required = append(required, plugin.RidesFilename)
		risk = Executable
	}
	if executable != "" {
		required = append(required, executable)
		risk = Executable
	}
	if err := verifyExactFiles(directory, required, checksums); err != nil {
		return Candidate{}, err
	}

	return Candidate{
		Name: metadata.Name, Version: metadata.Version, Source: source,
		Directory: directory, Checksum: packageChecksum(checksums), Risk: risk,
		Custody: descriptor.Semantic.Custody, Database: filepath.Base(descriptor.Database),
		Executable: executable, Files: checksums,
	}, nil
}

func readChecksums(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ChecksumsFilename, err)
	}
	defer file.Close()
	checksums := map[string]string{}
	scanner := bufio.NewScanner(io.LimitReader(file, 1<<20))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 || !safeFile(fields[1]) {
			return nil, fmt.Errorf("%s has an invalid line %q", ChecksumsFilename, line)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return nil, fmt.Errorf("%s has an invalid checksum for %s", ChecksumsFilename, fields[1])
		}
		if _, exists := checksums[fields[1]]; exists {
			return nil, fmt.Errorf("%s repeats %s", ChecksumsFilename, fields[1])
		}
		checksums[fields[1]] = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", ChecksumsFilename, err)
	}
	return checksums, nil
}

func verifyExactFiles(directory string, required []string, checksums map[string]string) error {
	sort.Strings(required)
	declared := make([]string, 0, len(checksums))
	for name := range checksums {
		declared = append(declared, name)
	}
	sort.Strings(declared)
	if strings.Join(required, "\x00") != strings.Join(declared, "\x00") {
		return fmt.Errorf("%s declares %v, want exactly %v", ChecksumsFilename, declared, required)
	}
	return verifyChecksummedFiles(directory, checksums)
}

func verifyChecksummedFiles(directory string, checksums map[string]string) error {
	for name, expected := range checksums {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("checksum source file %s is not a regular file", name)
		}
		digest, err := fileChecksum(path)
		if err != nil {
			return err
		}
		if digest != expected {
			return fmt.Errorf("checksum mismatch for %s: source declares %s, file is %s",
				name, expected, digest)
		}
	}
	return nil
}

func packageChecksum(checksums map[string]string) string {
	names := make([]string, 0, len(checksums))
	for name := range checksums {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		fmt.Fprintf(hash, "%s\x00%s\n", name, checksums[name])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (m Manager) Install(candidate Candidate) (Result, error) {
	if err := m.valid(); err != nil {
		return Result{}, err
	}
	target := filepath.Join(m.PluginRoot, candidate.Name)
	if _, err := os.Lstat(target); err == nil || !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("plugin %s is already installed; run `roca plugin update %s`",
			candidate.Name, candidate.Name)
	}
	executable := m.executablePath(candidate)
	if err := refuseExecutableCollision(executable); err != nil {
		return Result{}, err
	}
	staged, err := m.stage(candidate, "")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(staged)
	if err := os.Rename(staged, target); err != nil {
		return Result{}, fmt.Errorf("install plugin directory: %w", err)
	}
	if executable != "" {
		if err := installFile(filepath.Join(target, candidate.Executable), executable, 0o700); err != nil {
			_ = os.RemoveAll(target)
			return Result{}, err
		}
	}
	return resultFor(candidate, target, executable), nil
}

func (m Manager) Update(candidate Candidate) (Result, error) {
	if err := m.valid(); err != nil {
		return Result{}, err
	}
	target := filepath.Join(m.PluginRoot, candidate.Name)
	previousManifest, err := ReadManifest(target)
	if err != nil {
		return Result{}, fmt.Errorf("update plugin %s: %w", candidate.Name, err)
	}
	if previousManifest.Database != candidate.Database {
		return Result{}, fmt.Errorf("plugin %s changed its database filename from %s to %s; update refused to protect data",
			candidate.Name, previousManifest.Database, candidate.Database)
	}
	if err := m.verifyOwnedExecutable(previousManifest); err != nil {
		return Result{}, err
	}
	executable := m.executablePath(candidate)
	if previousManifest.Executable == "" && executable != "" {
		if err := refuseExecutableCollision(executable); err != nil {
			return Result{}, err
		}
	}
	staged, err := m.stage(candidate, filepath.Join(target, previousManifest.Database))
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(staged)
	backup := filepath.Join(m.PluginRoot, "."+candidate.Name+".previous")
	if _, err := os.Lstat(backup); err == nil {
		return Result{}, fmt.Errorf("update recovery directory already exists at %s", backup)
	}
	if err := os.Rename(target, backup); err != nil {
		return Result{}, fmt.Errorf("preserve previous plugin: %w", err)
	}
	if err := os.Rename(staged, target); err != nil {
		_ = os.Rename(backup, target)
		return Result{}, fmt.Errorf("activate plugin update: %w", err)
	}
	if candidate.Executable == "" && previousManifest.Executable != "" {
		if err := os.Remove(previousManifest.Executable); err != nil && !os.IsNotExist(err) {
			_ = os.RemoveAll(target)
			_ = os.Rename(backup, target)
			return Result{}, fmt.Errorf("remove retired plugin executable: %w", err)
		}
	} else if executable != "" {
		if err := installFile(filepath.Join(target, candidate.Executable), executable, 0o700); err != nil {
			_ = os.RemoveAll(target)
			_ = os.Rename(backup, target)
			return Result{}, err
		}
	}
	if err := os.RemoveAll(backup); err != nil {
		return Result{}, fmt.Errorf("remove previous plugin after update: %w", err)
	}
	return resultFor(candidate, target, executable), nil
}

func (m Manager) Uninstall(name string) (Result, error) {
	if err := m.valid(); err != nil {
		return Result{}, err
	}
	if !safeName(name) {
		return Result{}, fmt.Errorf("invalid plugin name %q", name)
	}
	target := filepath.Join(m.PluginRoot, name)
	manifest, err := ReadManifest(target)
	if err != nil {
		return Result{}, fmt.Errorf("uninstall plugin %s: %w", name, err)
	}
	if manifest.Name != name {
		return Result{}, fmt.Errorf("plugin manifest names %q, not %q", manifest.Name, name)
	}
	if err := m.verifyOwnedExecutable(manifest); err != nil {
		return Result{}, err
	}
	result := Result{Name: name, Version: manifest.Version, Checksum: manifest.Checksum,
		Risk: manifest.Risk, Directory: target, Executable: manifest.Executable}
	if manifest.Custody {
		archiveRoot := m.ArchiveRoot
		if archiveRoot == "" {
			archiveRoot = filepath.Join(filepath.Dir(m.PluginRoot), "plugin-custody")
		}
		if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
			return Result{}, fmt.Errorf("create custody archive: %w", err)
		}
		now := time.Now
		if m.Now != nil {
			now = m.Now
		}
		archive := filepath.Join(archiveRoot, name+"-"+now().UTC().Format("20060102T150405Z"))
		if _, err := os.Lstat(archive); err == nil || !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("custody archive already exists at %s", archive)
		}
		if err := os.Rename(target, archive); err != nil {
			return Result{}, fmt.Errorf("archive custodial plugin: %w", err)
		}
		result.ArchivedTo = archive
	} else if err := os.RemoveAll(target); err != nil {
		return Result{}, fmt.Errorf("remove plugin directory: %w", err)
	}
	if manifest.Executable != "" {
		if err := os.Remove(manifest.Executable); err != nil && !os.IsNotExist(err) {
			return result, fmt.Errorf("remove plugin executable: %w", err)
		}
	}
	return result, nil
}

// InstalledExecutable is the executable a caller may delete as this product's
// own: the path the manifest recorded, and only while the file is still the
// verified one. An executable somebody replaced after the install is theirs,
// which is the same refusal verifyOwnedExecutable makes during an update.
func InstalledExecutable(manifest Manifest) string {
	if manifest.Executable == "" {
		return ""
	}
	digest, err := fileChecksum(manifest.Executable)
	if err != nil || digest != manifest.Files[manifest.ExecutableFile] {
		return ""
	}
	return manifest.Executable
}

func (m Manager) stage(candidate Candidate, preservedDatabase string) (string, error) {
	if err := os.MkdirAll(m.PluginRoot, 0o700); err != nil {
		return "", fmt.Errorf("create plugin directory: %w", err)
	}
	staged, err := os.MkdirTemp(m.PluginRoot, ".install-")
	if err != nil {
		return "", fmt.Errorf("stage plugin: %w", err)
	}
	failed := func(err error) (string, error) {
		_ = os.RemoveAll(staged)
		return "", err
	}
	for name := range candidate.Files {
		mode := os.FileMode(0o600)
		if name == candidate.Executable {
			mode = 0o700
		}
		if err := installFile(filepath.Join(candidate.Directory, name), filepath.Join(staged, name), mode); err != nil {
			return failed(err)
		}
	}
	if err := installFile(filepath.Join(candidate.Directory, ChecksumsFilename),
		filepath.Join(staged, ChecksumsFilename), 0o600); err != nil {
		return failed(err)
	}
	if err := verifyExactFiles(staged, mapKeys(candidate.Files), candidate.Files); err != nil {
		return failed(fmt.Errorf("verify staged plugin: %w", err))
	}
	if preservedDatabase != "" {
		if err := installFile(preservedDatabase, filepath.Join(staged, candidate.Database), 0o600); err != nil {
			return failed(fmt.Errorf("preserve plugin database: %w", err))
		}
	}
	if err := writeManifest(staged, candidate, m.executablePath(candidate)); err != nil {
		return failed(err)
	}
	return staged, nil
}

func writeManifest(directory string, candidate Candidate, executable string) error {
	manifest := Manifest{
		Schema: manifestSchema, Name: candidate.Name, Source: candidate.Source,
		Version: candidate.Version, Checksum: candidate.Checksum, Risk: candidate.Risk,
		Custody: candidate.Custody, Database: candidate.Database, Executable: executable,
		ExecutableFile: candidate.Executable, Files: candidate.Files,
	}
	manifestRaw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plugin manifest: %w", err)
	}
	manifestRaw = append(manifestRaw, '\n')
	if err := os.WriteFile(filepath.Join(directory, ManifestFilename), manifestRaw, 0o600); err != nil {
		return fmt.Errorf("write plugin manifest: %w", err)
	}
	return nil
}

// UpdateInPlace refreshes a data-only plugin's payload inside the directory it
// already occupies. A staged update replaces the directory, which unlinks the
// custody database out from under any process that already holds it open: its
// writes land in an inode nobody can reach again. Nothing but the payload
// changes between versions of such a plugin, so nothing else has to move.
func (m Manager) UpdateInPlace(candidate Candidate) (Result, error) {
	if err := m.valid(); err != nil {
		return Result{}, err
	}
	if candidate.Risk != DataOnly || candidate.Executable != "" {
		return Result{}, fmt.Errorf(
			"plugin %s can run code; an in-place update is refused", candidate.Name)
	}
	target := filepath.Join(m.PluginRoot, candidate.Name)
	previous, err := ReadManifest(target)
	if err != nil {
		return Result{}, fmt.Errorf("update plugin %s: %w", candidate.Name, err)
	}
	if previous.Executable != "" {
		return Result{}, fmt.Errorf(
			"plugin %s was installed with an executable; an in-place update is refused", candidate.Name)
	}
	if previous.Database != candidate.Database {
		return Result{}, fmt.Errorf(
			"plugin %s changed its database filename from %s to %s; update refused to protect data",
			candidate.Name, previous.Database, candidate.Database)
	}
	payload := make([]string, 0, len(candidate.Files))
	for name := range candidate.Files {
		if name == candidate.Database {
			continue
		}
		payload = append(payload, name)
		if err := installFile(filepath.Join(candidate.Directory, name),
			filepath.Join(target, name), 0o600); err != nil {
			return Result{}, err
		}
	}
	if err := installFile(filepath.Join(candidate.Directory, ChecksumsFilename),
		filepath.Join(target, ChecksumsFilename), 0o600); err != nil {
		return Result{}, err
	}
	replaced := make(map[string]string, len(payload))
	for _, name := range payload {
		replaced[name] = candidate.Files[name]
	}
	if err := verifyChecksummedFiles(target, replaced); err != nil {
		return Result{}, fmt.Errorf("verify updated plugin: %w", err)
	}
	for name := range previous.Files {
		if _, kept := candidate.Files[name]; kept || name == previous.Database {
			continue
		}
		if err := os.Remove(filepath.Join(target, name)); err != nil && !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("remove retired plugin file %s: %w", name, err)
		}
	}
	if err := writeManifest(target, candidate, ""); err != nil {
		return Result{}, err
	}
	return resultFor(candidate, target, ""), nil
}

func ReadManifest(directory string) (Manifest, error) {
	file, err := os.Open(filepath.Join(directory, ManifestFilename))
	if err != nil {
		return Manifest{}, fmt.Errorf("read %s: %w", ManifestFilename, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", ManifestFilename, err)
	}
	if manifest.Schema != manifestSchema || !safeName(manifest.Name) || manifest.Source == "" ||
		manifest.Version == "" || manifest.Checksum == "" || !safeFile(manifest.Database) {
		return Manifest{}, fmt.Errorf("%s is incomplete or unsupported", ManifestFilename)
	}
	return manifest, nil
}

// VerifyInstalledPayload proves that an installed directory still matches the
// manifest and checksums written by the installer. The database is excluded
// because it is the plugin's mutable user-owned state; every executable or
// declarative payload, including rides.toml, remains immutable and verified.
func VerifyInstalledPayload(expectedName, directory string) (Manifest, error) {
	for _, name := range []string{ManifestFilename, ChecksumsFilename} {
		info, err := os.Lstat(filepath.Join(directory, name))
		if err != nil || !info.Mode().IsRegular() {
			return Manifest{}, fmt.Errorf("installed %s is not a regular file", name)
		}
	}
	manifest, err := ReadManifest(directory)
	if err != nil {
		return Manifest{}, err
	}
	if manifest.Name != expectedName {
		return Manifest{}, fmt.Errorf(
			"%s names plugin %s, not directory %s", ManifestFilename, manifest.Name, expectedName)
	}
	checksums, err := readChecksums(filepath.Join(directory, ChecksumsFilename))
	if err != nil {
		return Manifest{}, err
	}
	if !maps.Equal(manifest.Files, checksums) {
		return Manifest{}, fmt.Errorf(
			"%s payload checksums differ from %s", ManifestFilename, ChecksumsFilename)
	}
	if checksum := packageChecksum(checksums); manifest.Checksum != checksum {
		return Manifest{}, fmt.Errorf(
			"%s package checksum is %s, want %s", ManifestFilename, manifest.Checksum, checksum)
	}
	for _, name := range []string{PackageFilename, plugin.SemanticFilename, manifest.Database} {
		if _, declared := checksums[name]; !declared {
			return Manifest{}, fmt.Errorf("%s does not own required payload %s", ManifestFilename, name)
		}
	}
	immutable := maps.Clone(checksums)
	delete(immutable, manifest.Database)
	if err := verifyChecksummedFiles(directory, immutable); err != nil {
		return Manifest{}, err
	}
	if info, err := os.Lstat(filepath.Join(directory, manifest.Database)); err != nil ||
		!info.Mode().IsRegular() {
		return Manifest{}, fmt.Errorf("installed database %s is not a regular file", manifest.Database)
	}
	if _, err := os.Lstat(filepath.Join(directory, plugin.RidesFilename)); err == nil {
		if _, declared := immutable[plugin.RidesFilename]; !declared {
			return Manifest{}, fmt.Errorf(
				"%s is not owned by %s", plugin.RidesFilename, ManifestFilename)
		}
	} else if !os.IsNotExist(err) {
		return Manifest{}, fmt.Errorf("inspect %s: %w", plugin.RidesFilename, err)
	}
	return manifest, nil
}

func (m Manager) valid() error {
	if m.PluginRoot == "" || m.BinDir == "" {
		return fmt.Errorf("plugin root and executable directory are required")
	}
	return nil
}

func (m Manager) executablePath(candidate Candidate) string {
	if candidate.Executable == "" {
		return ""
	}
	return filepath.Join(m.BinDir, candidate.Executable)
}

func (m Manager) verifyOwnedExecutable(manifest Manifest) error {
	if manifest.Executable == "" {
		return nil
	}
	expected := filepath.Join(m.BinDir, manifest.ExecutableFile)
	if filepath.Clean(manifest.Executable) != filepath.Clean(expected) {
		return fmt.Errorf("plugin executable path %s is outside the configured executable directory", manifest.Executable)
	}
	digest, err := fileChecksum(expected)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if digest != manifest.Files[manifest.ExecutableFile] {
		return fmt.Errorf("plugin executable %s changed since install; refusing to overwrite or delete it", expected)
	}
	return nil
}

func resultFor(candidate Candidate, directory, executable string) Result {
	return Result{Name: candidate.Name, Version: candidate.Version, Checksum: candidate.Checksum,
		Risk: candidate.Risk, Directory: directory, Executable: executable}
}

func refuseExecutableCollision(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
		return fmt.Errorf("refusing to overwrite existing executable %s", path)
	}
	return nil
}

func installFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("read %s: %w", source, err)
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create directory for %s: %w", destination, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".plugin-file-")
	if err != nil {
		return fmt.Errorf("stage %s: %w", destination, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return fmt.Errorf("set permissions for %s: %w", destination, err)
	}
	if _, err := io.Copy(temporary, input); err != nil {
		temporary.Close()
		return fmt.Errorf("write %s: %w", destination, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close %s: %w", destination, err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("install %s: %w", destination, err)
	}
	return nil
}

func fileChecksum(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("checksum %s: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func executableNames(name string) []string {
	base := "roca-" + name
	if runtime.GOOS == "windows" {
		return []string{base + ".exe", base + ".com", base + ".bat", base + ".cmd"}
	}
	return []string{base}
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func safeName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && !strings.ContainsRune("-_", char) {
			return false
		}
	}
	return true
}

// safeFile only keeps a payload name inside its own directory. Which names a
// package may actually carry is decided by verifyExactFiles, not here.
func safeFile(name string) bool {
	return name != "" && name != "." && name != ".." &&
		filepath.Base(name) == name && !strings.ContainsAny(name, `/\`)
}
