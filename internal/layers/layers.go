// Package layers reads the layer registry, which travels embedded as data and
// not as code: adding a layer is editing a YAML, not touching Go.
package layers

import (
	"fmt"

	"github.com/thellmwhisperer/la-roca/data"
	"gopkg.in/yaml.v3"
)

// Layer is a memory layer declared in the registry.
type Layer struct {
	Name              string `yaml:"name" json:"name"`
	Description       string `yaml:"description" json:"description"`
	IngestAllowed     bool   `yaml:"ingest_allowed" json:"ingest_allowed"`
	IsCoordination    bool   `yaml:"is_coordination" json:"is_coordination"`
	SearchExcluded    bool   `yaml:"search_excluded" json:"search_excluded"`
	IsClassifierLabel bool   `yaml:"is_classifier_label" json:"is_classifier_label"`
	AliasOf           string `yaml:"alias_of" json:"alias_of,omitempty"`
	Deprecated        bool   `yaml:"deprecated" json:"deprecated"`
	AddedBy           string `yaml:"added_by" json:"added_by"`
	Lifecycle         string `yaml:"lifecycle" json:"lifecycle"`
	SinceVersion      string `yaml:"since_version" json:"since_version"`
}

// Registry is the whole registry, in the order it is declared in.
type Registry struct {
	Version int     `yaml:"version"`
	Layers  []Layer `yaml:"layers"`
}

// Load reads the embedded registry.
func Load() (Registry, error) {
	var registry Registry
	if err := yaml.Unmarshal(data.Layers, &registry); err != nil {
		return Registry{}, fmt.Errorf("read the embedded layer registry: %w", err)
	}
	if len(registry.Layers) == 0 {
		return Registry{}, fmt.Errorf("the embedded layer registry is empty")
	}
	return registry, nil
}

// Resolve turns a layer name declared by an artefact into the physical layer it
// lands in, following the aliases the registry declares.
//
// A name this registry does not know, or one it does not admit for ingest, falls
// to the given default. That is not laxity: a memory file whose frontmatter says
// `type: reference` is still a memory, and refusing it over a word the registry
// has not learned yet would lose the content to keep the vocabulary tidy.
func (r Registry) Resolve(name, fallback string) string {
	if resolved, ok := r.resolve(name); ok {
		return resolved
	}
	if resolved, ok := r.resolve(fallback); ok {
		return resolved
	}
	return fallback
}

// resolve follows the alias chain, bounded by the number of declared layers so a
// registry that ever declares a cycle degrades instead of hanging.
func (r Registry) resolve(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	byName := make(map[string]Layer, len(r.Layers))
	for _, layer := range r.Layers {
		byName[layer.Name] = layer
	}
	current, ok := byName[name]
	if !ok || !current.IngestAllowed {
		return "", false
	}
	for range len(r.Layers) {
		if current.AliasOf == "" {
			return current.Name, true
		}
		next, ok := byName[current.AliasOf]
		if !ok {
			return current.Name, true
		}
		current = next
	}
	return current.Name, true
}

// Coordination are the coordination layers as taxonomy. Search exclusion is
// SearchExcluded: handoff is coordination (session continuity) and still
// searchable; private messaging layers are both coordination and excluded.
func (r Registry) Coordination() []string {
	return r.filter(func(l Layer) bool { return l.IsCoordination })
}

// SearchExcluded are the layers term search and FTS omit so private messaging
// does not appear in a memory query. Handoff is not among them.
func (r Registry) SearchExcluded() []string {
	return r.filter(func(l Layer) bool { return l.SearchExcluded })
}

// ClassifierLabels are the layers that feed the classifier's labels.
func (r Registry) ClassifierLabels() []string {
	return r.filter(func(l Layer) bool { return l.IsClassifierLabel })
}

// Names are all the declared layers.
func (r Registry) Names() []string { return r.filter(func(Layer) bool { return true }) }

func (r Registry) filter(want func(Layer) bool) []string {
	var names []string
	for _, layer := range r.Layers {
		if want(layer) {
			names = append(names, layer.Name)
		}
	}
	return names
}
