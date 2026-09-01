package agentcfg

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

// Parent-container ownership is recorded at install time, never deduced from
// emptiness. Nested JSON runtimes (zcode) create mcp and hooks objects that
// may already have been empty when the operator owned them; uninstall prunes
// only the containers this install created, and only when they are empty again.
const ownedMarker = "owned-containers-v1"

type ownedContainers struct {
	Marker string   `json:"roca"`
	MCP    []string `json:"mcp,omitempty"`
	Hooks  []string `json:"hooks,omitempty"`
}

func ownedSidecar(path string) string { return path + ".roca-owned" }

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
	if len(owned.MCP) == 0 && len(owned.Hooks) == 0 {
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
		return os.WriteFile(sidecar, body, 0o600)
	}
	if err != nil {
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

func saveOwnedMCP(path string, created []string) error {
	owned, err := loadOwned(path)
	if err != nil {
		return err
	}
	owned.MCP = created
	return writeOwned(path, owned)
}

func SaveOwnedHooks(path string, created []string) error {
	owned, err := loadOwned(path)
	if err != nil {
		return err
	}
	owned.Hooks = created
	return writeOwned(path, owned)
}

func clearOwnedMCP(path string) error {
	owned, err := loadOwned(path)
	if err != nil {
		return err
	}
	owned.MCP = nil
	return writeOwned(path, owned)
}

func ClearOwnedHooks(path string) error {
	owned, err := loadOwned(path)
	if err != nil {
		return err
	}
	owned.Hooks = nil
	return writeOwned(path, owned)
}

func loadOwnedMCP(path string) ([]string, error) {
	owned, err := loadOwned(path)
	if err != nil {
		return nil, err
	}
	return owned.MCP, nil
}

func LoadOwnedHooks(path string) ([]string, error) {
	owned, err := loadOwned(path)
	if err != nil {
		return nil, err
	}
	return owned.Hooks, nil
}
