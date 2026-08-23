package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// CompanionDeclaration is the optional session-owned child a plugin asks the
// MCP server to raise for the lifetime of that serve process. The executable
// is a single filename inside the plugin directory; args are passed directly
// to exec, never through a shell.
type CompanionDeclaration struct {
	Executable string   `json:"executable"`
	Args       []string `json:"args,omitempty"`
}

// SessionCompanion is one installed plugin's verified companion declaration,
// ready for a session parent to exec from the plugin directory.
type SessionCompanion struct {
	Plugin     string
	Directory  string
	Executable string
	Args       []string
}

func (c CompanionDeclaration) Valid() error {
	if !safeManifestFile(c.Executable) {
		return fmt.Errorf("%s has invalid companion executable %q", PackageFilename, c.Executable)
	}
	for _, argument := range c.Args {
		if strings.TrimSpace(argument) == "" || strings.ContainsRune(argument, 0) {
			return fmt.Errorf("%s has an empty companion argument", PackageFilename)
		}
	}
	return nil
}

type identityManifest struct {
	Schema    int                   `json:"schema"`
	Name      string                `json:"name"`
	Version   string                `json:"version"`
	Kind      string                `json:"kind,omitempty"`
	Custody   bool                  `json:"custody,omitempty"`
	StateDir  string                `json:"state_directory,omitempty"`
	Companion *CompanionDeclaration `json:"companion,omitempty"`
}

// SessionCompanions lists every installed plugin that declares a session
// companion. A malformed declaration is skipped with a warning; it never
// becomes a PATH lookup.
func SessionCompanions(root string) ([]SessionCompanion, []string) {
	names, warnings := pluginDirectories(root)
	var found []SessionCompanion
	for _, name := range names {
		spec, err := sessionCompanion(name, filepath.Join(root, name))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("plugin %s companion is unavailable", name))
			continue
		}
		if spec.Plugin != "" {
			found = append(found, spec)
		}
	}
	slices.SortFunc(found, func(a, b SessionCompanion) int {
		return strings.Compare(a.Plugin, b.Plugin)
	})
	return found, warnings
}

func sessionCompanion(name, directory string) (SessionCompanion, error) {
	raw, err := os.ReadFile(filepath.Join(directory, PackageFilename))
	if err != nil {
		if os.IsNotExist(err) {
			return SessionCompanion{}, nil
		}
		return SessionCompanion{}, err
	}
	federated, err := Federated(raw)
	if err != nil {
		return SessionCompanion{}, err
	}
	if federated {
		manifest, err := DecodeManifest(bytes.NewReader(raw))
		if err != nil {
			return SessionCompanion{}, err
		}
		if manifest.Name != name || manifest.Companion == nil {
			return SessionCompanion{}, nil
		}
		return SessionCompanion{
			Plugin: name, Directory: directory,
			Executable: manifest.Companion.Executable, Args: slices.Clone(manifest.Companion.Args),
		}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var identity identityManifest
	if err := decoder.Decode(&identity); err != nil {
		return SessionCompanion{}, err
	}
	if identity.Name != name || identity.Companion == nil {
		return SessionCompanion{}, nil
	}
	if err := identity.Companion.Valid(); err != nil {
		return SessionCompanion{}, err
	}
	return SessionCompanion{
		Plugin: name, Directory: directory,
		Executable: identity.Companion.Executable, Args: slices.Clone(identity.Companion.Args),
	}, nil
}
