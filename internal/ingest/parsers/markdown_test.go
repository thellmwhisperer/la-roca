package parsers

import "testing"

func TestClaudeMemoryReadsItsFrontmatter(t *testing.T) {
	content := "---\nname: the-dash-rule\ntype: feedback\ndescription: how to write\n---\n" +
		"Never use long dashes in the generated text.\n"
	records, err := Parse(KindClaudeMemory, []byte(content), FileMeta{
		Path:     "/w/.claude/projects/-w-demo/memory/dash.md",
		FileName: "dash.md",
		Project:  "demo",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records.Memories) != 1 {
		t.Fatalf("memories = %d, want 1", len(records.Memories))
	}
	memory := records.Memories[0]
	if memory.Layer != "feedback" {
		t.Errorf("layer = %q: it comes from the declared type", memory.Layer)
	}
	if memory.Content != "Never use long dashes in the generated text." {
		t.Errorf("content = %q", memory.Content)
	}
	if memory.Origin != "cron" || memory.SourceAgent != "claude-code" || memory.Project != "demo" {
		t.Errorf("memory = %+v", memory)
	}
	// This pair makes re-ingesting update rather than duplicate the memory.
	if memory.Metadata["_cron_source"] != "claude-code" ||
		memory.Metadata["file_path"] != "/w/.claude/projects/-w-demo/memory/dash.md" {
		t.Errorf("metadata = %+v", memory.Metadata)
	}
	if memory.Metadata["memory_name"] != "the-dash-rule" {
		t.Errorf("metadata = %+v", memory.Metadata)
	}
}

func TestMemoryWithoutFrontmatterIsAllBody(t *testing.T) {
	records, err := Parse(KindClaudeMemory, []byte("  a hand-written note  \n"), FileMeta{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	memory := records.Memories[0]
	if memory.Content != "a hand-written note" {
		t.Errorf("content = %q", memory.Content)
	}
	// No declared type: the layer is left for the ingest layer's registry to
	// resolve, which is the only place that knows what a valid layer is.
	if memory.Layer != "" {
		t.Errorf("layer = %q, want it undeclared", memory.Layer)
	}
}

func TestMemoryFrontmatterAcceptsCRLFAndQuotedScalars(t *testing.T) {
	file := ParseMemoryFile([]byte("---\r\nname: 'portable'\r\ntype: \"feedback\"\r\ndescription: 'quoted value'\r\n---\r\nbody\r\n"))
	if file.Name != "portable" || file.Type != "feedback" ||
		file.Description != "quoted value" || file.Body != "body" {
		t.Fatalf("frontmatter = %+v", file)
	}
}

func TestAnEmptyMemoryFileIsSkipped(t *testing.T) {
	for _, content := range []string{"", "   \n\n", "---\ntype: feedback\n---\n\n"} {
		records, err := Parse(KindClaudeMemory, []byte(content), FileMeta{})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(records.Memories) != 0 {
			t.Errorf("%q was not skipped: %+v", content, records)
		}
	}
}

func TestCodexFileLayerComesFromWhatKindOfFileItIs(t *testing.T) {
	cases := map[string]string{"memory": "feedback", "rule": "feedback"}
	for sourceType, layer := range cases {
		records, err := Parse(KindCodexFile, []byte("contenido"), FileMeta{
			Path:       "/w/.codex/" + sourceType + "/x",
			FileName:   "x",
			SourceType: sourceType,
		})
		if err != nil {
			t.Fatalf("parse %s: %v", sourceType, err)
		}
		memory := records.Memories[0]
		if memory.Layer != layer {
			t.Errorf("%s: layer = %q, want %q", sourceType, memory.Layer, layer)
		}
		if memory.SourceAgent != "codex" || memory.Metadata["_cron_source"] != "codex" {
			t.Errorf("%s: memory = %+v", sourceType, memory)
		}
		if memory.Metadata["source_type"] != sourceType {
			t.Errorf("%s: metadata = %+v", sourceType, memory.Metadata)
		}
	}
}
