package parsers

import (
	"maps"
	"regexp"
	"strings"
)

// The two memory sources of the matrix that are plain text: Claude memory files
// and Codex memories and rules.

// codexTypeToLayer maps Codex memory types to semantic layers.
var codexTypeToLayer = map[string]string{
	"memory": "feedback",
	"rule":   "feedback",
}

// frontmatter is the `---` block a memory file opens with.
var frontmatter = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n(.*)\z`)

// MemoryFile is a memory file already split into its declared header and body.
type MemoryFile struct {
	Name        string
	Type        string
	Description string
	Body        string
}

// ParseMemoryFile splits the frontmatter from the body. A file with no
// frontmatter is all body: that is a memory somebody wrote by hand, and it is
// not thrown away for lacking a header.
func ParseMemoryFile(content []byte) MemoryFile {
	text := string(content)
	match := frontmatter.FindStringSubmatch(text)
	if match == nil {
		return MemoryFile{Body: strings.TrimSpace(text)}
	}
	file := MemoryFile{Body: strings.TrimSpace(match[2])}
	for _, line := range strings.Split(match[1], "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		switch key {
		case "name":
			file.Name = value
		case "type":
			file.Type = value
		case "description":
			file.Description = value
		}
	}
	return file
}

// memoryRecord is the row both of them produce: the same eight fields,
// differing only in the layer it lands in, who wrote it and what its own
// metadata declares. The `_cron_source` and `file_path` pair travels inside the
// metadata as well as beside it, preserving identity across re-ingests.
func memoryRecord(source, layer, body string, meta FileMeta, declared map[string]any) Records {
	metadata := map[string]any{
		"_cron_source": source,
		"file_path":    meta.Path,
		"file_name":    meta.FileName,
	}
	maps.Copy(metadata, declared)
	return Records{Memories: []Memory{{
		Layer:       layer,
		Content:     body,
		Origin:      "cron",
		SourceAgent: source,
		Project:     meta.Project,
		Metadata:    metadata,
		Source:      source,
		FilePath:    meta.Path,
	}}}
}

// ParseClaudeMemory turns a per-project memory file into one memory. The layer
// comes from the file's declared `type`; what the registry does not know about
// falls to the default, which is the ingest layer's decision and not this one's.
func ParseClaudeMemory(content []byte, meta FileMeta) (Records, error) {
	file := ParseMemoryFile(content)
	if file.Body == "" {
		return Records{}, nil
	}
	declared := map[string]any{}
	putIfSet(declared, "memory_name", file.Name)
	putIfSet(declared, "memory_description", file.Description)
	return memoryRecord("claude-code", file.Type, file.Body, meta, declared), nil
}

// ParseCodexFile turns a Codex memory or rule into one memory. Codex keeps
// them outside any workspace, so the scan declares no project for them.
func ParseCodexFile(content []byte, meta FileMeta) (Records, error) {
	body := strings.TrimSpace(string(content))
	if body == "" {
		return Records{}, nil
	}
	declared := map[string]any{"source_type": meta.SourceType}
	return memoryRecord("codex", codexTypeToLayer[meta.SourceType], body, meta, declared), nil
}
