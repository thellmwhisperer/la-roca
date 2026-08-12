package query_test

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

func TestQuestionGateRejectsOnlyEmptyAndOversizedQuestions(t *testing.T) {
	benchCases := []struct {
		name     string
		question string
		wantErr  string
	}{
		{name: "empty", wantErr: "question is empty"},
		{name: "whitespace", question: " \n\t ", wantErr: "question is empty"},
		{name: "boundary", question: strings.Repeat("界", query.MaxQuestionChars)},
		{name: "oversized", question: strings.Repeat("x", query.MaxQuestionChars+1), wantErr: "at most 1000 characters"},
		{
			name:     "legitimate prompt-injection discussion",
			question: "How should SQL store the phrase ignore previous instructions in code?",
		},
	}
	for _, benchCase := range benchCases {
		t.Run(benchCase.name, func(t *testing.T) {
			err := query.ValidateQuestion(benchCase.question)
			if benchCase.wantErr == "" && err != nil {
				t.Fatalf("ValidateQuestion: %v", err)
			}
			if benchCase.wantErr != "" && (err == nil || !strings.Contains(err.Error(), benchCase.wantErr)) {
				t.Fatalf("ValidateQuestion error = %v, want %q", err, benchCase.wantErr)
			}
		})
	}
}
