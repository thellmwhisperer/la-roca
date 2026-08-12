package provider

import (
	"regexp"
	"strings"
)

// CleanProse is the shape for the second call, the row interpretation: the
// reasoning block goes, the blanks are trimmed, and nothing else is touched.
// Prose legitimately quotes fenced blocks and ends in punctuation, so the SQL
// repair path must never run on it.
func CleanProse(raw string) string {
	return strings.TrimSpace(thinkingBlock.ReplaceAllString(raw, ""))
}

// thinkingBlock is the reasoning a thinking model emits before answering. It is
// not part of the answer and the gate would reject it as syntax.
var thinkingBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)
