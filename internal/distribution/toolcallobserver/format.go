package toolcallobserver

import (
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

const outputBudget = 500

func Format(event parsers.CallEvent) string {
	stamp := displayTime(event.Timestamp)
	if event.IsResult {
		if isShell(event.Name) || event.Command != "" {
			body := bound(event.Output, outputBudget)
			if body == "" {
				body = "(no output)"
			}
			return stamp + "  output\n" + indent(body)
		}
		name := event.Name
		if name == "" {
			name = "tool"
		}
		return stamp + "  " + name + " result"
	}
	if isShell(event.Name) || event.Command != "" {
		return stamp + "  shell  " + bound(event.Command, axi.FieldWidth)
	}
	name := event.Name
	if name == "" {
		name = "tool"
	}
	detail := strings.TrimSpace(event.Params)
	if detail == "" {
		return stamp + "  " + name
	}
	return stamp + "  " + name + "  " + bound(detail, axi.FieldWidth)
}

func isShell(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "shell", "shell_command", "run_terminal_command":
		return true
	default:
		return false
	}
}

func displayTime(value string) string {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC().Format("2006-01-02 15:04:05")
		}
	}
	return value
}

func bound(text string, budget int) string {
	runes := []rune(text)
	if len(runes) <= budget {
		return text
	}
	return string(runes[:budget]) + " [truncated]"
}

func indent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return strings.Join(lines, "\n")
}
