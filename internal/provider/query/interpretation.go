package query

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var explicitComparisonColumn = regexp.MustCompile(
	`(?i)(?:^|_)(?:ratio|percentage?|percent|pct|difference|diff|delta|combined|multiple|times)(?:_|$)`)

// comparisonClaim is one phrase the guardian knows how to check. The pattern
// captures what the claim asserts and the punctuation that must survive its
// deletion; supported reads that assertion against the rows the model saw.
type comparisonClaim struct {
	pattern   *regexp.Regexp
	supported func(claim []string, magnitudes []float64) bool
}

var comparisonClaims = []comparisonClaim{
	{
		pattern: regexp.MustCompile(
			`(?i)(?:[ \t]*(?:,|;|—|-)[ \t]*)?\bmore\s+than\s+(?:the\s+)?next\s+(two|three|[2-9])(?:\s+\w+)?\s+combined\b[ \t]*([.!?]?)`),
		supported: moreThanTheNextCombined,
	},
	{
		pattern: regexp.MustCompile(
			`(?i)(?:[ \t]*(?:,|;|—|-)[ \t]*)?\b((?:nearly|almost|roughly)\s+)?(?:double|twice)\s+(?:the\s+)?next\s+(?:item|one|result|row|entry)\b[ \t]*([.!?]?)`),
		supported: doubleTheNext,
	},
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

// InterpretationMayBeSanitized says the guardian could still delete a phrase
// from prose about this result. It is what tells a live caller whether the
// text can be published as it arrives or has to be held until it is complete.
func InterpretationMayBeSanitized(columns []string) bool { return !hasExplicitComparison(columns) }

// SanitizeInterpretation deletes a small set of fabricated comparison phrases:
// the ones the result shape never computed AND the rows do not bear out. A
// claim the numbers in the prompt support is left alone, because it is the
// evidence and not the column name that makes it true. It never substitutes a
// softer claim or changes numbers: everything outside the matched phrase stays.
func SanitizeInterpretation(text string, columns []string, rows []map[string]any) string {
	if !InterpretationMayBeSanitized(columns) {
		return text
	}
	evidence := comparableMagnitudes(columns, rows)
	sanitized := text
	for _, claim := range comparisonClaims {
		sanitized = claim.pattern.ReplaceAllStringFunc(sanitized, func(phrase string) string {
			groups := claim.pattern.FindStringSubmatch(phrase)
			for _, magnitudes := range evidence {
				if claim.supported(groups, magnitudes) {
					return phrase
				}
			}
			return groups[2]
		})
	}
	sanitized = strings.TrimSpace(sanitized)
	if strings.Trim(sanitized, " \t\r\n.,;:!?—-") == "" {
		return ""
	}
	return sanitized
}

// moreThanTheNextCombined checks the claim the way the model should have: the
// largest magnitude against the sum of the next ones it names.
func moreThanTheNextCombined(claim []string, magnitudes []float64) bool {
	next := countWord(claim[1])
	if next <= 0 || len(magnitudes) < next+1 {
		return false
	}
	var combined float64
	for _, magnitude := range magnitudes[1 : next+1] {
		combined += magnitude
	}
	return magnitudes[0] > combined
}

// doubleTheNext reads the hedge as part of the claim: "nearly double" asserts
// less than "double" does, and deleting it for missing the exact factor would
// be deleting a claim the rows bear out.
func doubleTheNext(claim []string, magnitudes []float64) bool {
	if len(magnitudes) < 2 {
		return false
	}
	factor := 2.0
	if strings.TrimSpace(claim[1]) != "" {
		factor = 1.8
	}
	return magnitudes[1] > 0 && magnitudes[0] >= factor*magnitudes[1]
}

func countWord(word string) int {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "two":
		return 2
	case "three":
		return 3
	}
	count, err := strconv.Atoi(strings.TrimSpace(word))
	if err != nil {
		return 0
	}
	return count
}

// comparableMagnitudes is the evidence a cross-row comparison can be checked
// against: every column whose value is a number in every row, each sorted from
// largest to smallest. A claim any one of them bears out is not a fabrication.
func comparableMagnitudes(columns []string, rows []map[string]any) [][]float64 {
	if len(rows) < 2 {
		return nil
	}
	var evidence [][]float64
	for _, column := range columns {
		magnitudes := make([]float64, 0, len(rows))
		for _, row := range rows {
			number, ok := numeric(row[column])
			if !ok {
				magnitudes = nil
				break
			}
			magnitudes = append(magnitudes, number)
		}
		if len(magnitudes) == 0 {
			continue
		}
		slices.SortFunc(magnitudes, func(a, b float64) int { return cmp.Compare(b, a) })
		evidence = append(evidence, magnitudes)
	}
	return evidence
}

// numeric reads a row value as a magnitude. The database driver hands numbers
// back as int64 or float64, and the row renderer may have turned them into
// text, so a numeric string counts too.
func numeric(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return number, err == nil
	case []byte:
		number, err := strconv.ParseFloat(strings.TrimSpace(string(typed)), 64)
		return number, err == nil
	}
	return 0, false
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
