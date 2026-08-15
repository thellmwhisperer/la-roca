// Package plugininstall owns verified plugin package lifecycle operations.
package plugininstall

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

const (
	PackageFilename   = plugin.PackageFilename
	ChecksumsFilename = "checksums.txt"
	ManifestFilename  = plugin.ManifestFilename
	manifestSchema    = 1
)

var errSourceNotRegular = errors.New("source is not a regular file")

type Risk string

const (
	DataOnly   Risk = "data-only"
	Executable Risk = "executable"
)

type PackageKind string

const (
	DataPackage       PackageKind = "data"
	ExecutablePackage PackageKind = "executable"
)

type Candidate struct {
	Name       string
	Version    string
	Source     string
	Directory  string
	Checksum   string
	Kind       PackageKind
	Risk       Risk
	Custody    bool
	Database   string
	Databases  []string
	Executable string
	StateDir   string
	Files      map[string]string
}

type Manifest struct {
	Schema         int               `json:"schema"`
	Name           string            `json:"name"`
	Source         string            `json:"source"`
	Version        string            `json:"version"`
	Checksum       string            `json:"checksum"`
	Kind           PackageKind       `json:"kind,omitempty"`
	Risk           Risk              `json:"risk"`
	Custody        bool              `json:"custody"`
	Database       string            `json:"database,omitempty"`
	Databases      []string          `json:"databases,omitempty"`
	Executable     string            `json:"executable,omitempty"`
	ExecutableFile string            `json:"executable_file,omitempty"`
	StateDir       string            `json:"state_directory,omitempty"`
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

func candidateDatabaseFiles(candidate Candidate) []string {
	if len(candidate.Databases) > 0 {
		return slices.Clone(candidate.Databases)
	}
	if candidate.Database != "" {
		return []string{candidate.Database}
	}
	return nil
}

// sameDatabaseFiles compares the two declarations as sets. The guard exists to
// protect installed data, and a manifest that lists the same files in another
// order moves none of it.
func sameDatabaseFiles(previous, candidate []string) bool {
	declared, offered := slices.Clone(previous), slices.Clone(candidate)
	slices.Sort(declared)
	slices.Sort(offered)
	return slices.Equal(declared, offered)
}

func federatedPackage(directory string) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(directory, PackageFilename))
	if err != nil {
		return false, fmt.Errorf("read %s: %w", PackageFilename, err)
	}
	return plugin.Federated(raw)
}

func manifestDatabaseFiles(manifest Manifest) []string {
	if len(manifest.Databases) > 0 {
		return slices.Clone(manifest.Databases)
	}
	if manifest.Database != "" {
		return []string{manifest.Database}
	}
	return nil
}

type packageMetadata struct {
	Schema   int         `json:"schema"`
	Name     string      `json:"name"`
	Version  string      `json:"version"`
	Kind     PackageKind `json:"kind,omitempty"`
	Custody  bool        `json:"custody,omitempty"`
	StateDir string      `json:"state_directory,omitempty"`
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
	fullManifest, err := plugin.Federated(raw)
	if err != nil {
		return Candidate{}, err
	}
	var metadata packageMetadata
	var federation plugin.Manifest
	if fullManifest {
		federation, err = plugin.ReadManifest(metadataPath)
		if err != nil {
			return Candidate{}, err
		}
		metadata = packageMetadata{Schema: federation.Schema, Name: federation.Name,
			Version: federation.Version, Kind: DataPackage}
	} else {
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&metadata); err != nil {
			return Candidate{}, fmt.Errorf("parse %s: %w", PackageFilename, err)
		}
	}
	// Every later lifecycle step reads the installed manifest back through
	// safeName, so a package whose name only clears the discovery predicate
	// would install into a directory update, verify, and uninstall refuse.
	if metadata.Schema != 1 || !safeName(metadata.Name) || strings.TrimSpace(metadata.Version) == "" {
		return Candidate{}, fmt.Errorf(
			"%s needs schema 1, a safe name, and a version", PackageFilename)
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
	if fullManifest && federation.Binary != "roca" && executable != federation.Binary {
		return Candidate{}, fmt.Errorf("%s declares binary %q but the package supplies %q",
			PackageFilename, federation.Binary, executable)
	}
	kind := metadata.Kind
	if kind == "" {
		kind = DataPackage
	}
	required := []string{PackageFilename}
	// A ride manifest is an execution surface as much as a shipped binary is:
	// the cron train hands every declared command to a shell under the
	// operator's own privileges, so such a package is never data-only.
	if len(rides) > 0 {
		required = append(required, plugin.RidesFilename)
	}
	candidate := Candidate{
		Name: metadata.Name, Version: metadata.Version, Source: source,
		Directory: directory, Checksum: packageChecksum(checksums), Kind: kind,
		Executable: executable, Files: checksums,
	}
	switch kind {
	case DataPackage:
		if !fullManifest && (metadata.Custody || metadata.StateDir != "") {
			return Candidate{}, fmt.Errorf(
				"%s data packages declare custody and writable state in %s",
				PackageFilename, plugin.SemanticFilename)
		}
		descriptors, err := plugin.InspectAll(metadata.Name, directory)
		if err != nil {
			return Candidate{}, fmt.Errorf("inspect plugin package: %w", err)
		}
		for _, descriptor := range descriptors {
			candidate.Custody = candidate.Custody || descriptor.Semantic.Custody
			candidate.Databases = append(candidate.Databases, filepath.Base(descriptor.Database))
		}
		if len(candidate.Databases) == 1 {
			candidate.Database = candidate.Databases[0]
		}
		candidate.Risk = DataOnly
		if !fullManifest {
			required = append(required, plugin.SemanticFilename)
		}
		required = append(required, candidate.Databases...)
		if executable != "" {
			required = append(required, executable)
		}
		if executable != "" || len(rides) > 0 {
			candidate.Risk = Executable
		}
	case ExecutablePackage:
		if executable == "" {
			return Candidate{}, fmt.Errorf("executable plugin %s has no roca-%s payload", metadata.Name, metadata.Name)
		}
		if metadata.StateDir != "" && !safeFile(metadata.StateDir) {
			return Candidate{}, fmt.Errorf("%s has an invalid state_directory %q", PackageFilename, metadata.StateDir)
		}
		if _, collision := checksums[metadata.StateDir]; metadata.StateDir != "" && collision {
			return Candidate{}, fmt.Errorf("%s state_directory %q collides with a payload file",
				PackageFilename, metadata.StateDir)
		}
		candidate.Risk = Executable
		candidate.Custody = metadata.Custody
		candidate.StateDir = metadata.StateDir
		required = append(required, executable)
	default:
		return Candidate{}, fmt.Errorf("%s kind is %q, want %q or %q", PackageFilename,
			kind, DataPackage, ExecutablePackage)
	}
	if err := verifyExactFiles(directory, required, checksums); err != nil {
		return Candidate{}, err
	}
	return candidate, nil
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
		file, err := openRegularSource(path)
		if errors.Is(err, errSourceNotRegular) {
			return fmt.Errorf("checksum source file %s is not a regular file", name)
		}
		if err != nil {
			return err
		}
		digest, checksumErr := openFileChecksum(file, path)
		closeErr := file.Close()
		if checksumErr != nil {
			return checksumErr
		}
		if closeErr != nil {
			return fmt.Errorf("close checksum source file %s: %w", name, closeErr)
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
	staged, err := m.stage(candidate, nil)
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(staged)
	if err := os.Rename(staged, target); err != nil {
		return Result{}, fmt.Errorf("install plugin directory: %w", err)
	}
	if err := createStateDir(target, candidate.StateDir); err != nil {
		_ = os.RemoveAll(target)
		return Result{}, err
	}
	if executable != "" {
		if err := installFile(filepath.Join(target, candidate.Executable), executable, 0o700); err != nil {
			_ = os.RemoveAll(target)
			return Result{}, err
		}
	}
	return resultFor(candidate, target, executable), nil
}

func createStateDir(target, name string) error {
	if name == "" {
		return nil
	}
	if err := os.Mkdir(filepath.Join(target, name), 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create plugin state directory: %w", err)
	}
	return nil
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
	if previousManifest.Kind != candidate.Kind {
		return Result{}, fmt.Errorf("plugin %s changed its package kind from %s to %s; update refused",
			candidate.Name, previousManifest.Kind, candidate.Kind)
	}
	previousDatabases := manifestDatabaseFiles(previousManifest)
	candidateDatabases := candidateDatabaseFiles(candidate)
	if !sameDatabaseFiles(previousDatabases, candidateDatabases) {
		return Result{}, fmt.Errorf("plugin %s changed its database files from %v to %v; update refused to protect data",
			candidate.Name, previousDatabases, candidateDatabases)
	}
	if previousManifest.StateDir != candidate.StateDir {
		return Result{}, fmt.Errorf("plugin %s changed its state directory from %s to %s; update refused to protect data",
			candidate.Name, previousManifest.StateDir, candidate.StateDir)
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
	preservedDatabases := make(map[string]string, len(previousDatabases))
	for _, database := range previousDatabases {
		preservedDatabases[database] = filepath.Join(target, database)
	}
	staged, err := m.stage(candidate, preservedDatabases)
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
	stateMoved := false
	if candidate.StateDir != "" {
		state := filepath.Join(backup, candidate.StateDir)
		err := os.Rename(state, filepath.Join(target, candidate.StateDir))
		switch {
		case err == nil:
			stateMoved = true
		case os.IsNotExist(err):
			if err := createStateDir(target, candidate.StateDir); err != nil {
				_ = os.RemoveAll(target)
				_ = os.Rename(backup, target)
				return Result{}, err
			}
		default:
			_ = os.RemoveAll(target)
			_ = os.Rename(backup, target)
			return Result{}, fmt.Errorf("preserve plugin state directory: %w", err)
		}
	}
	rollback := func() {
		if stateMoved {
			_ = os.Rename(filepath.Join(target, candidate.StateDir), filepath.Join(backup, candidate.StateDir))
		}
		_ = os.RemoveAll(target)
		_ = os.Rename(backup, target)
	}
	if candidate.Executable == "" && previousManifest.Executable != "" {
		if err := os.Remove(previousManifest.Executable); err != nil && !os.IsNotExist(err) {
			rollback()
			return Result{}, fmt.Errorf("remove retired plugin executable: %w", err)
		}
	} else if executable != "" {
		if err := installFile(filepath.Join(target, candidate.Executable), executable, 0o700); err != nil {
			rollback()
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

func (m Manager) stage(candidate Candidate, preservedDatabases map[string]string) (string, error) {
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
	for name, expected := range candidate.Files {
		mode := os.FileMode(0o600)
		if name == candidate.Executable {
			mode = 0o700
		}
		if err := installChecksummedFile(filepath.Join(candidate.Directory, name),
			filepath.Join(staged, name), mode, name, expected); err != nil {
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
	for name, source := range preservedDatabases {
		if err := installFile(source, filepath.Join(staged, name), 0o600); err != nil {
			return failed(fmt.Errorf("preserve plugin database %s: %w", name, err))
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
		Version: candidate.Version, Checksum: candidate.Checksum, Kind: candidate.Kind, Risk: candidate.Risk,
		Custody: candidate.Custody, Database: candidate.Database, Executable: executable,
		ExecutableFile: candidate.Executable, StateDir: candidate.StateDir, Files: candidate.Files,
	}
	if databases := candidateDatabaseFiles(candidate); len(databases) > 1 {
		manifest.Database = ""
		manifest.Databases = databases
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
	if candidate.Kind != DataPackage || candidate.Risk != DataOnly || candidate.Executable != "" {
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
	if previous.Kind != DataPackage {
		return Result{}, fmt.Errorf(
			"plugin %s is not a data package; an in-place update is refused", candidate.Name)
	}
	previousDatabases := manifestDatabaseFiles(previous)
	candidateDatabases := candidateDatabaseFiles(candidate)
	if !sameDatabaseFiles(previousDatabases, candidateDatabases) {
		return Result{}, fmt.Errorf(
			"plugin %s changed its database files from %v to %v; update refused to protect data",
			candidate.Name, previousDatabases, candidateDatabases)
	}
	databaseSet := make(map[string]bool, len(candidateDatabases))
	for _, database := range candidateDatabases {
		databaseSet[database] = true
	}
	payload := make([]string, 0, len(candidate.Files))
	for name := range candidate.Files {
		if databaseSet[name] {
			continue
		}
		payload = append(payload, name)
		if err := installChecksummedFile(filepath.Join(candidate.Directory, name),
			filepath.Join(target, name), 0o600, name, candidate.Files[name]); err != nil {
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
		if _, kept := candidate.Files[name]; kept || slices.Contains(previousDatabases, name) {
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

// CandidateFromManifest describes an installed plugin the way Inspect describes
// a source, so a caller that only has the manifest asks the operator about the
// same package. Manifest.Executable is the installed path; the candidate names
// the payload file it came from.
func CandidateFromManifest(manifest Manifest, directory string) Candidate {
	return Candidate{
		Name: manifest.Name, Version: manifest.Version, Source: manifest.Source,
		Directory: directory, Checksum: manifest.Checksum, Risk: manifest.Risk,
		Custody: manifest.Custody, Database: manifest.Database, Databases: manifestDatabaseFiles(manifest),
		Executable: manifest.ExecutableFile, StateDir: manifest.StateDir, Kind: manifest.Kind, Files: manifest.Files,
	}
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
	if manifest.Kind == "" {
		manifest.Kind = DataPackage
	}
	databaseFiles := manifestDatabaseFiles(manifest)
	validDatabases := len(databaseFiles) > 0
	for _, database := range databaseFiles {
		validDatabases = validDatabases && safeFile(database)
	}
	validShape := manifest.Kind == DataPackage && validDatabases ||
		manifest.Kind == ExecutablePackage && len(databaseFiles) == 0 &&
			manifest.Risk == Executable && manifest.Executable != "" && safeFile(manifest.ExecutableFile) &&
			(manifest.StateDir == "" || safeFile(manifest.StateDir))
	for name := range manifest.Files {
		validShape = validShape && safeFile(name)
	}
	if manifest.Schema != manifestSchema || !safeName(manifest.Name) || manifest.Source == "" ||
		manifest.Version == "" || manifest.Checksum == "" || !validShape {
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
	databaseFiles := manifestDatabaseFiles(manifest)
	payload := append([]string{PackageFilename}, databaseFiles...)
	if manifest.Kind == DataPackage {
		// A data package whose plugin.json is not a federation manifest carries
		// its meaning in a separate semantic layer, and that file is as required
		// as the database it describes.
		federated, err := federatedPackage(directory)
		if err != nil {
			return Manifest{}, err
		}
		if !federated {
			payload = append(payload, plugin.SemanticFilename)
		}
	}
	if manifest.Kind == ExecutablePackage {
		payload = []string{PackageFilename, manifest.ExecutableFile}
	}
	for _, name := range payload {
		if _, declared := checksums[name]; !declared {
			return Manifest{}, fmt.Errorf("%s does not own required payload %s", ManifestFilename, name)
		}
	}
	immutable := maps.Clone(checksums)
	for _, database := range databaseFiles {
		delete(immutable, database)
	}
	if err := verifyChecksummedFiles(directory, immutable); err != nil {
		return Manifest{}, err
	}
	for _, database := range databaseFiles {
		if info, err := os.Lstat(filepath.Join(directory, database)); err != nil ||
			!info.Mode().IsRegular() {
			return Manifest{}, fmt.Errorf("installed database %s is not a regular file", database)
		}
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

// InstalledPaths expands only the paths an installed manifest declares. A
// writable state directory is a package-owned namespace, so its current tree
// is inventoried without following symlinks; unrelated siblings stay outside
// the purge plan.
func InstalledPaths(directory string, manifest Manifest) []string {
	paths := make([]string, 0, len(manifest.Files)+8)
	for name := range manifest.Files {
		paths = append(paths, filepath.Join(directory, name))
	}
	for _, name := range manifestDatabaseFiles(manifest) {
		database := filepath.Join(directory, name)
		paths = append(paths, database+"-wal", database+"-shm", database+"-journal")
	}
	if manifest.StateDir != "" {
		state := filepath.Join(directory, manifest.StateDir)
		_ = filepath.WalkDir(state, func(path string, _ os.DirEntry, err error) error {
			if err == nil {
				paths = append(paths, path)
			}
			return nil
		})
	}
	return append(paths, filepath.Join(directory, ManifestFilename),
		filepath.Join(directory, ChecksumsFilename), directory)
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
	input, err := openRegularSource(source)
	if err != nil {
		return fmt.Errorf("read %s: %w", source, err)
	}
	defer input.Close()
	return installOpenFile(input, destination, mode, "", "")
}

// installChecksummedFile writes a payload the source published a checksum for.
// The digest is taken from the bytes it copies out of this one open file rather
// than from a second look at the path, because the path is what an attacker can
// still change between the verification the consent screen showed and this copy:
// binding both to a single descriptor leaves no moment where the file checked
// and the file installed can differ.
func installChecksummedFile(source, destination string, mode os.FileMode, name, expected string) error {
	input, err := openRegularSource(source)
	if errors.Is(err, errSourceNotRegular) {
		return fmt.Errorf("checksum source file %s is not a regular file", name)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", source, err)
	}
	defer input.Close()
	return installOpenFile(input, destination, mode, name, expected)
}

func installOpenFile(input *os.File, destination string, mode os.FileMode, name, expected string) error {
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
	writer := io.Writer(temporary)
	hash := sha256.New()
	if expected != "" {
		writer = io.MultiWriter(temporary, hash)
	}
	if _, err := io.Copy(writer, input); err != nil {
		temporary.Close()
		return fmt.Errorf("write %s: %w", destination, err)
	}
	if expected != "" {
		digest := hex.EncodeToString(hash.Sum(nil))
		if digest != expected {
			temporary.Close()
			return fmt.Errorf("checksum mismatch for %s: source declares %s, file is %s",
				name, expected, digest)
		}
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
	return openFileChecksum(file, path)
}

func openFileChecksum(file *os.File, path string) (string, error) {
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
