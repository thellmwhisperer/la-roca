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

// ValidateQuestion checks the shape of a deterministic search. Prompt content
// is searchable data; model-invoking surfaces own their prompt defenses.
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
