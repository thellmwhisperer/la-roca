package agentcfg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

// Parent-container ownership is recorded at install time, never deduced from
// emptiness. Nested JSON runtimes (zcode) create mcp and hooks objects that
// may already have been empty when the operator owned them; uninstall prunes
// only the containers this install created, and only when they are empty again.
//
// Claims are the leaf values La Roca wrote, each as the digest of its
// normalized JSON. A key La Roca has no valid claim on is the operator's.
const ownedMarker = "owned-containers-v1"

type ownedContainers struct {
	Marker string            `json:"roca"`
	MCP    []string          `json:"mcp,omitempty"`
	Hooks  []string          `json:"hooks,omitempty"`
	Claims map[string]string `json:"claims,omitempty"`
}

func ownedSidecar(path string) string { return path + ".roca-owned" }

// LockOwned serializes one config's read-modify-write with its sidecar, so a
// concurrent MCP and hook install cannot overwrite each other's claims. A
// missing directory has nothing to race over unless the caller creates it.
func LockOwned(path string, create bool) (func() error, error) {
	dir := filepath.Dir(path)
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create the directory of %s: %w", path, err)
		}
	} else if _, err := os.Stat(dir); os.IsNotExist(err) {
		return func() error { return nil }, nil
	}
	return securefile.Lock(ownedSidecar(path) + ".lock")
}

// ValueDigest is the claim recorded for one value La Roca wrote.
func ValueDigest(value any) string {
	encoded, _ := json.Marshal(value) // map keys sorted, no whitespace
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func BytesDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// owns answers whether the value at key is still the one La Roca wrote. A
// legacy sidecar (containers, no digest) owns it only while it is what this
// version writes, the command compared by basename: the absolute path that
// install wrote was never recorded.
func (o ownedContainers) owns(r runtime, key string, value any, present bool) bool {
	if !present {
		return false
	}
	if digest, ok := o.Claims[key]; ok {
		return digest == ValueDigest(value)
	}
	entry, ok := value.(map[string]any)
	if len(o.MCP) == 0 || !ok {
		return false
	}
	command, _ := entry["command"].(string)
	entry = maps.Clone(entry)
	entry["command"] = filepath.Base(command)
	want := map[string]any{}
	for _, f := range r.entry("roca") {
		want[f.key] = f.value
	}
	return ValueDigest(entry) == ValueDigest(want)
}

func loadOwned(path string) (ownedContainers, error) {
	body, err := os.ReadFile(ownedSidecar(path))
	if os.IsNotExist(err) {
		return ownedContainers{Marker: ownedMarker}, nil
	}
	if err != nil {
		return ownedContainers{}, err
	}
	var owned ownedContainers
	if err := json.Unmarshal(body, &owned); err != nil || owned.Marker != ownedMarker {
		return ownedContainers{}, fmt.Errorf("refuse to overwrite unrecognized ownership file %s", ownedSidecar(path))
	}
	return owned, nil
}

func writeOwned(path string, owned ownedContainers) error {
	owned.Marker = ownedMarker
	if len(owned.MCP) == 0 && len(owned.Hooks) == 0 && len(owned.Claims) == 0 {
		return removeOwned(path)
	}
	body, err := json.MarshalIndent(owned, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	sidecar := ownedSidecar(path)
	previous, err := os.ReadFile(sidecar)
	if os.IsNotExist(err) {
		return securefile.CreatePreservingParentMode(sidecar, body, 0o600, 0o700)
	}
	if err != nil || string(previous) == string(body) {
		return err
	}
	return securefile.Replace(sidecar, body, previous)
}

func removeOwned(path string) error {
	sidecar := ownedSidecar(path)
	if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", sidecar, err)
	}
	return nil
}

// SaveOwnedHooks records the hook containers an install created and the
// values it claims, keyed by path; a nil value drops that key's claim.
func SaveOwnedHooks(path string, created []string, claims map[string]any) error {
	owned, err := loadOwned(path)
	if err != nil {
		return err
	}
	owned.Hooks = mergeOwned(owned.Hooks, created)
	for key, value := range claims {
		if value == nil {
			delete(owned.Claims, key)
			continue
		}
		if owned.Claims == nil {
			owned.Claims = map[string]string{}
		}
		owned.Claims[key] = ValueDigest(value)
	}
	return writeOwned(path, owned)
}

func SaveOwnedHookFile(path, key string, body []byte) error {
	owned, err := loadOwned(path)
	if err != nil {
		return err
	}
	if owned.Claims == nil {
		owned.Claims = map[string]string{}
	}
	owned.Claims[key] = BytesDigest(body)
	return writeOwned(path, owned)
}

func ClearOwnedHookFile(path, key string) error {
	owned, err := loadOwned(path)
	if err != nil {
		return err
	}
	delete(owned.Claims, key)
	return writeOwned(path, owned)
}

func mergeOwned(existing, created []string) []string {
	merged := make([]string, 0, len(existing)+len(created))
	seen := make(map[string]struct{}, len(existing)+len(created))
	for _, path := range append(append([]string{}, existing...), created...) {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		merged = append(merged, path)
	}
	return merged
}

func ClearOwnedHooks(path string) error {
	owned, err := loadOwned(path)
	if err != nil {
		return err
	}
	owned.Hooks = nil
	for key := range owned.Claims {
		if strings.HasPrefix(key, "hooks.") {
			delete(owned.Claims, key)
		}
	}
	return writeOwned(path, owned)
}

// LoadOwnedHooks returns the hook containers La Roca created and every claim
// digest the sidecar holds.
func LoadOwnedHooks(path string) ([]string, map[string]string, error) {
	owned, err := loadOwned(path)
	if err != nil {
		return nil, nil, err
	}
	return owned.Hooks, owned.Claims, nil
}
