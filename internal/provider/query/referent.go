package query

import (
	"regexp"
	"strings"
)

var indefiniteReferent = regexp.MustCompile(
	`(?i)\b(?:a|an)\s+(?:specific|particular|given|named)\s+([a-z][a-z0-9_-]*)\b`)

// referentValueAfter are the shapes in which the question already supplied the
// value, so there is nothing to ask for. A quoted word right after the noun is
// one of them: "a specific project 'synthetic-orchid'" names its project as
// plainly as "called synthetic-orchid" does.
var referentValueAfter = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^\s+(?:called|named|with\s+(?:the\s+)?(?:name|id|key|value))\s+["']?[a-z0-9]`),
	regexp.MustCompile(`(?i)^\s*[:,]\s*["']?[a-z0-9]`),
	regexp.MustCompile(`(?i)^\s*["'][a-z0-9]`),
}

// MissingReferent is the generic slot named by the question itself and the
// clarification that asks for its value.
type MissingReferent struct {
	Slot string
	Ask  string
}

// DetectMissingReferent recognizes only an explicit indefinite placeholder
// such as "a specific project". It carries no dataset vocabulary and does not
// try to infer missing entities from ordinary pronouns or broad questions.
func DetectMissingReferent(question string) (MissingReferent, bool) {
	match := indefiniteReferent.FindStringSubmatchIndex(question)
	if match == nil {
		return MissingReferent{}, false
	}
	tail := question[match[1]:]
	for _, value := range referentValueAfter {
		if value.MatchString(tail) {
			return MissingReferent{}, false
		}
	}
	slot := strings.ToLower(question[match[2]:match[3]])
	return MissingReferent{
		Slot: slot,
		Ask:  "Which " + slot + " should I use? Please name it in the question.",
	}, true
}
