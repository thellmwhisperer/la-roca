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
	RidesFilename = "rides.toml"
	DefaultTrain  = "nightly"
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
	return found, err
}

func readRides(pluginName, path string) ([]Ride, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var document rideDocument
	metadata, err := toml.Decode(string(raw), &document)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", RidesFilename, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("parse %s: unknown field %s", RidesFilename, undecoded[0])
	}
	if len(document.Rides) == 0 {
		return nil, fmt.Errorf("%s declares no rides", RidesFilename)
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
				RidesFilename, name)
		}
		if gated && dependency != "ingest" {
			if _, declared := document.Rides[dependency]; !declared {
				return nil, fmt.Errorf(
					"%s ride %q gate %q does not resolve to a ride in the same plugin",
					RidesFilename, name, gate)
			}
		}
		rides = append(rides, Ride{
			Name: name, Plugin: pluginName, Train: train, Command: command, Gate: gate,
		})
	}
	slices.SortFunc(rides, func(a, b Ride) int { return strings.Compare(a.Name, b.Name) })
	return rides, nil
}
