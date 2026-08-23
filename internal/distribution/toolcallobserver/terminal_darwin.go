//go:build darwin

package toolcallobserver

import (
	"os/exec"
	"strings"
)

func openOSTerminal(req TerminalRequest) error {
	script := "tell application \"Terminal\" to do script " + appleString(terminalCommand(req))
	return exec.Command("osascript", "-e", script).Run()
}

func terminalCommand(req TerminalRequest) string {
	var parts []string
	if req.Cwd != "" {
		parts = append(parts, "cd "+shellQuote(req.Cwd))
	}
	for _, env := range req.Env {
		parts = append(parts, "export "+quoteEnvValue(env))
	}
	parts = append(parts, shellJoin(req.Command))
	return strings.Join(parts, " && ")
}

func appleString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(value) + `"`
}

func quoteEnvValue(assignment string) string {
	key, value, found := strings.Cut(assignment, "=")
	if !found {
		return assignment
	}
	return key + "=" + shellQuote(value)
}

func shellQuote(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `'"'"'`) + `'`
}

func shellJoin(command []string) string {
	parts := make([]string, len(command))
	for i, part := range command {
		parts[i] = shellQuote(part)
	}
	return strings.Join(parts, " ")
}
