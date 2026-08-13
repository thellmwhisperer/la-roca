package query_test

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

func TestMissingReferentUsesTheQuestionsOwnGenericNoun(t *testing.T) {
	for _, testCase := range []struct {
		question, slot string
	}{
		{"Which agents worked on a specific project?", "project"},
		{"What happened to a particular release?", "release"},
		{"Compare a given provider across sessions", "provider"},
	} {
		missing, found := query.DetectMissingReferent(testCase.question)
		if !found || missing.Slot != testCase.slot ||
			!strings.Contains(strings.ToLower(missing.Ask), "which "+testCase.slot) {
			t.Errorf("DetectMissingReferent(%q) = %+v, %v", testCase.question, missing, found)
		}
	}
}

func TestMissingReferentDetectorStaysNarrow(t *testing.T) {
	for _, question := range []string{
		"Which specific project has the most sessions?",
		"What are the top providers?",
		"Show sessions from a specific project called synthetic-orchid",
		`Show sessions from a specific project: "synthetic-orchid"`,
		`Show sessions from a specific project "synthetic-orchid"`,
	} {
		if missing, found := query.DetectMissingReferent(question); found {
			t.Errorf("DetectMissingReferent(%q) = %+v, want no ask", question, missing)
		}
	}
}
