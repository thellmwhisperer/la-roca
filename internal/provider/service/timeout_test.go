package service

import (
	"testing"
	"time"
)

func TestQueryExecutionBudgetDistinguishesDefaultDisabledAndConfigured(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		opts    Options
		budget  time.Duration
		enabled bool
	}{
		{name: "absent uses default", budget: DefaultQueryTimeout, enabled: true},
		{name: "explicit zero disables", opts: Options{QueryTimeoutSet: true}},
		{name: "configured", opts: Options{QueryTimeout: 2750 * time.Millisecond, QueryTimeoutSet: true},
			budget: 2750 * time.Millisecond, enabled: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			svc := Service{opts: testCase.opts}
			budget, enabled := svc.queryExecutionBudget()
			if budget != testCase.budget || enabled != testCase.enabled {
				t.Fatalf("budget = %s, enabled = %v; want %s, %v",
					budget, enabled, testCase.budget, testCase.enabled)
			}
		})
	}
}
