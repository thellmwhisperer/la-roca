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
	"runtime"
	"strings"
	"time"

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
	"roca_exec", "roca_handoff_latest", "roca_health", "roca_pill_show",
	"roca_query", "roca_store",
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
	session    *mcp.ClientSession
	tools      *mcp.ListToolsResult
	last       *mcp.CallToolResult
	clientName string
	elapsed    time.Duration
}

const sessionHandoffContent = "branch: fixture\ndone: recorded\nstate: stored\nnext: continue"

func registerMCPSteps(ctx *godog.ScenarioContext, m *world) {
	ctx.Given(`^La Roca is in read-only mode$`, m.inReadOnlyMode)
	ctx.Given(`^the agent "([^"]*)" has its configuration file with content of its own$`,
		m.anAgentWithItsOwnConfiguration)

	ctx.When(`^I open an MCP session over stdio against the binary$`, m.openThePlug)
	ctx.When(`^I open an MCP session as client "([^"]*)"$`, m.iOpenAnMCPSessionAsClient)
	ctx.When(`^I store a session handoff over MCP with the same content the CLI accepts$`,
		m.iStoreASessionHandoffOverMCP)
	ctx.When(`^I store the same session handoff through the CLI$`,
		m.iStoreTheSameSessionHandoffThroughTheCLI)
	ctx.When(`^I send "initialize"$`, m.iSendInitialize)
	ctx.When(`^I send "tools/list"$`, m.iAskForTheTools)
	ctx.When(`^I call the query tool with the question "([^"]*)"$`, m.iCallQuery)
	ctx.When(`^I call the exec tool with the SQL "([^"]*)"$`, m.iCallExecSQL)
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
	ctx.Then(`^the readable response contains "([^"]*)"$`, m.theReadableResponseContains)
	ctx.Then(`^the MCP call finished within (\d+) seconds$`, m.theMCPCallFinishedWithinSeconds)
	ctx.Then(`^the count has gone up by one$`, m.theCountHasGoneUpByOne)
	ctx.Then(`^the identity card of that write declares it came from the plug$`,
		m.theIdentityCardSaysItCameFromThePlug)
	ctx.Then(`^that handoff is stored with agent "([^"]*)" and surface mcp$`,
		m.theHandoffIsStoredWithAgent)
	ctx.Then(`^the refusal names the agent, surface, origin and why it was refused$`,
		m.theRefusalNamesTheHandoffWriter)
	ctx.Then(`^the MCP store audit names agent, surface and origin$`,
		m.theMCPStoreAuditNamesAuthorship)
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
	if m.plug.clientName == "" {
		m.plug.clientName = "acceptance"
	}
	return m.openThePlugAs(m.plug.clientName)
}

func (m *world) iOpenAnMCPSessionAsClient(name string) error {
	return m.openThePlugAs(name)
}

func (m *world) openThePlugAs(name string) error {
	if m.plug.session != nil && m.plug.clientName == name {
		return nil
	}
	if m.plug.session != nil {
		m.closeThePlug()
	}
	if m.residentSocket == "" {
		socket, cleanup, err := acceptanceResident()
		if err != nil {
			return err
		}
		m.residentSocket, m.residentCleanup = socket, cleanup
	}
	command := exec.Command(m.binaryPath(), "mcp", "serve")
	command.Env = m.environment()
	command.Stderr = os.Stderr

	client := mcp.NewClient(&mcp.Implementation{Name: name, Version: "1"}, nil)
	session, err := client.Connect(context.Background(),
		&mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return fmt.Errorf("open the MCP session: %w", err)
	}
	m.plug.session = session
	m.plug.clientName = name
	return nil
}

func (m *world) iStoreASessionHandoffOverMCP() error {
	return m.callTool("roca_store", map[string]any{
		"layer":   "handoff",
		"content": sessionHandoffContent,
	})
}

func (m *world) iStoreTheSameSessionHandoffThroughTheCLI() error {
	_, err := m.runWith("roca store session handoff", []string{
		"store", "--layer", "handoff", "--content", sessionHandoffContent,
		"--origin", "agent", "--agent", "claude", "--model", "sonnet",
	})
	return err
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

func (m *world) iCallExecSQL(statement string) error {
	return m.callTool("roca_exec", map[string]any{"sql": statement})
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
	started := time.Now()
	result, err := m.plug.session.CallTool(context.Background(),
		&mcp.CallToolParams{Name: name, Arguments: arguments})
	m.plug.elapsed = time.Since(started)
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

func (m *world) theReadableResponseContains(want string) error {
	text := renderedText(m.plug.last)
	if !strings.Contains(text, want) {
		return fmt.Errorf("readable response does not contain %q: %q", want, text)
	}
	return nil
}

func (m *world) theMCPCallFinishedWithinSeconds(seconds int) error {
	limit := time.Duration(seconds) * time.Second
	if m.plug.elapsed > limit {
		return fmt.Errorf("MCP call took %s, want <= %s", m.plug.elapsed, limit)
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

func (m *world) theIdentityCardSaysItCameFromThePlug() error {
	db, err := m.openRocaOpsDB()
	if err != nil {
		return err
	}
	defer db.Close()
	var surface string
	err = db.QueryRow(
		`SELECT source_surface FROM memories ORDER BY id DESC LIMIT 1`).Scan(&surface)
	if err != nil {
		return fmt.Errorf("read the identity card: %w", err)
	}
	if surface != "mcp" {
		return fmt.Errorf("the identity card says surface %q, not mcp", surface)
	}
	return nil
}

func (m *world) theHandoffIsStoredWithAgent(agent string) error {
	db, err := m.openRocaOpsDB()
	if err != nil {
		return err
	}
	defer db.Close()
	var storedAgent, surface string
	err = db.QueryRow(
		`SELECT source_agent, source_surface FROM memories WHERE content = ? ORDER BY id DESC LIMIT 1`,
		sessionHandoffContent).Scan(&storedAgent, &surface)
	if err != nil {
		return fmt.Errorf("read the stored handoff: %w", err)
	}
	if storedAgent != agent || surface != "mcp" {
		return fmt.Errorf("stored authorship = %q via %q, want %q via mcp", storedAgent, surface, agent)
	}
	return nil
}

func (m *world) theRefusalNamesTheHandoffWriter() error {
	refused := renderedText(m.plug.last)
	agent := strings.ToLower(m.plug.clientName)
	want := fmt.Sprintf(`handoff refused: agent=%q surface=%q origin=%q`, agent, "mcp", "agent")
	for _, phrase := range []string{want, "session writers are", "tasks-axi"} {
		if !strings.Contains(refused, phrase) {
			return fmt.Errorf("the refusal does not name %q: %s", phrase, refused)
		}
	}
	return nil
}

func (m *world) theMCPStoreAuditNamesAuthorship() error {
	matches, err := filepath.Glob(filepath.Join(m.home, ".roca", "logs", "executions-*.jsonl"))
	if err != nil || len(matches) == 0 {
		return fmt.Errorf("MCP audit logs = %v, err=%v", matches, err)
	}
	raw, err := os.ReadFile(matches[len(matches)-1])
	if err != nil {
		return err
	}
	var lastStore string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, `"tool":"roca_store"`) {
			lastStore = line
		}
	}
	if lastStore == "" {
		return fmt.Errorf("no roca_store audit row in %s", raw)
	}
	for _, field := range []string{`"agent":`, `"surface":`, `"origin":`} {
		if !strings.Contains(lastStore, field) {
			return fmt.Errorf("store audit lacks %s: %s", field, lastStore)
		}
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
	if _, err := os.Stat(m.agentConfig + ".roca.bak"); err != nil {
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
	case "claude-desktop":
		return claudeDesktopConfigPath(home), `{
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
	case "zcode":
		return filepath.Join(home, ".zcode", "cli", "config.json"), `{
  "theme": "dark",
  "mcp": {
    "servers": {
      "other-server": {
        "type": "stdio",
        "command": "other-binary"
      }
    }
  }
}
`, nil
	default:
		return "", "", fmt.Errorf("I do not know the runtime %q", agent)
	}
}

func claudeDesktopConfigPath(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Claude",
			"claude_desktop_config.json")
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "Claude",
			"claude_desktop_config.json")
	default:
		return filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")
	}
}

// --- reading a protocol answer ---

func renderedText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var rendered strings.Builder
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok {
			continue
		}
		if rendered.Len() > 0 {
			rendered.WriteByte('\n')
		}
		rendered.WriteString(text.Text)
	}
	return rendered.String()
}
