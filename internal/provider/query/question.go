package query

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxQuestionChars is deliberately generous: ordinary questions stay short,
// while callers can still include substantial code or context without being
// pushed toward splitting one question unnaturally.
const MaxQuestionChars = 1000

// ValidateQuestion is the first gate for every natural-language query surface.
// It rejects only shapes that cannot be useful. Prompt-injection phrases are
// intentionally allowed: people legitimately ask about prompts and SQL, and the
// security boundary is the strict SQL gate, not a brittle wording filter.
func ValidateQuestion(question string) error {
	if strings.TrimSpace(question) == "" {
		return fmt.Errorf("question is empty; provide a natural-language question")
	}
	length := utf8.RuneCountInString(question)
	if length > MaxQuestionChars {
		return fmt.Errorf("question must be at most %d characters (got %d)", MaxQuestionChars, length)
	}
	return nil
}
