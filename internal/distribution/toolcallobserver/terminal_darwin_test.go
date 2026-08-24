//go:build darwin

package toolcallobserver

import (
	"strings"
	"testing"
)

func TestTerminalCommandQuotesEnvValues(t *testing.T) {
	got := terminalCommand(TerminalRequest{
		Cwd:     "/work/my dir",
		Env:     []string{"ROCA_TOOL_CALL_OBSERVER_FOLLOW=/path with space"},
		Command: []string{"roca", "tool-call-observer"},
	})
	if !strings.Contains(got, "export ROCA_TOOL_CALL_OBSERVER_FOLLOW='/path with space'") {
		t.Fatalf("env value is not quoted: %s", got)
	}
	if !strings.Contains(got, "cd '/work/my dir'") {
		t.Fatalf("cwd quoting changed: %s", got)
	}
}

func TestTerminalCommandQuotesEnvValuesWithQuoteCharacters(t *testing.T) {
	got := terminalCommand(TerminalRequest{
		Env:     []string{"ROCA_TOOL_CALL_OBSERVER_FOLLOW=/path/it's here"},
		Command: []string{"roca", "tool-call-observer"},
	})
	if !strings.Contains(got, `export ROCA_TOOL_CALL_OBSERVER_FOLLOW='/path/it'"'"'s here'`) {
		t.Fatalf("env value with a quote is not escaped: %s", got)
	}
}
