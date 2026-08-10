package provider

import (
	"regexp"
	"strings"
)

// Clean turns what a model emitted into what the gate can look at.
//
// None of the three things it does is cosmetic: each one is a shape in which a
// real model breaks a real query, observed in the lab against local models.
// What it does NOT do is repair SQL: the gate judges, and the repairs of
// TECH-SPEC 1.5 are built only when the golden bench proves each one necessary.
func Clean(raw string) string {
	text := thinkingBlock.ReplaceAllString(raw, "")
	text = insideTheFence(text)
	text = deloop(text)
	text = strings.TrimSpace(text)
	text = strings.TrimSuffix(text, ";")
	return strings.TrimSpace(text)
}

// CleanProse is the shape for the second call, the row interpretation: the
// reasoning block goes, the blanks are trimmed, and nothing else is touched.
// Prose legitimately quotes fenced blocks and ends in punctuation, so Clean's
// fence extraction must never run on it: applied to a full Spanish answer it
// kept only the first fence and delivered the single word "atm" (2026-08-10).
func CleanProse(raw string) string {
	return strings.TrimSpace(thinkingBlock.ReplaceAllString(raw, ""))
}

// thinkingBlock is the reasoning a thinking model emits before answering. It is
// not part of the answer and the gate would reject it as syntax.
var thinkingBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)

var fence = regexp.MustCompile("(?s)```(?:[a-zA-Z]*)\n?(.*?)```")

// insideTheFence keeps what a model wrapped in markdown despite being told not
// to. Asking again costs twenty seconds; reading the fence costs nothing.
func insideTheFence(text string) string {
	if match := fence.FindStringSubmatch(text); match != nil {
		return match[1]
	}
	return text
}

// loopThreshold is the length above which a repeated line is a loop and not
// legitimate SQL. Short lines repeat on their own in a UNION or a join; a long
// one repeating verbatim is a small model that has stopped being able to stop.
const loopThreshold = 50

// deloop cuts the text at the first repetition loop, like the lab's `_deloop`.
func deloop(text string) string {
	lines := strings.Split(text, "\n")
	seen := make(map[string]bool, len(lines))
	for i, line := range lines {
		key := strings.TrimSpace(line)
		if len(key) <= loopThreshold {
			continue
		}
		if seen[key] {
			return strings.TrimSpace(strings.Join(lines[:i], "\n"))
		}
		seen[key] = true
	}
	return text
}
