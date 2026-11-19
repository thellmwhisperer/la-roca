//go:build acceptance && (darwin || linux)

package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/BurntSushi/toml"
)

// promptPlayground offers one capability whose acceptance sets a single
// config value, so the [y/N] answer is visible in config.toml.
const promptPlayground = `#!/bin/sh
case "$1" in
 capabilities) printf '%s\n' '[{"ID":"acceptance-prompt","Proposal":{"Alert":"synthetic prompt capability","Prompt":"Enable the synthetic capability?","Changes":[{"Kind":"set","Table":"models.acceptance","Key":"model","Value":"prompt-accepted"}]}}]' ;;
 probe) printf '%s\n' '{}' ;;
 *) printf 'unexpected playground fixture command: %s\n' "$1" >&2; exit 2 ;;
esac
`

// TestDoctorCapabilityPromptOnATerminal answers the doctor's [y/N] question
// through a real terminal: end of input and "n" decline and leave the
// configuration alone, "yes" applies the proposal's change.
func TestDoctorCapabilityPromptOnATerminal(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}
	home, _ := initializedDistributionHome(t, "roca-doctor-prompt-", binary)
	plugin := filepath.Join(home, ".roca", "plugins", "roca-playground")
	if err := os.MkdirAll(plugin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, "roca-playground"), []byte(promptPlayground), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".roca", "config.toml")
	readConfig := func() string {
		data, err := os.ReadFile(configPath)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return string(data)
	}
	before := readConfig()

	for _, answer := range []struct{ name, typed string }{{"end of input", "\x04"}, {"no", "n\n"}} {
		out, code := doctorOnTerminal(t, home, binary, answer.typed)
		if code != 0 {
			t.Fatalf("doctor answered %s: code %d\n%s", answer.name, code, out)
		}
		if !strings.Contains(out, "Enable the synthetic capability? [y/N]: ") {
			t.Fatalf("doctor answered %s did not ask the question:\n%s", answer.name, out)
		}
		if strings.Contains(out, "configuration updated") || readConfig() != before {
			t.Fatalf("doctor answered %s changed the configuration:\n%s", answer.name, out)
		}
	}

	out, code := doctorOnTerminal(t, home, binary, "yes\n")
	if code != 0 || !strings.Contains(out, "configuration updated: "+configPath) {
		t.Fatalf("doctor answered yes: code %d\n%s", code, out)
	}
	var config struct {
		Models map[string]struct{ Model string } `toml:"models"`
	}
	if _, err := toml.Decode(readConfig(), &config); err != nil {
		t.Fatalf("decode accepted configuration: %v", err)
	}
	if got := config.Models["acceptance"].Model; got != "prompt-accepted" {
		t.Fatalf("models.acceptance.model = %q after yes", got)
	}
}

// doctorOnTerminal runs `roca doctor` with stdin on a pseudo-terminal that has
// already received typed, and returns stdout and stderr together.
func doctorOnTerminal(t *testing.T, home, binary, typed string) (string, int) {
	t.Helper()
	master, slave, err := openPTY()
	if err != nil {
		t.Fatalf("open pseudo-terminal: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	if _, err := master.WriteString(typed); err != nil {
		t.Fatalf("type %q: %v", typed, err)
	}
	cmd := exec.Command(binary, "doctor")
	cmd.Env = distributionEnvironment(home, binary)
	cmd.Stdin = slave
	var output strings.Builder
	cmd.Stdout, cmd.Stderr = &output, &output
	err = cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return output.String(), exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run doctor: %v", err)
	}
	return output.String(), 0
}

// openPTY returns a pseudo-terminal pair so the real binary sees a terminal
// on stdin, the only condition under which it asks a [y/N] question.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	name, err := ptySlaveName(master)
	if err == nil {
		slave, err = os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	}
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
