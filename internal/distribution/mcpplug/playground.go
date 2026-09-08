package mcpplug

import (
	"context"
	"github.com/thellmwhisperer/la-roca/internal/distribution/playground"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"strconv"
)

func (p *plug) playground(ctx context.Context, verb, question, layer, databases string, maxChars int, deep bool) (service.QueryResult, error) {
	args := []string{verb, "--db-path", p.svc.DB().Path()}
	if verb == "sql" {
		args[0] = "playground"
		args = append(args, "--sql-only")
	}
	if layer != "" {
		args = append(args, "--layer", layer)
	}
	if databases != "" {
		args = append(args, "--databases", databases)
	}
	if maxChars > 0 {
		args = append(args, "--max-chars", strconv.Itoa(maxChars))
	}
	if deep {
		args = append(args, "--deep")
	}
	if p.svc.ReadOnly() {
		args = append(args, "--read-only")
	}
	args = append(args, "--", question)
	var result service.QueryResult
	err := playground.JSON(ctx, args, &result)
	result.MaxChars = service.TextBudget(maxChars)
	return result, err
}
