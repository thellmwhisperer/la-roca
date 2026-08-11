//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/cucumber/godog"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The steps about the plug. They stand apart because they are the only ones
// that speak a protocol instead of reading standard output.
//
// The suite stays black box: what is imported here is the protocol's own client
// SDK, which is what any third-party agent would use, and not one symbol of the
// product. The server is the real binary, launched as a subprocess exactly as
// an agent would launch it.

// theDecidedSurface is the tool list v1 published. It is written out here so
// that adding or withdrawing a tool has to be a decision somebody takes in two
// places, not a diff nobody read.
var theDecidedSurface = []string{
	"roca_exec", "roca_health", "roca_query", "roca_sql", "roca_store",
}

// theWithdrawnTools are the ones the pruning took out, with the command line
// that replaces each.
var theWithdrawnTools = map[string]string{
	"roca_list_runs": "roca health",
	"roca_inbox":     "roca query",
	"roca_proposals": "roca query",
	"roca_video":     "roca query",
	"roca_vision":    "roca query",
}

// plugWorld is the scenario's protocol session and what it last got back.
type plugWorld struct {
	session *mcp.ClientSession
	tools   *mcp.ListToolsResult
	last    *mcp.CallToolResult
}

func registerMCPSteps(ctx *godog.ScenarioContext, m *world) {
	ctx.Given(`^La Roca is in read-only mode$`, m.inReadOnlyMode)
	ctx.Given(`^the agent "([^"]*)" has its configuration file with content of its own$`,
		m.anAgentWithItsOwnConfiguration)

	ctx.When(`^I open an MCP session over stdio against the binary$`, m.openThePlug)
	ctx.When(`^I send "initialize"$`, m.iSendInitialize)
	ctx.When(`^I send "tools/list"$`, m.iAskForTheTools)
	ctx.When(`^I call the query tool with the question "([^"]*)"$`, m.iCallQuery)
	ctx.When(`^I call the query tool over stdio with the question "([^"]*)"$`, m.iCallQuery)
	ctx.When(`^I call the query tool with no arguments$`, m.iCallQueryWithNoArguments)
	ctx.When(`^I call the store tool over stdio with a new memory$`, m.iCallStore)
	ctx.When(`^I call the store tool over stdio$`, m.iCallStore)

	ctx.Then(`^the response declares the server name$`, m.itDeclaresTheServerName)
	ctx.Then(`^the response declares the product version, not a library's$`,
		m.itDeclaresTheProductVersion)
	ctx.Then(`^the response declares the supported protocol version$`,
		m.itDeclaresTheProtocolVersion)
	ctx.Then(`^the process exits when standard input is closed$`, m.itDiesWithThePipe)
	ctx.Then(`^the response lists exactly the tools decided for v1$`, m.exactlyTheDecidedTools)
	ctx.Then(`^every tool has a non-empty description$`, m.everyToolIsDescribed)
	ctx.Then(`^every tool has a valid input schema$`, m.everyToolHasASchema)
	ctx.Then(`^no tool that is not on the decided list appears$`, m.exactlyTheDecidedTools)
	ctx.Then(`^no withdrawn tool appears in the list$`, m.noWithdrawnTool)
	ctx.Then(`^for every withdrawn tool the command line that replaces it exists$`,
		m.everyWithdrawnToolHasItsCommand)
	ctx.Then(`^the response is not an error$`, m.theResponseIsNotAnError)
	ctx.Then(`^the response is a tool error$`, m.theResponseIsAToolError)
	ctx.Then(`^the response is an error$`, m.theResponseIsAToolError)
	ctx.Then(`^the response names the missing argument$`, m.itNamesTheMissingArgument)
	ctx.Then(`^the session is still alive$`, m.theSessionIsStillAlive)
	ctx.Then(`^a correct call right after it works$`, m.aCorrectCallAfterItWorks)
	ctx.Then(`^the response carries no structured content$`, m.theResponseCarriesNoStructuredContent)
	ctx.Then(`^the readable response is plain AXI text$`, m.theReadableResponseIsPlainAXI)
	ctx.Then(`^the count has gone up by one$`, m.theCountHasGoneUpByOne)
	ctx.Then(`^the audit record of that write declares it came from the plug$`,
		m.theAuditSaysItCameFromThePlug)
	ctx.Then(`^that error says the same as the command line said$`, m.bothSurfacesRefuseAlike)
	ctx.Then(`^the output names read-only mode and the refused operation$`,
		m.itNamesReadOnlyModeAndTheOperation)

	ctx.Then(`^the configuration of "([^"]*)" contains an MCP server entry for Roca$`,
		m.theConfigurationCarriesRoca)
	ctx.Then(`^all the previous content of that configuration is preserved byte for byte$`,
		m.thePreviousContentSurvives)
	ctx.Then(`^a backup of the previous file exists$`, m.aBackupOfTheConfigurationExists)
}

// --- the protocol session ---

// openThePlug launches the real binary as an agent would and speaks MCP to it
// over its standard input and output.
func (m *world) openThePlug() error {
	if m.plug.session != nil {
		return nil
	}
	command := exec.Command(m.binary, "mcp", "serve")
	command.Env = m.environment()
	command.Stderr = os.Stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "1"}, nil)
	session, err := client.Connect(context.Background(),
		&mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return fmt.Errorf("open the MCP session: %w", err)
	}
	m.plug.session = session
	return nil
}

// iSendInitialize is a no-op with a purpose: connecting is what performs the
// handshake, and the scenario asks about what it answered.
func (m *world) iSendInitialize() error { return m.openThePlug() }

func (m *world) iAskForTheTools() error {
	if err := m.openThePlug(); err != nil {
		return err
	}
	tools, err := m.plug.session.ListTools(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("tools/list: %w", err)
	}
	m.plug.tools = tools
	return nil
}

func (m *world) iCallQuery(question string) error {
	return m.callTool("roca_query", map[string]any{
		"query": question,
	})
}

func (m *world) iCallQueryWithNoArguments() error {
	return m.callTool("roca_query", map[string]any{})
}

func (m *world) iCallStore() error {
	return m.callTool("roca_store", map[string]any{
		"layer":   "discovery",
		"content": "a synthetic memory written through the protocol",
	})
}

func (m *world) callTool(name string, arguments map[string]any) error {
	if err := m.openThePlug(); err != nil {
		return err
	}
	result, err := m.plug.session.CallTool(context.Background(),
		&mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return fmt.Errorf("call %s: %w", name, err)
	}
	m.plug.last = result
	return nil
}

func (m *world) closeThePlug() {
	if m.plug.session != nil {
		m.plug.session.Close()
	}
	m.plug = plugWorld{}
}

// --- the assertions ---

func (m *world) itDeclaresTheServerName() error {
	if name := m.plug.session.InitializeResult().ServerInfo.Name; name != "roca" {
		return fmt.Errorf("server name = %q, want roca", name)
	}
	return nil
}

// The version announced is the product's. An agent reading back a library
// version has been told nothing about what answered it.
func (m *world) itDeclaresTheProductVersion() error {
	declared := m.plug.session.InitializeResult().ServerInfo.Version
	if declared == "" {
		return fmt.Errorf("the handshake declares no version")
	}
	reported, err := m.run("roca version --json")
	if err != nil {
		return err
	}
	var build map[string]any
	if err := json.Unmarshal([]byte(reported.stdout), &build); err != nil {
		return err
	}
	if fmt.Sprint(build["version"]) != declared {
		return fmt.Errorf("the plug declares %q and the binary %v",
			declared, build["version"])
	}
	return nil
}

func (m *world) itDeclaresTheProtocolVersion() error {
	version := m.plug.session.InitializeResult().ProtocolVersion
	if version == "" {
		return fmt.Errorf("the handshake declares no protocol version")
	}
	// A date, which is how every revision of this protocol is spelled.
	if len(version) != len("2026-07-28") || strings.Count(version, "-") != 2 {
		return fmt.Errorf("protocol version = %q, which is not a revision of the protocol",
			version)
	}
	return nil
}

// The server is born with the pipe and dies with it: there is no daemon, so
// closing standard input is the whole of its lifecycle.
func (m *world) itDiesWithThePipe() error {
	if m.plug.session == nil {
		return fmt.Errorf("there is no open session")
	}
	session := m.plug.session
	m.plug.session = nil
	if err := session.Close(); err != nil {
		return fmt.Errorf("the server did not exit when the pipe closed: %w", err)
	}
	return nil
}

func (m *world) exactlyTheDecidedTools() error {
	if m.plug.tools == nil {
		if err := m.iAskForTheTools(); err != nil {
			return err
		}
	}
	var names []string
	for _, tool := range m.plug.tools.Tools {
		names = append(names, tool.Name)
	}
	if !reflect.DeepEqual(names, theDecidedSurface) {
		return fmt.Errorf("tools = %v, want %v", names, theDecidedSurface)
	}
	return nil
}

func (m *world) everyToolIsDescribed() error {
	for _, tool := range m.plug.tools.Tools {
		if strings.TrimSpace(tool.Description) == "" {
			return fmt.Errorf("the tool %q has no description", tool.Name)
		}
	}
	return nil
}

func (m *world) everyToolHasASchema() error {
	for _, tool := range m.plug.tools.Tools {
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			return fmt.Errorf("the tool %q has no input schema", tool.Name)
		}
		if schema["type"] != "object" {
			return fmt.Errorf("the schema of %q is not an object: %v", tool.Name, schema)
		}
	}
	return nil
}

func (m *world) noWithdrawnTool() error {
	for _, tool := range m.plug.tools.Tools {
		if _, withdrawn := theWithdrawnTools[tool.Name]; withdrawn {
			return fmt.Errorf("the withdrawn tool %q is published again", tool.Name)
		}
	}
	return nil
}

// A withdrawn tool is only withdrawn if what it did
// is still reachable, and the command that does it has to really exist.
func (m *world) everyWithdrawnToolHasItsCommand() error {
	for tool, command := range theWithdrawnTools {
		result, err := m.run(command + " --help")
		if err != nil {
			return err
		}
		if result.code != 0 {
			return fmt.Errorf("%q replaces the withdrawn %q and does not exist: %s",
				command, tool, result.stderr)
		}
	}
	return nil
}

func (m *world) theResponseIsNotAnError() error {
	if m.plug.last == nil {
		return fmt.Errorf("no tool has been called")
	}
	if m.plug.last.IsError {
		return fmt.Errorf("the response is an error: %s", renderedText(m.plug.last))
	}
	return nil
}

func (m *world) theResponseIsAToolError() error {
	if m.plug.last == nil {
		return fmt.Errorf("no tool has been called")
	}
	if !m.plug.last.IsError {
		return fmt.Errorf("the response is not an error: %s", renderedText(m.plug.last))
	}
	return nil
}

func (m *world) itNamesTheMissingArgument() error {
	if text := strings.ToLower(renderedText(m.plug.last)); !strings.Contains(text, "query") {
		return fmt.Errorf("the error does not name the missing argument: %s", text)
	}
	return nil
}

// The session survived a mistaken call, which is what tells a tool error from a
// protocol failure.
func (m *world) theSessionIsStillAlive() error {
	return m.plug.session.Ping(context.Background(), nil)
}

func (m *world) aCorrectCallAfterItWorks() error {
	if err := m.iCallQuery("how many memories are there"); err != nil {
		return err
	}
	return m.theResponseIsNotAnError()
}

func (m *world) theResponseCarriesNoStructuredContent() error {
	if m.plug.last.StructuredContent != nil {
		return fmt.Errorf("the MCP response carries structured content: %v", m.plug.last.StructuredContent)
	}
	return nil
}

func (m *world) theReadableResponseIsPlainAXI() error {
	text := strings.TrimSpace(renderedText(m.plug.last))
	if text == "" || strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		return fmt.Errorf("the readable response is not plain AXI text: %q", text)
	}
	return nil
}

func (m *world) theCountHasGoneUpByOne() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	rows, ok := document["rows"].([]any)
	if !ok || len(rows) == 0 {
		return fmt.Errorf("the count returned no rows")
	}
	first, _ := rows[0].(map[string]any)
	var total float64
	for _, value := range first {
		if number, ok := value.(float64); ok {
			total = number
		}
	}
	if int(total) != m.memories+1 {
		return fmt.Errorf("there are %d memories, want the %d there were plus one",
			int(total), m.memories)
	}
	return nil
}

// The audit is on the row itself: v1 has no audit table, and the record of who
// wrote a memory belongs with the memory.
func (m *world) theAuditSaysItCameFromThePlug() error {
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	var metadata string
	err = db.QueryRow(
		`SELECT metadata FROM memories ORDER BY id DESC LIMIT 1`).Scan(&metadata)
	if err != nil {
		return fmt.Errorf("read the audit: %w", err)
	}
	if !strings.Contains(metadata, `"surface":"mcp"`) {
		return fmt.Errorf("the audit %q does not say the write came from the plug", metadata)
	}
	return nil
}

// The shell's own rendering of the refusal: it names the mode and what it
// refused, so an operator reading it knows both why and what.
func (m *world) itNamesReadOnlyModeAndTheOperation() error {
	all := m.last.stdout + m.last.stderr
	for _, phrase := range []string{"read-only", "store"} {
		if !strings.Contains(all, phrase) {
			return fmt.Errorf("the output does not name %q: %s", phrase, all)
		}
	}
	return nil
}

// One refusal, rendered twice. The read-only message belongs to the service and
// neither surface rewrites it.
func (m *world) bothSurfacesRefuseAlike() error {
	fromThePlug := renderedText(m.plug.last)
	// The shell's refusal is the last command that ran: calling a tool over the
	// protocol is not a run of the binary and does not displace it.
	fromTheShell := m.last.stdout + m.last.stderr
	for _, phrase := range []string{"read-only", "store"} {
		if !strings.Contains(fromThePlug, phrase) {
			return fmt.Errorf("the plug's refusal does not name %q: %s", phrase, fromThePlug)
		}
		if !strings.Contains(fromTheShell, phrase) {
			return fmt.Errorf("the shell's refusal does not name %q: %s", phrase, fromTheShell)
		}
	}
	return nil
}

// --- the worlds ---

// inReadOnlyMode turns the operator's switch on for every command and every
// session of this scenario.
func (m *world) inReadOnlyMode() error {
	m.readOnly = true
	return nil
}

// anAgentWithItsOwnConfiguration writes a synthetic configuration in the shape
// that runtime really uses, with content of its own already in it. Synthetic on
// purpose: a fixture copied from a real machine would carry that machine's
// vocabulary into a public repository.
func (m *world) anAgentWithItsOwnConfiguration(agent string) error {
	path, content, err := configurationOf(agent, m.home)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	m.agentConfig = path
	m.agentConfigBefore = content
	m.agentConfigRuntime = agent
	return nil
}

func (m *world) theConfigurationCarriesRoca(agent string) error {
	result, err := m.run("roca mcp status " + agent)
	if err != nil {
		return err
	}
	if !strings.Contains(result.stdout, "configured") {
		return fmt.Errorf("%s does not carry Roca: %s", agent, result.stdout)
	}
	if !strings.Contains(result.stdout, "mcp serve") {
		return fmt.Errorf("the entry of %s does not launch the server: %s",
			agent, result.stdout)
	}
	return nil
}

// Byte for byte, measured the only way that is not a matter of opinion:
// withdrawing gives back the exact bytes that were there.
func (m *world) thePreviousContentSurvives() error {
	current, err := os.ReadFile(m.agentConfig)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(m.agentConfigBefore, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(string(current), line) {
			return fmt.Errorf("the line %q was lost from %s", line, m.agentConfig)
		}
	}
	if _, err := m.run("roca mcp uninstall " + m.agentConfigRuntime); err != nil {
		return err
	}
	after, err := os.ReadFile(m.agentConfig)
	if err != nil {
		return err
	}
	if string(after) != m.agentConfigBefore {
		return fmt.Errorf(
			"the configuration did not come back to what it was.\n--- before ---\n%s\n--- after ---\n%s",
			m.agentConfigBefore, after)
	}
	// And it is left installed, because the scenario is not over.
	_, err = m.run("roca mcp install " + m.agentConfigRuntime)
	return err
}

func (m *world) aBackupOfTheConfigurationExists() error {
	if _, err := os.Stat(m.agentConfig + ".bak"); err != nil {
		return fmt.Errorf("there is no backup of %s: %w", m.agentConfig, err)
	}
	return nil
}

// configurationOf is the synthetic fixture for one runtime, in its own format.
func configurationOf(agent, home string) (string, string, error) {
	switch agent {
	case "codex":
		return filepath.Join(home, ".codex", "config.toml"), `# The operator configuration
model = "gpt-5-codex"

[mcp_servers.other-server]
command = "other-binary"
`, nil
	case "claude":
		return filepath.Join(home, ".claude.json"), `{
  "numStartups": 42,
  "mcpServers": {
    "other-server": {
      "type": "stdio",
      "command": "other-binary"
    }
  }
}
`, nil
	case "opencode":
		return filepath.Join(home, ".config", "opencode", "opencode.json"), `{
  // OpenCode reads JSONC and this comment must survive
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "other-server": {
      "type": "local",
      "command": ["other-binary"],
      "enabled": true
    }
  }
}
`, nil
	case "hermes":
		return filepath.Join(home, ".hermes", "config.yaml"), `# Hermes configuration
runtime: hermes
mcp_servers:
  other-server:
    command: other-binary
`, nil
	case "pi":
		return filepath.Join(home, ".pi", "agent", "mcp.json"), `{
  "mcpServers": {
    "other-server": {
      "command": "other-binary"
    }
  }
}
`, nil
	default:
		return "", "", fmt.Errorf("I do not know the runtime %q", agent)
	}
}

// --- reading a protocol answer ---

func renderedText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
