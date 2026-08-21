package parsers

import (
	"strings"
	"testing"
)

func TestHermesMemorySplitsBlocksAndClassifiesByNature(t *testing.T) {
	content := strings.Join([]string{
		"The operator identity is a fictional cartographer based in the cobalt archipelago.",
		"Prefer short synthetic replies. Never invent a review finding.",
		"Run the invented compass check before launching a second probe.",
		"  ",
	}, "\n§\n")
	records, err := Parse(KindHermesMemory, []byte(content), FileMeta{
		Path: "/synthetic/home/.hermes/memories/MEMORY.md", FileName: "MEMORY.md",
		SourceAgent: "hermes",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ layer, snippet string }{
		{"user", "operator identity"},
		{"feedback", "Prefer short synthetic replies"},
		{"pattern", "invented compass check"},
	}
	if len(records.Memories) != len(want) {
		t.Fatalf("memories = %d, want %d: %+v", len(records.Memories), len(want), records)
	}
	for i, expect := range want {
		memory := records.Memories[i]
		if memory.Layer != expect.layer || !strings.Contains(memory.Content, expect.snippet) {
			t.Errorf("memory %d = %+v, want layer %s containing %q", i+1, memory, expect.layer, expect.snippet)
		}
		if memory.Origin != "agent" || memory.SourceAgent != "hermes" {
			t.Errorf("memory %d attribution = %+v", i+1, memory)
		}
		hash, _ := memory.Metadata["block_hash"].(string)
		if hash == "" || !strings.HasSuffix(memory.FilePath, "#block="+hash) {
			t.Errorf("memory %d identity = %+v", i+1, memory)
		}
		if memory.Metadata["aggregate_file_path"] != "/synthetic/home/.hermes/memories/MEMORY.md" {
			t.Errorf("memory %d provenance = %+v", i+1, memory.Metadata)
		}
	}
}
