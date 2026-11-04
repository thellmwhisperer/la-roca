//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"github.com/thellmwhisperer/la-roca/pkg/resident"
)

// A core resident that an MCP session started, then config.toml or the roca
// executable changes underneath it. The resident must retire rather than
// answer with what it read when it started. The store suite runs these with
// the stub companion; the e2e-federation journey with the pinned model.

func registerResidentConfigSteps(ctx *godog.ScenarioContext, m *world) {
	ctx.Given(`^an MCP session has called roca_query "([^"]*)" once$`, func(query string) error {
		if err := m.shortResidentSockets(); err != nil {
			return err
		}
		return m.callTool("roca_query", map[string]any{"query": query})
	})
	ctx.When(`^the MCP session calls roca_query "([^"]*)" again$`, func(query string) error {
		return m.callTool("roca_query", map[string]any{"query": query})
	})
	ctx.Then(`^the MCP answer is not an error and holds "([^"]*)"$`, func(text string) error {
		if err := m.theResponseIsNotAnError(); err != nil {
			return err
		}
		return m.theReadableMCPResponseContains(text)
	})
	ctx.When(`^that MCP session is closed and its resident let it go$`, m.mcpSessionLetGo)
	ctx.When(`^vector is switched on under features$`, m.vectorSwitchedOn)
	ctx.When(`^vector is switched off under features$`, func() error {
		return m.switchFeature("vector = true", "vector = false")
	})
	ctx.Then(`^the resident line names the version roca --version prints, "([^"]*)" and "([^"]*)"$`,
		func(vector, serving string) error {
			version, _, err := rocaVersion(m.binaryPath(), m.environment())
			if err != nil {
				return err
			}
			for _, line := range strings.Split(m.last.stdout, "\n") {
				if strings.HasPrefix(line, "resident: pid=") {
					for _, want := range []string{"version " + version + " ", vector, serving} {
						if !strings.Contains(line, want) {
							return fmt.Errorf("the resident line lacks %q: %s", want, line)
						}
					}
					return nil
				}
			}
			return fmt.Errorf("doctor printed no running resident:\n%s", m.last.stdout)
		})
	ctx.Then(`^the JSON resident names that build, config.toml, vector (true|false), roca_ops (true|false) and serving "([^"]*)"$`,
		func(vector, rocaOps, serving string) error {
			version, commit, err := rocaVersion(m.binaryPath(), m.environment())
			if err != nil {
				return err
			}
			want := map[string]string{
				"resident.version": version, "resident.commit": commit,
				"resident.config_path": filepath.Join(m.home, ".roca", "config.toml"),
				"resident.vector":      vector, "resident.roca_ops": rocaOps, "resident.serving": serving,
			}
			for key, value := range want {
				if err := m.jsonKeyEqualTo(key, value); err != nil {
					return err
				}
			}
			return nil
		})
	ctx.Given(`^an MCP session started with a PATH without ~/\.local/bin has called roca_query "([^"]*)" once$`,
		func(query string) error {
			if err := m.shortResidentSockets(); err != nil {
				return err
			}
			if err := m.pathLacksLocalBin(); err != nil {
				return err
			}
			if err := m.callTool("roca_query", map[string]any{"query": query}); err != nil {
				return err
			}
			return m.theResponseIsNotAnError()
		})
	ctx.Then(`^the MCP query hits hold "([^"]*)" with "vector" among their legs$`, func(text string) error {
		if err := m.theResponseIsNotAnError(); err != nil {
			return err
		}
		answer := renderedText(m.plug.last)
		for _, line := range strings.Split(answer, "\n") {
			// A hit row is rank,source,legs,...,snippet; the snippet does not say vector.
			if strings.Contains(line, text) && strings.Contains(line, "vector") {
				return nil
			}
		}
		return fmt.Errorf("no MCP query hit holds %q with a vector leg:\n%s", text, answer)
	})
	ctx.When(`^the operator copies another build of roca over ~/\.local/bin/roca without the installer$`, func() error {
		other, err := devStampedBinary()
		if err != nil {
			return err
		}
		// A new file renamed over the installed one: darwin kills a binary
		// rewritten in place, whatever build it is.
		installed := theInstalledBinary(m.home)
		if err := os.WriteFile(installed+".new", mustRead(other), 0o755); err != nil {
			return err
		}
		return os.Rename(installed+".new", installed)
	})
	ctx.Then(`^roca doctor reports a resident whose version is the one roca --version prints, or no resident$`, func() error {
		version, _, err := rocaVersion(theInstalledBinary(m.home), m.environment())
		if err != nil {
			return err
		}
		for _, line := range strings.Split(m.last.stdout, "\n") {
			if line == "resident: down" || strings.HasPrefix(line, "resident: pid=") && strings.Contains(line, "version "+version+" ") {
				return nil
			}
		}
		return fmt.Errorf("doctor reports a resident that is not %s:\n%s", version, m.last.stdout)
	})
	ctx.Then(`^the JSON output has no resident or a resident with vector (true|false)$`, func(vector string) error {
		document, err := m.json()
		if err != nil {
			return err
		}
		if _, running := document["resident"]; !running {
			return nil
		}
		return m.jsonKeyEqualTo("resident.vector", vector)
	})
}

// mcpSessionLetGo closes the session and waits until the resident no longer
// holds it: the status call that asks is its only client.
func (m *world) mcpSessionLetGo() error {
	m.closeThePlug()
	options := resident.Options{Socket: m.residentSocket}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		var status resident.Status
		raw, err := resident.Call(context.Background(), options, "status", nil)
		if err == nil && json.Unmarshal(raw, &status) == nil && status.AttachedClients <= 1 {
			return nil
		}
	}
	return fmt.Errorf("the resident still holds the closed MCP session")
}

// rocaVersion is the version and commit `roca --version` prints:
// "roca <version> (<commit>) <os>/<arch>".
func rocaVersion(binary string, environment []string) (string, string, error) {
	command := exec.Command(binary, "--version")
	command.Env = environment
	out, err := command.Output()
	if err != nil {
		return "", "", fmt.Errorf("roca --version: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 3 {
		return "", "", fmt.Errorf("roca --version printed %q", out)
	}
	return fields[1], strings.Trim(fields[2], "()"), nil
}
