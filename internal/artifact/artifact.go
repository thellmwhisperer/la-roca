// Package artifact owns the versioned agent-facing files La Roca installs.
package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

const (
	RegistrySchema   = 3
	SystemBegin      = "<!-- ROCA SYSTEM BEGIN -->"
	SystemEnd        = "<!-- ROCA SYSTEM END -->"
	UserBegin        = "<!-- ROCA USER BEGIN -->"
	UserEnd          = "<!-- ROCA USER END -->"
	frontmatter      = "---\n"
	frontmatterBegin = "# ROCA SYSTEM BEGIN"
)

// ErrBrokenZones is the one refresh failure force repairs: the markers are
// there and no zone can be read from between them. Every other failure — a
// permission, a disk, a concurrent-edit refusal — is told apart from it,
// because offering force for those is either useless or the clobber the
// refusal exists to prevent.
var ErrBrokenZones = errors.New("zone markers are broken")

type Zones struct {
	System string
	User   string
}

func Zoned(system, user string) string {
	prefix, begin := "", SystemBegin
	if strings.HasPrefix(system, frontmatter) {
		prefix, begin = frontmatter, frontmatterBegin
		system = strings.TrimPrefix(system, frontmatter)
	}
	return prefix + begin + "\n" + system + SystemEnd + "\n" +
		UserBegin + "\n" + user + UserEnd + "\n"
}

func Parse(content string) (Zones, error) {
	prefix, begin := "", SystemBegin
	if strings.HasPrefix(content, frontmatter+frontmatterBegin+"\n") {
		prefix, begin = frontmatter, frontmatterBegin
	}
	if !strings.HasPrefix(content, prefix+begin+"\n") {
		return Zones{}, fmt.Errorf("artifact does not contain one ordered SYSTEM and USER zone")
	}
	systemStart := len(prefix) + len(begin) + 1
	systemEnd := strings.Index(content[systemStart:], SystemEnd+"\n")
	if systemEnd >= 0 {
		systemEnd += systemStart
	}
	userStart := strings.Index(content, UserBegin+"\n")
	// Zoned always writes the closing marker last, so the last one is the zone's
	// end: an operator who quoted the marker inside their own lines still gets
	// their file read back the way it was written.
	userEnd := strings.LastIndex(content, UserEnd)
	if systemEnd < 0 || userStart < 0 || userEnd < 0 {
		return Zones{}, fmt.Errorf("artifact does not contain one ordered SYSTEM and USER zone")
	}
	expectedUserStart := systemEnd + len(SystemEnd) + 1
	if userStart != expectedUserStart || userStart >= userEnd ||
		userEnd+len(UserEnd) > len(content) {
		return Zones{}, fmt.Errorf("artifact does not contain one ordered SYSTEM and USER zone")
	}
	userStart += len(UserBegin) + 1
	if trailing := content[userEnd+len(UserEnd):]; trailing != "" && trailing != "\n" {
		return Zones{}, fmt.Errorf("artifact has content outside its SYSTEM and USER zones")
	}
	return Zones{System: prefix + content[systemStart:systemEnd], User: content[userStart:userEnd]}, nil
}

// ParseFile reads one artifact from disk and returns its zones. Every caller
// that holds a path rather than bytes reads and parses in the same breath, so
// the pair has one owner here instead of a copy on each side.
func ParseFile(path string) (Zones, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Zones{}, err
	}
	return Parse(string(body))
}

func Checksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

type FileRequest struct {
	Path   string
	System string
	// LegacySignature opens every version of this artifact the product has ever
	// shipped. A pre-zone file that carries it is this product's own text from an
	// older release, whatever bytes followed; anything else is the operator's.
	LegacySignature      string
	PreviousSystemSHA256 string
	Enabled              bool
	Force                bool
	// RestoreMissing writes a registered artifact that is no longer on disk
	// instead of refusing it. An install the operator asked for by name carries
	// that consent; an automatic refresh does not, and a deletion it finds is a
	// withdrawal it leaves alone.
	RestoreMissing bool
}

type FileOutcome struct {
	Path     string `json:"path"`
	Changed  bool   `json:"changed"`
	Outdated bool   `json:"outdated"`
	Diverged bool   `json:"diverged"`
	Adopted  bool   `json:"adopted"`
	Missing  bool   `json:"missing,omitempty"`
	// Unregistered is a divergence with no registry record behind it: the zones
	// are intact and nothing says this product wrote them, which is not the same
	// sentence as an operator having edited the SYSTEM zone.
	Unregistered bool   `json:"unregistered,omitempty"`
	Backup       string `json:"backup,omitempty"`
	SystemSHA256 string `json:"system_sha256,omitempty"`
}

func RefreshFile(request FileRequest) (FileOutcome, error) {
	out := FileOutcome{Path: request.Path, SystemSHA256: Checksum(request.System)}
	previous, err := os.ReadFile(request.Path)
	missing := os.IsNotExist(err)
	if err != nil && !missing {
		return out, fmt.Errorf("read %s: %w", request.Path, err)
	}
	current := string(previous)
	next := Zoned(request.System, "")
	if !missing {
		zones, parseErr := Parse(current)
		if parseErr == nil {
			currentChecksum := Checksum(zones.System)
			out.Outdated = currentChecksum != out.SystemSHA256
			out.Diverged = currentChecksum != request.PreviousSystemSHA256 &&
				(request.PreviousSystemSHA256 != "" || out.Outdated)
			out.Unregistered = out.Diverged && request.PreviousSystemSHA256 == ""
			if out.Diverged && !request.Force {
				return out, nil
			}
			next = Zoned(request.System, zones.User)
		} else if markersArePresent(current) {
			// Markers that are there but broken are the one state no zone can be
			// read from, so nothing can be transplanted. Force is the documented
			// remedy for a broken artifact and it must reach this file too: the
			// replaced bytes survive in the backup replaceFile writes.
			if !request.Force {
				return out, fmt.Errorf("read zones from %s: %w: %w", request.Path, ErrBrokenZones, parseErr)
			}
			out.Outdated = true
		} else {
			out.Outdated = true
			next = legacyZoned(current, request)
			out.Adopted = request.Enabled
		}
	} else {
		out.Missing = true
		out.Outdated = true
		out.Diverged = request.PreviousSystemSHA256 != "" && !request.RestoreMissing
		if out.Diverged && !request.Force {
			return out, nil
		}
	}
	if !request.Enabled {
		return out, nil
	}
	changed, backup, err := replaceFile(request.Path, next, previous)
	// The recovery copy is reported even when the write that followed it failed:
	// it holds the operator's file and naming it is the whole point of making it.
	out.Backup = backup
	if err != nil {
		return out, err
	}
	out.Changed = changed
	out.Missing = false
	out.Outdated = false
	out.Diverged = false
	out.Unregistered = false
	return out, nil
}

func markersArePresent(content string) bool {
	return strings.Contains(content, "<!-- ROCA ") || strings.Contains(content, frontmatterBegin)
}

// legacyZoned decides what a pre-zone file becomes. Text this product
// recognizes as its own earlier shipped artifact is replaced outright, because
// keeping it would preserve a stale copy of the product beside the current one
// forever. Everything else is the operator's and moves into USER verbatim.
func legacyZoned(current string, request FileRequest) string {
	if request.LegacySignature != "" && strings.HasPrefix(current, request.LegacySignature) {
		return Zoned(request.System, "")
	}
	return Zoned(request.System, current)
}

func replaceFile(path, next string, previous []byte) (bool, string, error) {
	if next == string(previous) {
		return false, "", nil
	}
	backup := ""
	if previous != nil {
		var err error
		backup, err = securefile.BackUp(path, previous)
		if err != nil {
			return false, "", err
		}
	}
	if err := securefile.Replace(path, []byte(next), previous); err != nil {
		return false, backup, fmt.Errorf("write %s: %w", path, err)
	}
	return true, backup, nil
}

type Entry struct {
	Kind                string `json:"kind"`
	Runtime             string `json:"runtime,omitempty"`
	Path                string `json:"path"`
	MutationPath        string `json:"mutation_path,omitempty"`
	InstalledVersion    string `json:"installed_version,omitempty"`
	AvailableVersion    string `json:"available_version,omitempty"`
	SystemSHA256        string `json:"system_sha256,omitempty"`
	Format              string `json:"format,omitempty"`
	Executable          string `json:"executable,omitempty"`
	CreatedRoot         bool   `json:"created_root,omitempty"`
	RootIdentity        string `json:"root_identity,omitempty"`
	CreatedConfigDir    bool   `json:"created_config_dir,omitempty"`
	CreatedHooksDir     bool   `json:"created_hooks_dir,omitempty"`
	CreatedConfig       bool   `json:"created_config,omitempty"`
	CreatedLock         bool   `json:"created_lock,omitempty"`
	CreatedHooksEnabled bool   `json:"created_hooks_enabled,omitempty"`
}

func (entry Entry) Key() string {
	return entry.Kind + "\x00" + entry.Runtime + "\x00" + entry.Path
}

type Registry struct {
	Schema  int     `json:"schema"`
	Entries []Entry `json:"artifacts"`
}

func LoadRegistry(path string) (Registry, error) {
	registry := Registry{Schema: RegistrySchema, Entries: []Entry{}}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return registry, nil
	}
	if err != nil {
		return Registry{}, fmt.Errorf("read artifact registry %s: %w", path, err)
	}
	if err := json.Unmarshal(body, &registry); err != nil {
		return Registry{}, fmt.Errorf("read artifact registry %s: %w", path, err)
	}
	if registry.Schema != 1 && registry.Schema != 2 && registry.Schema != RegistrySchema {
		return Registry{}, fmt.Errorf("artifact registry %s has schema %d, want 1, 2, or %d",
			path, registry.Schema, RegistrySchema)
	}
	registry.Schema = RegistrySchema
	if registry.Entries == nil {
		registry.Entries = []Entry{}
	}
	return registry, nil
}

func SaveRegistry(path string, registry Registry) error {
	if previous, err := os.ReadFile(path); err == nil {
		var header struct {
			Schema int `json:"schema"`
		}
		if err := json.Unmarshal(previous, &header); err != nil {
			return fmt.Errorf("read artifact registry %s: %w", path, err)
		}
		switch header.Schema {
		case 1, 2:
			if _, err := securefile.BackUp(path, previous); err != nil {
				return fmt.Errorf("back up artifact registry migration: %w", err)
			}
		case RegistrySchema:
		default:
			return fmt.Errorf("artifact registry %s has schema %d, refusing to overwrite with %d",
				path, header.Schema, RegistrySchema)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read artifact registry %s: %w", path, err)
	}
	registry.Schema = RegistrySchema
	sort.Slice(registry.Entries, func(i, j int) bool {
		return registry.Entries[i].Key() < registry.Entries[j].Key()
	})
	body, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("encode artifact registry: %w", err)
	}
	body = append(body, '\n')
	if err := securefile.Write(path, body, 0o600, 0o700); err != nil {
		return fmt.Errorf("write artifact registry: %w", err)
	}
	return nil
}

func (registry *Registry) Upsert(entry Entry) {
	for index := range registry.Entries {
		if registry.Entries[index].Key() == entry.Key() {
			registry.Entries[index] = entry
			return
		}
	}
	registry.Entries = append(registry.Entries, entry)
}

func (registry *Registry) RemoveKinds(kinds ...string) {
	removed := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		removed[kind] = true
	}
	kept := registry.Entries[:0]
	for _, entry := range registry.Entries {
		if !removed[entry.Kind] {
			kept = append(kept, entry)
		}
	}
	registry.Entries = kept
}

func (registry Registry) Find(kind, runtime, path string) (Entry, bool) {
	key := (Entry{Kind: kind, Runtime: runtime, Path: path}).Key()
	for _, entry := range registry.Entries {
		if entry.Key() == key {
			return entry, true
		}
	}
	return Entry{}, false
}

func OwnedPaths(registryPath string) ([]string, error) {
	registry, err := LoadRegistry(registryPath)
	if err != nil {
		return nil, err
	}
	owned := []string{registryPath, registryPath + ".lock", registryPath + ".mcp.lock", registryPath + ".hooks.lock", registryPath + ".zcode.lock"}
	for _, entry := range registry.Entries {
		if entry.Kind == "hook" {
			continue
		}
		zones, err := ParseFile(entry.Path)
		if err == nil && zones.User == "" && Checksum(zones.System) == entry.SystemSHA256 {
			owned = append(owned, entry.Path)
		}
	}
	sort.Strings(owned)
	return owned, nil
}
