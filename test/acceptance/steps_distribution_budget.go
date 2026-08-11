//go:build acceptance

package acceptance

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const budgetMarker = "budget-marker-"

func (w *distributionWorld) seedBudgetRow() error {
	if err := w.ensurePrepared(); err != nil {
		return err
	}
	content := budgetMarker + strings.Repeat("abcdefghij", 30)
	w.state["budgetFull"] = content
	result := w.run("store", "--layer", "discovery", "--content", content, "--origin", "agent")
	if result.code != 0 {
		return fmt.Errorf("seed budget row: %s", result.stderr)
	}
	return nil
}

func (w *distributionWorld) requestBudgetedRow(surface string) error {
	statement := "SELECT content FROM memories WHERE content LIKE '" + budgetMarker + "%'"
	var values []string
	switch surface {
	case "terminal":
		result := w.run("exec", statement, "--max-chars", "48")
		if result.code != 0 {
			return fmt.Errorf("terminal budget query: %s", result.stderr)
		}
		values = budgetedValues(result.stdout)
	case "MCP":
		if err := w.callTool("roca_exec", map[string]any{"sql": statement, "max_chars": 48}); err != nil {
			return err
		}
		if w.tool.StructuredContent != nil {
			return fmt.Errorf("the tool shipped a structured rows envelope: %v", w.tool.StructuredContent)
		}
		values = budgetedValues(renderedText(w.tool))
	default:
		return fmt.Errorf("unknown row surface %q", surface)
	}
	if len(values) == 0 {
		return fmt.Errorf("%s returned no budgeted text fields", surface)
	}
	w.state["budgetValues"] = values
	return nil
}

func budgetedValues(output string) []string {
	var values []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, budgetMarker) {
			values = append(values, line)
		}
	}
	return values
}

func (w *distributionWorld) budgetIsRespected() error {
	for _, value := range w.state["budgetValues"].([]string) {
		if utf8.RuneCountInString(value) > 48 {
			return fmt.Errorf("budgeted text has %d characters: %q", utf8.RuneCountInString(value), value)
		}
	}
	return nil
}
