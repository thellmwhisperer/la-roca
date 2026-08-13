package query

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxQuestionChars is deliberately generous: ordinary questions stay short,
// while callers can still include substantial code or context without being
// pushed toward splitting one question unnaturally.
const MaxQuestionChars = 1000

// ErrQuestionRejected is deliberately generic: callers learn that the input
// gate refused the question, never which signature it matched.
var ErrQuestionRejected = errors.New("invalid question")

var hostileQuestionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(?:(?:all|previous|prior|above|my|the|your|these)\s+)*(instructions?|prompts?|rules?)`),
	regexp.MustCompile(`(?i)disregard\s+(?:(?:all|your|previous|prior|above|the|my|these)\s+)*(instructions?|prompts?|rules?)`),
	regexp.MustCompile(`(?i)forget\s+(everything|all|your)`),
	regexp.MustCompile(`(?i)do\s+not\s+follow\s+(your|the|any)`),
	regexp.MustCompile(`(?i)override\s+(your|the|all|any)\s+(instructions?|prompts?|rules?)`),
	regexp.MustCompile(`(?i)you\s+are\s+now\b`),
	regexp.MustCompile(`(?i)\bact\s+as\s+(?:(?:a|an)\s+)?(database|system|admin|root|super|helpful|unrestricted|unfiltered)\b`),
	regexp.MustCompile(`(?i)\bsystem\s+prompt\b`),
	regexp.MustCompile(`(?i)\bjailbreak\b`),
	regexp.MustCompile(`(?i)\b(?:you\s+are\s+DAN|enable\s+DAN|DAN\s+mode)\b`),
	regexp.MustCompile("```"),
	regexp.MustCompile(`(?i)\[INST\]`),
	regexp.MustCompile(`(?i)<<SYS>>`),
	regexp.MustCompile(`(?i)\b(?:base64|hex)\s+decode\s+(?:this|the|it)(?:\s+(?:string|payload|text|code))?\b`),
	regexp.MustCompile(`(?i)\bdecode\s+(this|the|it)\s+(string|payload|text|base64|hex|code)\b`),
	regexp.MustCompile(`(?i)\b0x[0-9a-f]{8,}\b`),
}

// ValidateQuestion is the first gate for every natural-language query surface.
// It rejects unusable shapes and the lineage's narrow set of known prompt
// attacks. The strict SQL gate remains the execution boundary; this earlier
// gate avoids sending recognizable jailbreak and encoding payloads to a model.
// Turning strict off skips only those signatures, never the basic shape checks.
func ValidateQuestion(question string, strict bool) error {
	if strings.TrimSpace(question) == "" {
		return fmt.Errorf("question is empty; provide a natural-language question")
	}
	length := utf8.RuneCountInString(question)
	if length > MaxQuestionChars {
		return fmt.Errorf("question must be at most %d characters (got %d)", MaxQuestionChars, length)
	}
	if !strict {
		return nil
	}
	for _, pattern := range hostileQuestionPatterns {
		if pattern.MatchString(question) {
			return ErrQuestionRejected
		}
	}
	return nil
}
