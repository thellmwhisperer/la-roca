package parsers

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const hermesMemorySeparator = "§"

var (
	hermesUserCue     = regexp.MustCompile(`(?i)\b(i am|my name|lives in|based in|identity|biography)\b`)
	hermesFeedbackCue = regexp.MustCompile(`(?i)\b(prefer|prefers|review|expect|expects|never|do not|don't|wants|hate|hates)\b`)
)

func detectHermesMemory(file File) bool {
	name := firstNonEmpty(file.Meta.FileName, filepath.Base(file.Meta.Path))
	return sourceIs(file.Meta, "hermes") && strings.EqualFold(name, "MEMORY.md") &&
		strings.TrimSpace(string(file.Content)) != ""
}

// ParseHermesMemory splits Hermes's curated MEMORY.md at §. Each block is its
// own memory: identity is the content hash, so a rewrite that drops a block
// leaves that memory to be superseded and a new block lands as a new row.
func ParseHermesMemory(content []byte, meta FileMeta) (Records, error) {
	records := Records{}
	for _, block := range hermesMemoryBlocks(string(content)) {
		hash := hermesBlockHash(block)
		identity := meta.Path + "#block=" + hash
		blockMeta := meta
		blockMeta.Path = identity
		declared := map[string]any{
			"block_hash":          hash,
			"aggregate_file_path": meta.Path,
			"file_name":           firstNonEmpty(meta.FileName, filepath.Base(meta.Path)),
		}
		parsed := memoryRecord("hermes", hermesMemoryLayer(block), block, blockMeta, declared)
		parsed.Memories[0].Origin = "agent"
		records.Memories = append(records.Memories, parsed.Memories[0])
	}
	return records, nil
}

func hermesMemoryBlocks(text string) []string {
	parts := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), hermesMemorySeparator)
	blocks := make([]string, 0, len(parts))
	for _, part := range parts {
		if block := strings.TrimSpace(part); block != "" {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func hermesBlockHash(block string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(block)))
}

func hermesMemoryLayer(content string) string {
	switch {
	case hermesUserCue.MatchString(content):
		return "user"
	case hermesFeedbackCue.MatchString(content):
		return "feedback"
	default:
		return "pattern"
	}
}
