package logfile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

func TestErrorTypeAnswersDeclaredCategoriesNeverGoTypeNames(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"plain", errors.New("synthetic failure"), ErrorUnclassified},
		{"declared", Typed(errors.New("synthetic refusal"), ErrorInvalidUsage), ErrorInvalidUsage},
		{"declared through a wrap", fmt.Errorf("while running: %w",
			Typed(errors.New("synthetic refusal"), ErrorNotInitialized)), ErrorNotInitialized},
		{"declared through a correlation", Correlate(
			Typed(errors.New("synthetic refusal"), ErrorInvalidUsage)), ErrorInvalidUsage},
		{"joined", errors.Join(errors.New("synthetic first"),
			Typed(errors.New("synthetic second"), ErrorTimeout)), ErrorTimeout},
		{"canceled", fmt.Errorf("call: %w", context.Canceled), ErrorCanceled},
		{"deadline", fmt.Errorf("call: %w", context.DeadlineExceeded), ErrorTimeout},
		{"missing", fmt.Errorf("open the log: %w", fs.ErrNotExist), ErrorNotFound},
		{"refused", fmt.Errorf("open the log: %w", fs.ErrPermission), ErrorPermission},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ErrorType(testCase.err); got != testCase.want {
				t.Fatalf("ErrorType = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestCorrelateKeepsTheDeclaredCategoryAndTheUserFacingMessage(t *testing.T) {
	err := Correlate(Typed(errors.New("synthetic refusal"), ErrorInvalidUsage))
	id := CorrelationID(err)
	if id == "" {
		t.Fatal("a correlated error carries no correlation ID")
	}
	if want := "synthetic refusal (correlation_id: " + id + ")"; err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
	if again := Correlate(err); CorrelationID(again) != id {
		t.Fatalf("correlating twice minted a second ID: %q then %q", id, CorrelationID(again))
	}
}
