//go:build linux

package toolcallobserver

import (
	"fmt"
	"os"
	"os/exec"
)

func openOSTerminal(req TerminalRequest) error {
	command := req.Command
	if len(command) == 0 {
		return fmt.Errorf("no observer command to run")
	}
	env := os.Environ()
	env = append(env, req.Env...)
	candidates := [][]string{
		{"x-terminal-emulator", "-e"},
		{"gnome-terminal", "--"},
		{"konsole", "-e"},
		{"xfce4-terminal", "-e"},
		{"xterm", "-e"},
	}
	if custom := os.Getenv("TERMINAL"); custom != "" {
		candidates = append([][]string{{custom, "-e"}}, candidates...)
	}
	var last error
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate[0]); err != nil {
			last = err
			continue
		}
		args := append(append([]string{}, candidate[1:]...), command...)
		cmd := exec.Command(candidate[0], args...)
		cmd.Env = env
		cmd.Dir = req.Cwd
		if err := cmd.Start(); err != nil {
			last = err
			continue
		}
		return nil
	}
	if last == nil {
		last = fmt.Errorf("no terminal program was found")
	}
	return last
}
