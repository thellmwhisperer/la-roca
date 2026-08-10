package layers_test

import (
	"slices"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/layers"
)

func TestTheRegistryCarriesTheTwelveLayers(t *testing.T) {
	registry, err := layers.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{
		"user", "feedback", "project", "pattern", "pill", "discovery",
		"handoff", "handover", "question", "review", "issue", "protocol",
	}
	if len(registry.Layers) != len(want) {
		t.Fatalf("layers = %d, want %d", len(registry.Layers), len(want))
	}
	for i, name := range want {
		if registry.Layers[i].Name != name {
			t.Errorf("layer[%d] = %q, want %q", i, registry.Layers[i].Name, name)
		}
	}
}

// is_coordination is taxonomy: handoff is coordination (session continuity)
// and so are the messaging layers. Search exclusion is a different flag.
func TestTheCoordinationLayersAreTheFive(t *testing.T) {
	registry, err := layers.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"handoff", "handover", "issue", "question", "review"}
	got := registry.Coordination()
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("coordination = %v, want %v", got, want)
	}
}

// Private messaging is excluded from term search; handoff is session
// continuity (job J1) and must remain searchable.
func TestSearchExcludesMessagingNotHandoff(t *testing.T) {
	registry, err := layers.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"issue", "question", "review"}
	got := registry.SearchExcluded()
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("search excluded = %v, want %v", got, want)
	}
	for _, layer := range registry.Layers {
		if layer.Name == "handoff" || layer.Name == "handover" {
			if layer.SearchExcluded {
				t.Errorf("%s is search_excluded: handoffs must stay searchable", layer.Name)
			}
			if !layer.IsCoordination {
				t.Errorf("%s lost is_coordination taxonomy", layer.Name)
			}
		}
	}
}

func TestTheClassifierLabelsComeFromTheRegistry(t *testing.T) {
	registry, err := layers.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// "protocol" remains a classifier label even though it aliases "pattern";
	// the embedded registry is the classifier's source of truth.
	want := []string{
		"feedback", "pattern", "pill", "discovery", "handoff",
		"question", "review", "issue", "protocol",
	}
	got := registry.ClassifierLabels()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("classifier labels = %v, want %v", got, want)
	}
}

func TestResolveFollowsTheDeclaredAliases(t *testing.T) {
	registry, err := layers.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cases := map[string]string{
		"feedback": "feedback",
		// The registry declares handover as an alias of handoff and protocol as one
		// of pattern; an artefact that names either has to land in the physical one.
		"handover": "handoff",
		"protocol": "pattern",
		// A word the registry has not learned falls to the default instead of
		// losing the memory.
		"reference": "pattern",
		"":          "pattern",
	}
	for declared, want := range cases {
		if got := registry.Resolve(declared, "pattern"); got != want {
			t.Errorf("%q: %q, want %q", declared, got, want)
		}
	}
}
