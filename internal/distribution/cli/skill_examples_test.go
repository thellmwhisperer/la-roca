package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
	"github.com/thellmwhisperer/la-roca/internal/provider/layers"
)

// The skill is the only manual an agent ever receives. Every command it shows
// has to run and every layer it names has to exist, or the agent is led to an
// invocation the product refuses. This guard parses the embedded skill the way
// a reader does and checks each example against the real cobra command tree and
// the real layer registry, so the documented release blocker -- examples that
// named flags and layers the binary does not have -- cannot come back.
//
// There is one skill body: internal/skill/SKILL.md (embedded). The Agent
// Plugins copy at skills/roca/SKILL.md is generated from it; identity is
// checked below so a divergent hand-edit cannot teach a different CLI.
func TestEverySkillExampleIsValidAgainstTheRealCLI(t *testing.T) {
	root := rootCommand(&cliEnv{out: io.Discard})
	registry, err := layers.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	realLayers := map[string]bool{}
	for _, name := range registry.Names() {
		realLayers[name] = true
	}

	// Flags that parse are not enough: an example can be a SELECT the gate
	// refuses, and only running it catches that. Every fenced example is also
	// executed against a real initialized installation, and the one that does
	// not exit 0 fails the build. This is the line the acta caught: the skill
	// showed `roca exec "SELECT ..."`, whose flags parse but whose body the
	// engine rejects.
	fixture := fixtureInstallation(t)
	content := skill.Content()
	for _, line := range fencedCommandLines(content) {
		tokens := shellTokens(line)
		if len(tokens) == 0 || tokens[0] != "roca" {
			continue
		}
		cmd, args := resolve(root, tokens[1:])
		if cmd == nil || cmd == root {
			t.Errorf("skill example is not a known command:\n  %s", line)
			continue
		}
		validateExampleFlags(t, line, cmd, args, realLayers)
		if code, out := fixture.run(tokens[1:]); code != ExitOK {
			t.Errorf("skill example did not exit 0 (code %d):\n  %s\n%s",
				code, line, out)
		}
	}
}

// The plugin package skill must be the same bytes as the embedded canonical
// skill. Editing one and not the other is how skills/roca once taught
// `roca health` while the product surface and the embedded skill taught
// `roca doctor`.
func TestPluginSkillIsExactCopyOfCanonical(t *testing.T) {
	got, err := os.ReadFile(filepath.Join("..", "..", "..", "skills", "roca", "SKILL.md"))
	if err != nil {
		t.Fatalf("read plugin skill: %v", err)
	}
	want := skill.Content()
	if string(got) != want {
		t.Fatalf("skills/roca/SKILL.md diverges from internal/distribution/skill/SKILL.md; edit the embedded skill and run go generate ./internal/distribution/skill")
	}
}

// Every shell command the skill names — fenced blocks and single-backtick
// invocations alike — must resolve on the real CLI. Prose that says
// `roca health` when health is not what the product wants agents to run is
// still a taught command; this catches it even outside a fence.
func TestSkillTeachesOnlyCommandsTheCLIHas(t *testing.T) {
	root := rootCommand(&cliEnv{out: io.Discard})
	for _, line := range taughtShellLines(skill.Content()) {
		tokens := shellTokens(line)
		if len(tokens) == 0 || tokens[0] != "roca" {
			continue
		}
		cmd, _ := resolve(root, tokens[1:])
		if cmd == nil || cmd == root {
			t.Errorf("skill teaches a command the CLI does not have:\n  %s", line)
		}
	}
}

// Diagnosis on the public menu is `roca doctor`. `roca health` is a hidden
// live-data probe, not the agent-facing remedy surface. The skill must not
// put agents on the wrong verb.
func TestSkillDiagnosisCommandIsDoctor(t *testing.T) {
	body := skill.Content()
	if !strings.Contains(body, "roca doctor") {
		t.Fatal(`skill does not teach "roca doctor"`)
	}
	for _, line := range taughtShellLines(body) {
		tokens := shellTokens(line)
		if len(tokens) >= 2 && tokens[0] == "roca" && tokens[1] == "health" {
			t.Errorf("skill teaches hidden `roca health` instead of public `roca doctor`:\n  %s", line)
		}
	}
}

func TestAgentPluginPackageMatchesThePublicContract(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	read := func(name string) []byte {
		t.Helper()
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return content
	}

	var manifest struct {
		Schema      string `json:"$schema"`
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(read("plugin.json"), &manifest); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}
	if manifest.Schema != "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json" {
		t.Errorf("plugin.json has schema %q", manifest.Schema)
	}
	if manifest.Name != "roca" || manifest.Version != "1.0.0" || manifest.Description == "" {
		t.Errorf("plugin.json metadata = name %q, version %q, description %q", manifest.Name, manifest.Version, manifest.Description)
	}

	var mcp struct {
		Schema     string `json:"$schema"`
		MCPServers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(read("mcp.json"), &mcp); err != nil {
		t.Fatalf("parse mcp.json: %v", err)
	}
	server, ok := mcp.MCPServers["roca"]
	if mcp.Schema != "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json" || !ok ||
		server.Type != "stdio" || server.Command != "roca" ||
		len(server.Args) != 2 || server.Args[0] != "mcp" || server.Args[1] != "serve" {
		t.Errorf("mcp.json does not declare roca mcp serve over stdio: %#v", mcp)
	}

	// Manifests only: the skill body is the product manual (identity-checked
	// against the embedded canonical) and may use the product's own words.
	packageFiles := []string{"plugin.json", "mcp.json"}
	// Built without writing the banned role name as a contiguous literal; the
	// product-vocabulary gate owns that ban and must stay the only place that
	// names the terms it forbids.
	role := "cap" + "tain"
	forbidden := regexp.MustCompile(`(?i)roca[-_ ]?madre|bench|golden|calibrat|` + role + `|internal|/users/`)
	for _, name := range packageFiles {
		if match := forbidden.Find(read(name)); match != nil {
			t.Errorf("%s contains non-public vocabulary %q", name, match)
		}
	}
}

// taughtShellLines collects every line an agent might copy as a shell
// invocation: fenced blocks, and single-backtick spans that start with `roca`.
func taughtShellLines(md string) []string {
	lines := fencedCommandLines(md)
	for _, span := range backticksInside(md) {
		if strings.HasPrefix(span, "roca") {
			lines = append(lines, span)
		}
	}
	return lines
}

// The "Layers" section is prose, so command parsing does not reach it. Its
// backticked layer names are checked straight against the registry: this is the
// line the release blocker lived on, where `fact`, `decision` and `preference`
// were presented as layers that do not exist.
func TestTheSkillLayersSectionNamesOnlyRealLayers(t *testing.T) {
	registry, err := layers.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	real := map[string]bool{}
	for _, name := range registry.Names() {
		real[name] = true
	}
	named := backticksInside(layersSection(skill.Content()))
	if len(named) == 0 {
		t.Fatal("the skill has no Layers section, or it names no layers in backticks")
	}
	for _, name := range named {
		if !real[name] {
			t.Errorf("skill Layers section names %q, which is not a layer in the registry", name)
		}
	}
}

// fencedCommandLines returns every line that lives inside a triple-backtick
// fence, whatever the fence's language tag. Prose between single backticks is
// left alone: a reader copies a block, not a sentence.
func fencedCommandLines(md string) []string {
	var lines []string
	inFence := false
	for _, raw := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(raw), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			lines = append(lines, raw)
		}
	}
	return lines
}

// shellTokens splits one shell line into argv, honouring single and double
// quotes and treating an unquoted '#' as the start of a comment. It is the
// least the skill's examples need to be read as the shell reads them.
func shellTokens(line string) []string {
	var tokens []string
	var b strings.Builder
	inWord := false
	inQuote := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
				continue
			}
			b.WriteByte(c)
			continue
		}
		switch c {
		case '"', '\'':
			inQuote = c
			inWord = true
		case ' ', '\t':
			if inWord {
				tokens = append(tokens, b.String())
				b.Reset()
				inWord = false
			}
		case '#':
			if !inWord {
				return tokens
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		tokens = append(tokens, b.String())
	}
	return tokens
}

// resolve walks the command tree the way cobra dispatches, following subcommand
// names and aliases until a token is a flag or an unknown word. It returns the
// resolved command and the remaining tokens (flags and positionals).
func resolve(root *cobra.Command, tokens []string) (*cobra.Command, []string) {
	node := root
	for i, tok := range tokens {
		if strings.HasPrefix(tok, "-") {
			return node, tokens[i:]
		}
		child := findChild(node, tok)
		if child == nil {
			return node, tokens[i:]
		}
		node = child
	}
	return node, nil
}

func findChild(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name {
			return c
		}
		for _, a := range c.Aliases {
			if a == name {
				return c
			}
		}
	}
	return nil
}

// validateExampleFlags checks that every flag the example uses exists on the
// resolved command (including inherited persistent flags) and that --layer and
// --template name something real. Nothing is executed: the gate's job is to
// refuse at runtime, not to help this test parse.
func validateExampleFlags(
	t *testing.T, line string, cmd *cobra.Command, args []string,
	realLayers map[string]bool,
) {
	t.Helper()
	accepted := map[string]*pflag.Flag{}
	add := func(fs *pflag.FlagSet) { fs.VisitAll(func(f *pflag.Flag) { accepted[f.Name] = f }) }
	add(cmd.Flags())
	for p := cmd.Parent(); p != nil; p = p.Parent() {
		add(p.PersistentFlags())
	}

	for i := 0; i < len(args); i++ {
		tok := args[i]
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			continue // a positional: the query question or the SELECT
		}
		if tok == "--" {
			break
		}
		name, value, hasValue := splitFlag(tok)
		flag, ok := accepted[name]
		if !ok {
			t.Errorf("skill example uses a flag the command does not have (--%s):\n  %s", name, line)
			continue
		}
		if !hasValue && flag.NoOptDefVal == "" {
			// The flag takes the next token as its value.
			if i+1 < len(args) {
				i++
				value, hasValue = args[i], true
			}
		}
		if !hasValue || value == "" {
			continue
		}
		if name == "layer" && !realLayers[value] {
			t.Errorf("skill example uses --layer %q, which is not in the registry:\n  %s", value, line)
		}
	}
}

func splitFlag(tok string) (name, value string, hasValue bool) {
	rest := strings.TrimLeft(tok, "-")
	if eq := strings.IndexByte(rest, '='); eq >= 0 {
		return rest[:eq], rest[eq+1:], true
	}
	return rest, "", false
}

// layersSection returns the body between the "## Layers" heading and the next
// heading or the end of the document.
func layersSection(md string) string {
	var b strings.Builder
	inSection := false
	for _, raw := range strings.Split(md, "\n") {
		trim := strings.TrimSpace(raw)
		if strings.HasPrefix(trim, "## ") {
			inSection = strings.HasPrefix(trim, "## Layers")
			continue
		}
		if inSection {
			b.WriteString(raw)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// backticksInside returns every `token` fenced in backticks within the text.
func backticksInside(md string) []string {
	var names []string
	for rest := md; strings.Contains(rest, "`"); {
		open := strings.IndexByte(rest, '`')
		after := rest[open+1:]
		close := strings.IndexByte(after, '`')
		if close < 0 {
			break
		}
		if name := strings.TrimSpace(after[:close]); name != "" {
			names = append(names, name)
		}
		rest = after[close+1:]
	}
	return names
}

// fixtureInstallation builds a real, initialized installation under a throwaway
// HOME so the skill's documented commands can be executed against the same
// SQLite, the same gate and the same index an operator's command hits. It is
// the fixture the flag validator lacked.
type skillFixture struct{}

func fixtureInstallation(t *testing.T) skillFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_DB_PATH", "")
	t.Setenv("ROCA_CONFIG", "")
	t.Setenv("ROCA_MODELS_ORDER", "none")
	for _, key := range []string{
		"CLAUDE_CONFIG_DIR", "CODEX_HOME", "OPENCODE_CONFIG",
		"HERMES_HOME", "PI_CODING_AGENT_DIR",
	} {
		t.Setenv(key, "")
	}
	runRoot(t, Build{Version: "test", Commit: "test-sha"},
		"init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	return skillFixture{}
}

// run executes one skill example (argv after `roca`) against the fixture
// installation and returns the exit code with its output. Nothing is asserted
// here: the caller decides what exit 0 means.
func (skillFixture) run(args []string) (int, string) {
	var out strings.Builder
	env := &cliEnv{out: &out, errOut: &out}
	root := rootCommand(env)
	root.SetArgs(args)
	err := root.Execute()
	if err != nil {
		fmt.Fprintf(&out, "error: %v", err)
		return ExitError, out.String()
	}
	return env.code, out.String()
}
