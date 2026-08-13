package query

import "regexp"

var refusalAnswer = regexp.MustCompile("(?is)^\\s*(?:```(?:sql|sqlite)?\\s*)?REFUSE\\b")

// IsRefusal recognizes the model's explicit out-of-scope answer before any
// SQL repair or parsing treats it as a malformed statement.
func IsRefusal(answer string) bool { return refusalAnswer.MatchString(answer) }
