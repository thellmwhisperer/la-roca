//go:build acceptance

package acceptance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
		if err := m.openAgentSession(); err != nil {
			return err
		}
		return m.plug.agent.call(m, "roca_query", map[string]any{"query": query})
	})
	ctx.When(`^the MCP session calls roca_query "([^"]*)" again$`, func(query string) error {
		if m.plug.agent != nil {
			return m.plug.agent.call(m, "roca_query", map[string]any{"query": query})
		}
		return m.callTool("roca_query", map[string]any{"query": query})
	})
	ctx.Given(`^the MCP session has a roca_exec running that takes a second$`, func() error {
		if _, err := m.plug.agent.send("tools/call", map[string]any{"name": "roca_exec",
			"arguments": map[string]any{"sql": longSQL}}); err != nil {
			return err
		}
		// The resident admits it before config.toml changes.
		time.Sleep(100 * time.Millisecond)
		return nil
	})
	ctx.When(`^the MCP session sends roca_query "([^"]*)" while that roca_exec runs$`, func(query string) error {
		if _, err := m.plug.agent.send("tools/call", map[string]any{"name": "roca_query",
			"arguments": map[string]any{"query": query}}); err != nil {
			return err
		}
		// The retired resident refuses it while the roca_exec still runs.
		time.Sleep(100 * time.Millisecond)
		return nil
	})
	ctx.Then(`^every call the MCP session made was answered without an error$`, func() error {
		for _, id := range m.plug.agent.calls {
			result, err := m.plug.agent.result(id)
			if err != nil {
				return err
			}
			if result.IsError {
				return fmt.Errorf("call %s is an error: %s", id, renderedText(result))
			}
		}
		return nil
	})
	ctx.Then(`^the MCP answer is not an error and holds "([^"]*)"$`, func(text string) error {
		if err := m.theResponseIsNotAnError(); err != nil {
			return err
		}
		return m.theReadableMCPResponseContains(text)
	})
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

// longSQL keeps a roca_exec running for about a second, under exec's limit.
const longSQL = `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i + 1 FROM n WHERE i < 5000000) SELECT count(*) FROM n`

// agentSession speaks MCP over roca mcp serve as an installed agent does:
// initialize, notifications/initialized, then one JSON-RPC line per call. The
// SDK client opens with server/discover instead, so it never sends the
// handshake the shim replays to a new resident.
type agentSession struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	lines   chan []byte
	answers map[string]json.RawMessage
	calls   []string
	next    int
}

func (m *world) openAgentSession() error {
	command := exec.Command(m.binaryPath(), "mcp", "serve")
	command.Env = m.environment()
	command.Stderr = os.Stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	agent := &agentSession{command: command, stdin: stdin, lines: make(chan []byte), answers: map[string]json.RawMessage{}}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 64<<20)
		for scanner.Scan() {
			agent.lines <- bytes.Clone(scanner.Bytes())
		}
		close(agent.lines)
	}()
	m.plug.agent = agent
	id, err := agent.send("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "acceptance-agent", "version": "1"}})
	if err == nil {
		_, err = agent.answer(id)
	}
	if err == nil {
		err = agent.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	}
	return err
}

func (a *agentSession) write(message map[string]any) error {
	line, err := json.Marshal(message)
	if err == nil {
		_, err = a.stdin.Write(append(line, '\n'))
	}
	return err
}

// send writes one request and returns its id; a tools/call is remembered.
func (a *agentSession) send(method string, params any) (string, error) {
	a.next++
	id := fmt.Sprint(a.next)
	if method == "tools/call" {
		a.calls = append(a.calls, id)
	}
	return id, a.write(map[string]any{"jsonrpc": "2.0", "id": a.next, "method": method, "params": params})
}

// answer is the response to id, keeping the others it reads on the way.
func (a *agentSession) answer(id string) (json.RawMessage, error) {
	timeout := time.After(30 * time.Second)
	for {
		if answer, ok := a.answers[id]; ok {
			return answer, nil
		}
		select {
		case line, open := <-a.lines:
			if !open {
				return nil, fmt.Errorf("roca mcp serve closed stdout before answering %s", id)
			}
			var envelope struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(line, &envelope) == nil && len(envelope.ID) > 0 {
				a.answers[string(envelope.ID)] = line
			}
		case <-timeout:
			return nil, fmt.Errorf("no answer to %s in 30s", id)
		}
	}
}

// result is the tool result answered to id; a JSON-RPC error is an error.
func (a *agentSession) result(id string) (*mcp.CallToolResult, error) {
	line, err := a.answer(id)
	if err != nil {
		return nil, err
	}
	var response struct {
		Result *mcp.CallToolResult `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return nil, err
	}
	if response.Error != nil || response.Result == nil {
		return nil, fmt.Errorf("call %s got no result: %s", id, line)
	}
	return response.Result, nil
}

func (a *agentSession) call(m *world, name string, arguments map[string]any) error {
	id, err := a.send("tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return err
	}
	result, err := a.result(id)
	if err != nil {
		return err
	}
	m.plug.last = result
	return nil
}

// close is the agent closing the pipe; roca mcp serve exits on its own.
func (a *agentSession) close() {
	a.stdin.Close()
	exited := make(chan error, 1)
	go func() { exited <- a.command.Wait() }()
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		_ = a.command.Process.Kill()
		<-exited
	}
}
