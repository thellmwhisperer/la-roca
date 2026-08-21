package parsers

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func detectGLMSkill(file File) bool {
	name := firstNonEmpty(file.Meta.FileName, filepath.Base(file.Meta.Path))
	return file.Meta.SourceAgent == "glm" && strings.EqualFold(filepath.Ext(name), ".md") &&
		utf8.Valid(file.Content) && strings.TrimSpace(string(file.Content)) != ""
}

// ParseGLMSkill keeps one installed skill document as one user knowledge
// record. Supporting templates beneath a skill are separate files with their
// own stable path identity, so updates replace only the document that changed.
func ParseGLMSkill(content []byte, meta FileMeta) (Records, error) {
	body := strings.TrimSpace(string(content))
	if body == "" {
		return Records{}, nil
	}
	return memoryRecord("glm", "user", body, meta,
		map[string]any{"source_type": "skill"}), nil
}
