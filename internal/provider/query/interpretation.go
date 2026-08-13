package query

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var explicitComparisonColumn = regexp.MustCompile(
	`(?i)(?:^|_)(?:ratio|percentage?|percent|pct|difference|diff|delta|combined|multiple|times)(?:_|$)`)

// comparisonClaim is one phrase the guardian knows how to check. The pattern
// captures what the claim asserts and the punctuation that must survive its
// deletion; proven reads that assertion against the ranked rows, starting from
// the one the prose named.
type comparisonClaim struct {
	pattern *regexp.Regexp
	proven  func(claim []string, ranked []rankedRow, subject int) bool
}

var comparisonClaims = []comparisonClaim{
	{
		pattern: regexp.MustCompile(
			`(?i)[ \t]*(?:(?:,|;|—|-)[ \t]*)?\bmore\s+than\s+(?:the\s+)?next\s+(two|three|[2-9])(?:\s+\w+)?\s+combined\b[ \t]*([.!?]?)`),
		proven: moreThanTheNextCombined,
	},
	{
		pattern: regexp.MustCompile(
			`(?i)[ \t]*(?:(?:,|;|—|-)[ \t]*)?\b((?:nearly|almost|roughly)\s+)?(?:double|twice)\s+(?:the\s+)?next\s+(?:item|one|result|row|entry)\b[ \t]*([.!?]?)`),
		proven: doubleTheNext,
	},
}

// rankedRow is one result row as a comparison sees it: the magnitude the
// result measured, and the names the prose can call that row by.
type rankedRow struct {
	labels    []string
	magnitude float64
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

// SanitizeInterpretation deletes the comparison phrases the result never
// computed and the rows do not prove. What survives is proven, and proven
// means bound to its subject: the arithmetic has to hold for the row the prose
// names, read on the one quantity the result measured. An unrelated numeric
// column, an unnamed subject or a second measure proves nothing, and what is
// not proven goes. It never substitutes a softer claim or changes numbers:
// everything outside the matched phrase stays.
func SanitizeInterpretation(text string, columns []string, rows []map[string]any) string {
	if !InterpretationMayBeSanitized(columns) {
		return text
	}
	ranked := rankedEvidence(columns, rows)
	sanitized := text
	for _, claim := range comparisonClaims {
		sanitized = deleteUnprovenClaims(sanitized, claim, ranked)
	}
	sanitized = strings.TrimSpace(sanitized)
	if strings.Trim(sanitized, " \t\r\n.,;:!?—-") == "" {
		return ""
	}
	return sanitized
}

// deleteUnprovenClaims walks the phrases one by one, because what proves a
// phrase is the subject named before it and not the answer as a whole.
func deleteUnprovenClaims(text string, claim comparisonClaim, ranked []rankedRow) string {
	matches := claim.pattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	written := 0
	for _, match := range matches {
		groups := make([]string, 3)
		for group := 1; group <= 2; group++ {
			if match[2*group] >= 0 {
				groups[group] = text[match[2*group]:match[2*group+1]]
			}
		}
		out.WriteString(text[written:match[0]])
		if claim.proven(groups, ranked, subjectRank(text[:match[0]], ranked)) {
			out.WriteString(text[match[0]:match[1]])
		} else {
			out.WriteString(groups[2])
		}
		written = match[1]
	}
	out.WriteString(text[written:])
	return out.String()
}

// moreThanTheNextCombined checks the claim the way the model should have: the
// named row's magnitude against the sum of the ones ranked right after it.
func moreThanTheNextCombined(claim []string, ranked []rankedRow, subject int) bool {
	next := countWord(claim[1])
	if subject < 0 || next <= 0 || len(ranked) < subject+next+1 {
		return false
	}
	var combined float64
	for _, row := range ranked[subject+1 : subject+next+1] {
		combined += row.magnitude
	}
	return ranked[subject].magnitude > combined
}

// doubleTheNext reads the hedge as part of the claim: "nearly double" asserts
// less than "double" does, and deleting it for missing the exact factor would
// be deleting a claim the rows prove.
func doubleTheNext(claim []string, ranked []rankedRow, subject int) bool {
	if subject < 0 || len(ranked) < subject+2 {
		return false
	}
	factor := 2.0
	if strings.TrimSpace(claim[1]) != "" {
		factor = 1.8
	}
	next := ranked[subject+1].magnitude
	return next > 0 && ranked[subject].magnitude >= factor*next
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

// rankedEvidence is the result read as a comparison: the one measured quantity,
// largest first, with the names each row answers to. Without a single measure
// there is nothing a cross-row claim can be checked against, and guessing which
// of two quantities the prose meant is how an unrelated column ends up vouching
// for a fabrication.
func rankedEvidence(columns []string, rows []map[string]any) []rankedRow {
	if len(rows) < 2 {
		return nil
	}
	measure := theMeasuredColumn(columns, rows)
	if measure == "" {
		return nil
	}
	ranked := make([]rankedRow, 0, len(rows))
	for _, row := range rows {
		magnitude, ok := numeric(row[measure])
		if !ok {
			return nil
		}
		ranked = append(ranked, rankedRow{labels: labelsOf(columns, measure, row), magnitude: magnitude})
	}
	slices.SortStableFunc(ranked, func(a, b rankedRow) int { return cmp.Compare(b.magnitude, a.magnitude) })
	return ranked
}

// theMeasuredColumn is the only column a comparison can be about: the single
// one that is a number in every row. Two of them and the quantity is ambiguous;
// none and there is nothing to compare.
func theMeasuredColumn(columns []string, rows []map[string]any) string {
	measured := ""
	for _, column := range columns {
		countable := true
		for _, row := range rows {
			if _, ok := numeric(row[column]); !ok {
				countable = false
				break
			}
		}
		if !countable {
			continue
		}
		if measured != "" {
			return ""
		}
		measured = column
	}
	return measured
}

// labelsOf are the names the prose can call a row by: everything it carries
// that is not the measured quantity.
func labelsOf(columns []string, measure string, row map[string]any) []string {
	labels := make([]string, 0, len(columns))
	for _, column := range columns {
		if column == measure {
			continue
		}
		label := strings.TrimSpace(fmt.Sprint(row[column]))
		if label != "" {
			labels = append(labels, label)
		}
	}
	return labels
}

// subjectRank is which row the phrase is about: the last one named before it.
// A phrase that names nobody is bound to nobody, and a name two rows answer to
// with different magnitudes says which of them no better than silence does.
func subjectRank(before string, ranked []rankedRow) int {
	subject, latest := -1, -1
	ambiguous := false
	for rank, row := range ranked {
		for _, label := range row.labels {
			at := lastMentionOf(label, before)
			switch {
			case at < 0 || at < latest:
			case at > latest:
				latest, subject, ambiguous = at, rank, false
			case rank != subject && row.magnitude != ranked[subject].magnitude:
				ambiguous = true
			}
		}
	}
	if ambiguous {
		return -1
	}
	return subject
}

// lastMentionOf finds the label as a whole word, so a row called "Ana" is not
// named by "ganancia" here either.
func lastMentionOf(label, text string) int {
	if label == "" {
		return -1
	}
	haystack, needle := strings.ToLower(text), strings.ToLower(label)
	found := -1
	for at := 0; at+len(needle) <= len(haystack); {
		next := strings.Index(haystack[at:], needle)
		if next < 0 {
			break
		}
		start := at + next
		if isolatedWord(haystack, start, start+len(needle)) {
			found = start
		}
		at = start + 1
	}
	return found
}

func isolatedWord(text string, start, end int) bool {
	before, after := ' ', ' '
	if start > 0 {
		before, _ = utf8.DecodeLastRuneInString(text[:start])
	}
	if end < len(text) {
		after, _ = utf8.DecodeRuneInString(text[end:])
	}
	return !wordRune(before) && !wordRune(after)
}

func wordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

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
