package parsers

import (
	"os"
	"testing"
)

func TestCodexAggregateSplitsThreadBlocksWithStableIdentity(t *testing.T) {
	content, err := os.ReadFile("../testdata/codex_raw_memories.fixture")
	if err != nil {
		t.Fatal(err)
	}
	records, err := Parse(KindCodexMemoryAggregate, content, FileMeta{
		Path:       "/home/test/.codex/memories/raw_memories.md",
		FileName:   "raw_memories.md",
		SourceType: "memory",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Memories) != 2 {
		t.Fatalf("memories = %d, want 2: %+v", len(records.Memories), records)
	}
	for i, want := range []struct{ id, stamp, cwd string }{
		{"11111111-2222-3333-4444-555555555555", "2026-08-01T10:20:30Z", "/workspace/atlas"},
		{"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "2026-08-02T11:22:33.456Z", `C:\workspace\beacon`},
	} {
		memory := records.Memories[i]
		if memory.Layer != "feedback" || memory.CreatedAt != want.stamp {
			t.Errorf("memory %d classification/timestamp = %+v", i+1, memory)
		}
		if memory.Metadata["thread_id"] != want.id || memory.Metadata["cwd"] != want.cwd {
			t.Errorf("memory %d metadata = %+v", i+1, memory.Metadata)
		}
		if memory.Metadata["aggregate_file_path"] != "/home/test/.codex/memories/raw_memories.md" {
			t.Errorf("memory %d aggregate provenance = %+v", i+1, memory.Metadata)
		}
		if memory.FilePath == "/home/test/.codex/memories/raw_memories.md" ||
			memory.Metadata["file_path"] != memory.FilePath {
			t.Errorf("memory %d identity = %+v", i+1, memory)
		}
	}
	if records.Memories[0].Content[:len("## Thread")] != "## Thread" {
		t.Errorf("first block lost its heading: %q", records.Memories[0].Content)
	}
}
