package service

import (
	"testing"
	"time"
)

func TestQueryExecutionBudgetDistinguishesDefaultDisabledAndConfigured(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		opts   Options
		budget time.Duration
	}{
		{name: "absent uses default", budget: DefaultQueryTimeout},
		{name: "explicit zero uses default", opts: Options{QueryTimeoutSet: true},
			budget: DefaultQueryTimeout},
		{name: "a bound no statement could meet falls back to default",
			opts:   Options{QueryTimeout: -5 * time.Second, QueryTimeoutSet: true},
			budget: DefaultQueryTimeout},
		{name: "configured", opts: Options{QueryTimeout: 2750 * time.Millisecond, QueryTimeoutSet: true},
			budget: 2750 * time.Millisecond},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			svc := Service{opts: testCase.opts}
			if budget := svc.queryExecutionBudget(); budget != testCase.budget {
				t.Fatalf("budget = %s; want %s", budget, testCase.budget)
			}
		})
	}
}
