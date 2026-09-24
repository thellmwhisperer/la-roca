package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	RidesFilename  = "rides.toml"
	DefaultTrain   = "nightly"
	OperatorPlugin = "operator"
)

type Ride struct {
	Name    string `json:"ride"`
	Plugin  string `json:"plugin"`
	Train   string `json:"train"`
	Command string `json:"command"`
	Gate    string `json:"gate,omitempty"`
}

type RideVerifier func(pluginName, directory string) error

type rideDocument struct {
	Rides map[string]rideConfig `toml:"ride"`
}

type rideConfig struct {
	Train   string `toml:"train"`
	Command string `toml:"command"`
	Gate    string `toml:"gate"`
}

// DiscoverRides reads the optional ride manifest from every installed plugin.
// A bad plugin is reported without hiding the valid rides beside it.
func DiscoverRides(root string, verify RideVerifier) ([]Ride, []string) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) || strings.TrimSpace(root) == "" {
		return nil, nil
	}
	if err != nil {
		return nil, []string{fmt.Sprintf("plugin rides could not be discovered: %v", err)}
	}

	var rides []Ride
	var warnings []string
	for _, entry := range entries {
		if !entry.IsDir() || !validPluginName(entry.Name()) {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		if verify == nil {
			warnings = append(warnings,
				fmt.Sprintf("plugin %s rides cannot be trusted without installer verification", entry.Name()))
			continue
		}
		if err := verify(entry.Name(), directory); err != nil {
			warnings = append(warnings,
				fmt.Sprintf("plugin %s rides are not from a verified installation: %v", entry.Name(), err))
			continue
		}
		found, err := InspectRides(entry.Name(), directory)
		if err != nil {
			warnings = append(warnings,
				fmt.Sprintf("plugin %s has no usable %s: %v", entry.Name(), RidesFilename, err))
			continue
		}
		rides = append(rides, found...)
	}
	slices.SortFunc(rides, func(a, b Ride) int {
		if byPlugin := strings.Compare(a.Plugin, b.Plugin); byPlugin != 0 {
			return byPlugin
		}
		return strings.Compare(a.Name, b.Name)
	})
	return rides, warnings
}

// InspectRides validates one optional ride manifest. A plugin without one has
// no scheduled work and is not an error.
func InspectRides(pluginName, directory string) ([]Ride, error) {
	found, err := readRides(pluginName, filepath.Join(directory, RidesFilename))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err == nil {
		err = validateRideDependencies(filepath.Join(directory, RidesFilename), found)
	}
	return found, err
}

func readRides(pluginName, path string) ([]Ride, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseRideSource(pluginName, path, raw, false)
}

// DiscoverOperatorRides reads operator-owned ride tables from config.toml and
// rides.d.
func DiscoverOperatorRides(configPath, ridesDir string) ([]Ride, []string, error) {
	byName := map[string]Ride{}
	sourceOf := map[string]string{}
	var warnings []string

	if directory := strings.TrimSpace(ridesDir); directory != "" {
		entries, err := os.ReadDir(directory)
		if err != nil && !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf(
				"operator rides in %s could not be read: %w", directory, err)
		} else if err == nil {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				name := entry.Name()
				if entry.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".toml") {
					continue
				}
				names = append(names, name)
			}
			slices.Sort(names)
			for _, name := range names {
				path := filepath.Join(directory, name)
				found, err := readOperatorRides(OperatorPlugin, path, false)
				if err != nil {
					return nil, warnings, fmt.Errorf(
						"operator ride file %s is unusable: %w", name, err)
				}
				for _, ride := range found {
					if previous, ok := sourceOf[ride.Name]; ok {
						return nil, warnings, fmt.Errorf(
							"duplicate operator ride %q declared in %s and %s",
							ride.Name, previous, name)
					}
					byName[ride.Name] = ride
					sourceOf[ride.Name] = name
				}
			}
		}
	}

	if path := strings.TrimSpace(configPath); path != "" {
		found, err := readOperatorRides(OperatorPlugin, path, true)
		if err != nil {
			return nil, warnings, fmt.Errorf(
				"operator rides in %s are unusable: %w", path, err)
		} else {
			label := filepath.Base(path)
			for _, ride := range found {
				if previous, ok := sourceOf[ride.Name]; ok {
					return nil, warnings, fmt.Errorf(
						"duplicate operator ride %q declared in %s and %s",
						ride.Name, previous, label)
				}
				byName[ride.Name] = ride
				sourceOf[ride.Name] = label
			}
		}
	}

	rides := make([]Ride, 0, len(byName))
	for _, ride := range byName {
		rides = append(rides, ride)
	}
	source := strings.TrimSpace(ridesDir)
	if source == "" {
		source = strings.TrimSpace(configPath)
	}
	if err := validateRideDependencies(source, rides); err != nil {
		return nil, warnings, err
	}
	slices.SortFunc(rides, func(a, b Ride) int { return strings.Compare(a.Name, b.Name) })
	return rides, warnings, nil
}

func readOperatorRides(pluginName, path string, configFile bool) ([]Ride, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseRideSource(pluginName, path, raw, configFile)
}

func parseRideSource(pluginName, source string, raw []byte, configFile bool) ([]Ride, error) {
	var document rideDocument
	metadata, err := toml.Decode(string(raw), &document)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	if configFile {
		for _, key := range metadata.Undecoded() {
			if strings.HasPrefix(key.String(), "ride.") {
				return nil, fmt.Errorf("parse %s: unknown field %s", source, key)
			}
		}
	} else if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("parse %s: unknown field %s", source, undecoded[0])
	}
	if len(document.Rides) == 0 {
		if configFile {
			return nil, nil
		}
		return nil, fmt.Errorf("%s declares no rides", source)
	}

	rides := make([]Ride, 0, len(document.Rides))
	for name, config := range document.Rides {
		train := strings.TrimSpace(config.Train)
		if train == "" {
			train = DefaultTrain
		}
		command := strings.TrimSpace(config.Command)
		gate := strings.TrimSpace(config.Gate)
		dependency, gated := strings.CutPrefix(gate, "after_")
		if !validIdentifier(name) || !validIdentifier(train) || command == "" ||
			(gate != "" && (!gated || !validIdentifier(dependency))) {
			return nil, fmt.Errorf(
				"%s ride %q needs safe ride, train, and gate names plus a command",
				source, name)
		}
		rides = append(rides, Ride{
			Name: name, Plugin: pluginName, Train: train, Command: command, Gate: gate,
		})
	}
	slices.SortFunc(rides, func(a, b Ride) int { return strings.Compare(a.Name, b.Name) })
	return rides, nil
}

func validateRideDependencies(source string, rides []Ride) error {
	declared := make(map[string]struct{}, len(rides))
	for _, ride := range rides {
		declared[ride.Name] = struct{}{}
	}
	for _, ride := range rides {
		dependency, gated := strings.CutPrefix(ride.Gate, "after_")
		if gated && dependency != "ingest" {
			if _, ok := declared[dependency]; !ok {
				return fmt.Errorf(
					"%s ride %q gate %q does not resolve to a ride in the same plugin",
					source, ride.Name, ride.Gate)
			}
		}
	}
	return nil
}
