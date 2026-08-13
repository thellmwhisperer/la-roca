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

// InterpretationShapeHint warns whenever several rows travel to the model. The
// warning is not conditioned on how the result names its columns: those names
// are the aliases of the same model this hint addresses, so a claim of having
// computed a comparison is not evidence that one was computed.
func InterpretationShapeHint(rowCount int) string {
	if rowCount < 2 {
		return ""
	}
	return "These are raw result rows. Do not invent ratios, combined totals, " +
		"or cross-row arithmetic such as more than the next two combined."
}

// SanitizeInterpretation deletes the comparison phrases the result never
// computed and the rows do not prove. What survives is proven, and proven
// means bound to its subject: the arithmetic has to hold for the row the prose
// names, read on the one quantity the result measured. An unrelated numeric
// column, an unnamed subject or a second measure proves nothing, and what is
// not proven goes. It never substitutes a softer claim or changes numbers:
// everything outside the matched phrase stays.
func SanitizeInterpretation(text string, columns []string, rows []map[string]any) string {
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
// phrase is the subject named before it and not the answer as a whole. Each
// deletion rewrites the text around it, so the walk starts over on what is
// left; a proven phrase is stepped over and never touched again.
func deleteUnprovenClaims(text string, claim comparisonClaim, ranked []rankedRow) string {
	for from := 0; from <= len(text); {
		match := claim.pattern.FindStringSubmatchIndex(text[from:])
		if match == nil {
			return text
		}
		phrase := span{from + match[0], from + match[1]}
		groups := make([]string, 3)
		for group := 1; group <= 2; group++ {
			if match[2*group] >= 0 {
				groups[group] = text[from+match[2*group] : from+match[2*group+1]]
			}
		}
		if claim.proven(groups, ranked, subjectRank(text[:phrase.start], ranked)) {
			from = phrase.end
			continue
		}
		text = deleteClaim(text, span{phrase.start, phrase.end - len(groups[2])})
		from = 0
	}
	return text
}

// span is a half-open byte range of the prose.
type span struct{ start, end int }

// deleteClaim removes the phrase and, when what is left of the sentence no
// longer reads as one, the clause or the whole sentence that carried it. It is
// still deletion and nothing else: no word is rewritten, no meaning is
// synthesized, and a neighbouring sentence that stands on its own is untouched.
func deleteClaim(text string, body span) string {
	sentence := sentenceAround(text, body)
	clause := clauseAround(text, sentence, body)
	if standsAlone(text[clause.start:body.start] + text[body.end:clause.end]) {
		return text[:body.start] + text[body.end:]
	}
	cut := clauseCut(text, sentence, clause)
	if cut.start <= sentence.start && cut.end >= sentence.end {
		return deleteSentence(text, sentence)
	}
	if !standsAlone(text[sentence.start:cut.start] + text[cut.end:sentence.end]) {
		return deleteSentence(text, sentence)
	}
	return text[:cut.start] + text[cut.end:]
}

// sentenceAround is the body of the sentence the phrase sits in, terminator
// excluded. A line break ends a sentence too: a paragraph the model wrote apart
// is not one clause of the one above it.
func sentenceAround(text string, body span) span {
	sentence := span{0, len(text)}
	if at := lastBreakBefore(text, body.start, 0, isSentenceBreak); at >= 0 {
		sentence.start = at + 1
	}
	for sentence.start < body.start && isBlank(text[sentence.start]) {
		sentence.start++
	}
	if at := firstBreakAfter(text, body.end, len(text), isSentenceBreak); at >= 0 {
		sentence.end = at
	}
	return sentence
}

// clauseAround is the part of that sentence the phrase belongs to, between the
// commas, semicolons, colons or dashes that separate it from the rest.
func clauseAround(text string, sentence, body span) span {
	clause := sentence
	if at := lastBreakBefore(text, body.start, sentence.start, isClauseBreak); at >= 0 {
		clause.start = at + 1
	}
	for clause.start < body.start && isBlank(text[clause.start]) {
		clause.start++
	}
	if at := firstBreakAfter(text, body.end, sentence.end, isClauseBreak); at >= 0 {
		clause.end = at
	}
	return clause
}

// clauseCut widens the clause over the separator that joined it to the rest, so
// removing one clause of three does not leave the punctuation of a clause that
// is no longer there.
func clauseCut(text string, sentence, clause span) span {
	cut := clause
	if at := lastBreakBefore(text, clause.start, sentence.start, isClauseBreak); at >= 0 {
		cut.start = at
		return cut
	}
	if cut.end < sentence.end {
		_, size := utf8.DecodeRuneInString(text[cut.end:])
		cut.end += size
		for cut.end < sentence.end && isBlank(text[cut.end]) {
			cut.end++
		}
	}
	return cut
}

// deleteSentence takes the sentence with its terminator and the space that
// followed it, which is what keeps the sentences around it exactly as they were.
// A sentence that had a line to itself takes that line break too, or what is
// left of the answer is a blank line where a paragraph used to be.
func deleteSentence(text string, sentence span) string {
	end := sentence.end
	if end < len(text) && strings.ContainsRune(".!?", rune(text[end])) {
		end++
	}
	for end < len(text) && (text[end] == ' ' || text[end] == '\t') {
		end++
	}
	if end < len(text) && text[end] == '\n' && startsItsOwnLine(text, sentence.start) {
		end++
	}
	return text[:sentence.start] + text[end:]
}

func startsItsOwnLine(text string, start int) bool {
	for at := start - 1; at >= 0; at-- {
		if text[at] == '\n' {
			return true
		}
		if !isBlank(text[at]) {
			return false
		}
	}
	return true
}

// danglingWords end a clause that was leaning on what has just been deleted.
// A remainder that stops at one of them is not a shorter sentence: it is the
// first half of one.
var danglingWords = set(
	"is", "are", "was", "were", "am", "be", "been", "being",
	"has", "have", "had", "does", "do", "did", "than", "then",
	"the", "a", "an", "of", "and", "or", "but", "with", "without",
	"by", "to", "at", "in", "on", "for", "from", "as", "that", "which",
	"while", "so", "because", "its", "their", "his", "her", "our", "your", "my")

// leadingConjunctions open a clause that hung from something the deletion took
// away. In lower case they are the other half of the same defect: a remainder
// that starts on one is the second half of a sentence, not a sentence.
var leadingConjunctions = set("and", "or", "but", "nor", "yet", "so", "because",
	"while", "than", "then", "which", "that", "though", "although", "whereas")

// standsAlone is the one rule every deletion answers to: what is left either
// reads as a sentence of its own or does not stay at all. It leans on neither
// end, because a remainder can be left hanging from either one.
func standsAlone(remainder string) bool {
	words := strings.Fields(strings.Trim(remainder, " \t\r\n.,;:!?—-"))
	if len(words) < 2 {
		return false
	}
	first := bareWord(words[0])
	if startsLower(first) && leadingConjunctions[strings.ToLower(first)] {
		return false
	}
	return !danglingWords[strings.ToLower(bareWord(words[len(words)-1]))]
}

func bareWord(word string) string { return strings.Trim(word, `.,;:!?"'()`) }

func startsLower(word string) bool {
	first, _ := utf8.DecodeRuneInString(word)
	return unicode.IsLower(first)
}

func isSentenceBreak(r rune) bool { return r == '.' || r == '!' || r == '?' || r == '\n' }

func isClauseBreak(r rune) bool {
	return r == ',' || r == ';' || r == ':' || r == '—' || r == '–'
}

func isBlank(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }

func lastBreakBefore(text string, from, limit int, isBreak func(rune) bool) int {
	for at := from; at > limit; {
		r, size := utf8.DecodeLastRuneInString(text[limit:at])
		at -= size
		if isBreak(r) {
			return at
		}
	}
	return -1
}

func firstBreakAfter(text string, from, limit int, isBreak func(rune) bool) int {
	for at := from; at < limit; {
		r, size := utf8.DecodeRuneInString(text[at:])
		if isBreak(r) {
			return at
		}
		at += size
	}
	return -1
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
