package parsers

import (
	"path/filepath"
	"strings"
)

func detectZCodeMemory(file File) bool {
	if !sourceIs(file.Meta, "zcode") || strings.TrimSpace(string(file.Content)) == "" {
		return false
	}
	if firstObject(file.Content) != nil {
		return false
	}
	return zcodeMemoryPath(file.Meta.Path)
}

// ParseZCodeMemory turns one project memory file into one memory. The
// frontmatter contract is the Claude Code one: name and description become
// metadata, the body is the content, and a declared type chooses the layer.
// ZCode writes that type under metadata.type; a top-level type is accepted
// so a file shaped exactly like a Claude memory still parses.
func ParseZCodeMemory(content []byte, meta FileMeta) (Records, error) {
	file := ParseMemoryFile(content)
	if file.Body == "" {
		return Records{}, nil
	}
	declared := map[string]any{}
	putIfSet(declared, "memory_name", file.Name)
	putIfSet(declared, "memory_description", file.Description)
	return memoryRecord("zcode", file.Type, file.Body, meta, declared), nil
}

func zcodeMemoryPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := 0; i+4 < len(parts); i++ {
		if parts[i] != "memories" || parts[i+1] != "projects" || parts[i+3] != "memory" {
			continue
		}
		if parts[i+2] == "" || !strings.HasSuffix(strings.ToLower(parts[i+4]), ".md") {
			return false
		}
		return i+4 == len(parts)-1
	}
	return false
}
