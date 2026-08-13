// Package artifact owns the versioned agent-facing files La Roca installs.
package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

const (
	RegistrySchema   = 1
	SystemBegin      = "<!-- ROCA SYSTEM BEGIN -->"
	SystemEnd        = "<!-- ROCA SYSTEM END -->"
	UserBegin        = "<!-- ROCA USER BEGIN -->"
	UserEnd          = "<!-- ROCA USER END -->"
	frontmatter      = "---\n"
	frontmatterBegin = "# ROCA SYSTEM BEGIN"
)

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
	userEnd := strings.Index(content, UserEnd)
	if systemEnd < 0 || userStart < 0 || userEnd < 0 ||
		systemEnd >= userStart || userStart >= userEnd || userEnd+len(UserEnd) > len(content) {
		return Zones{}, fmt.Errorf("artifact does not contain one ordered SYSTEM and USER zone")
	}
	userStart += len(UserBegin) + 1
	if trailing := content[userEnd+len(UserEnd):]; trailing != "" && trailing != "\n" {
		return Zones{}, fmt.Errorf("artifact has content outside its SYSTEM and USER zones")
	}
	return Zones{System: prefix + content[systemStart:systemEnd], User: content[userStart:userEnd]}, nil
}

func Checksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

type FileRequest struct {
	Path                 string
	System               string
	LegacySystems        []string
	PreviousSystemSHA256 string
	Enabled              bool
	Force                bool
}

type FileOutcome struct {
	Path         string `json:"path"`
	Changed      bool   `json:"changed"`
	Outdated     bool   `json:"outdated"`
	Diverged     bool   `json:"diverged"`
	Adopted      bool   `json:"adopted"`
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
			if out.Diverged && !request.Force {
				return out, nil
			}
			next = Zoned(request.System, zones.User)
		} else {
			if strings.Contains(current, "<!-- ROCA ") || strings.Contains(current, frontmatterBegin) {
				return out, fmt.Errorf("read zones from %s: %w", request.Path, parseErr)
			}
			out.Outdated = true
			recognized := false
			for _, shipped := range request.LegacySystems {
				if current == shipped {
					recognized = true
					break
				}
			}
			if !recognized {
				next = Zoned(request.System, current)
			}
			out.Adopted = request.Enabled
		}
	} else {
		out.Outdated = true
		out.Diverged = request.PreviousSystemSHA256 != ""
		if out.Diverged && !request.Force {
			return out, nil
		}
	}
	if !request.Enabled {
		return out, nil
	}
	changed, backup, err := replaceFile(request.Path, next, previous)
	if err != nil {
		return out, err
	}
	out.Changed = changed
	out.Backup = backup
	out.Outdated = false
	out.Diverged = false
	return out, nil
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
	Kind             string `json:"kind"`
	Runtime          string `json:"runtime,omitempty"`
	Path             string `json:"path"`
	InstalledVersion string `json:"installed_version,omitempty"`
	AvailableVersion string `json:"available_version,omitempty"`
	SystemSHA256     string `json:"system_sha256,omitempty"`
	Format           string `json:"format,omitempty"`
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
	if registry.Schema != RegistrySchema {
		return Registry{}, fmt.Errorf("artifact registry %s has schema %d, want %d",
			path, registry.Schema, RegistrySchema)
	}
	if registry.Entries == nil {
		registry.Entries = []Entry{}
	}
	return registry, nil
}

func SaveRegistry(path string, registry Registry) error {
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
	owned := []string{registryPath}
	for _, entry := range registry.Entries {
		if entry.Kind == "hook" {
			continue
		}
		body, err := os.ReadFile(entry.Path)
		if err != nil {
			continue
		}
		zones, err := Parse(string(body))
		if err == nil && zones.User == "" && Checksum(zones.System) == entry.SystemSHA256 {
			owned = append(owned, entry.Path)
		}
	}
	sort.Strings(owned)
	return owned, nil
}
