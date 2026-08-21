package parsers

import (
	"regexp"
	"strings"
	"time"
)

var (
	codexThreadBoundary = regexp.MustCompile("(?m)^## Thread `([^`\\r\\n]+)`[ \\t]*$")
	codexUpdatedAt      = regexp.MustCompile(`(?m)^updated_at:[ \t]*(\S+)[ \t]*$`)
	codexCwd            = regexp.MustCompile(`(?m)^cwd:[ \t]*(.*\S)[ \t]*$`)
)

// ParseCodexMemoryAggregate splits Codex's merged stage-1 export at its thread
// headings. Each block keeps the complete source text while its thread id gets
// a synthetic path, so it cannot collide with the historical whole-file row.
func ParseCodexMemoryAggregate(content []byte, meta FileMeta) (Records, error) {
	text := string(content)
	boundaries := codexThreadBoundary.FindAllStringSubmatchIndex(text, -1)
	records := Records{}
	for index, boundary := range boundaries {
		end := len(text)
		if index+1 < len(boundaries) {
			end = boundaries[index+1][0]
		}
		block := strings.TrimSpace(text[boundary[0]:end])
		threadID := text[boundary[2]:boundary[3]]
		stamp := firstMatch(codexUpdatedAt, block)
		if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
			records.Discards = append(records.Discards, Discard{
				Record: index + 1, Reason: "thread block has no valid updated_at timestamp",
			})
			continue
		}

		identity := meta.Path + "#thread=" + threadID
		threadMeta := meta
		threadMeta.Path = identity
		declared := map[string]any{
			"source_type":         "memory",
			"thread_id":           threadID,
			"aggregate_file_path": meta.Path,
			"updated_at":          stamp,
		}
		if cwd := firstMatch(codexCwd, block); cwd != "" {
			declared["cwd"] = cwd
		}
		parsed := memoryRecord("codex", codexTypeToLayer["memory"], block, threadMeta, declared)
		parsed.Memories[0].CreatedAt = stamp
		records.Memories = append(records.Memories, parsed.Memories[0])
	}
	return records, nil
}

func firstMatch(pattern *regexp.Regexp, text string) string {
	match := pattern.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}
