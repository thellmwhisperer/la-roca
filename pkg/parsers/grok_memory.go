package parsers

import (
	"path/filepath"
	"strings"
)

func detectGrokMemory(file File) bool {
	if !sourceIs(file.Meta, "grok") || strings.TrimSpace(string(file.Content)) == "" {
		return false
	}
	name := firstNonEmpty(file.Meta.FileName, filepath.Base(file.Meta.Path))
	if !strings.EqualFold(name, "MEMORY.md") {
		return false
	}
	return grokMemoryPath(file.Meta.Path)
}

// ParseGrokMemory keeps one MEMORY.md as one memory. The file is the row:
// a later edit of the same path supersedes that row instead of splitting
// the document or inserting a second copy.
func ParseGrokMemory(content []byte, meta FileMeta) (Records, error) {
	body := strings.TrimSpace(string(content))
	if body == "" {
		return Records{}, nil
	}
	return memoryRecord("grok", "", body, meta, nil), nil
}

func grokMemoryPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] != "memory-v2" {
			continue
		}
		rest := parts[i+1:]
		switch {
		case len(rest) == 2 && rest[0] == "global" && strings.EqualFold(rest[1], "MEMORY.md"):
			return true
		case len(rest) == 3 && rest[0] == "workspaces" && rest[1] != "" &&
			strings.EqualFold(rest[2], "MEMORY.md"):
			return true
		default:
			return false
		}
	}
	return false
}
