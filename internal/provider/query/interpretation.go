package query

import (
	"regexp"
	"strings"
)

var explicitComparisonColumn = regexp.MustCompile(
	`(?i)(?:^|_)(?:ratio|percentage?|percent|pct|difference|diff|delta|combined|multiple|times)(?:_|$)`)

var unsupportedComparisonClaims = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:[ \t]*(?:,|;|—|-)[ \t]*)?\bmore\s+than\s+(?:the\s+)?next\s+(?:two|three|[2-9])(?:\s+\w+)?\s+combined\b[ \t]*([.!?]?)`),
	regexp.MustCompile(`(?i)(?:[ \t]*(?:,|;|—|-)[ \t]*)?\b(?:(?:nearly|almost|roughly)\s+)?(?:double|twice)\s+(?:the\s+)?next\s+(?:item|one|result|row|entry)\b[ \t]*([.!?]?)`),
}

// InterpretationShapeHint warns only when several raw rows carry no explicit
// comparison result. A comparison column makes the arithmetic part of the
// evidence and therefore needs no blanket warning.
func InterpretationShapeHint(columns []string, rowCount int) string {
	if rowCount < 2 || hasExplicitComparison(columns) {
		return ""
	}
	return "These are raw rows without an explicit comparison column. Do not invent ratios, " +
		"combined totals, or cross-row arithmetic such as more than the next two combined."
}

// SanitizeInterpretation deletes a small set of known fabricated comparison
// phrases when the result shape did not compute one. It never substitutes a
// softer claim or changes numbers: everything outside the matched phrase stays.
func SanitizeInterpretation(text string, columns []string) string {
	if hasExplicitComparison(columns) {
		return text
	}
	sanitized := text
	for _, claim := range unsupportedComparisonClaims {
		sanitized = claim.ReplaceAllString(sanitized, "$1")
	}
	sanitized = strings.TrimSpace(sanitized)
	if strings.Trim(sanitized, " \t\r\n.,;:!?—-") == "" {
		return ""
	}
	return sanitized
}

func hasExplicitComparison(columns []string) bool {
	for _, column := range columns {
		normalized := strings.ReplaceAll(strings.ToLower(column), "-", "_")
		if explicitComparisonColumn.MatchString(normalized) {
			return true
		}
	}
	return false
}
