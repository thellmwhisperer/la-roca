package agentcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// yamlFlowSpan finds the bytes of a flow mapping so they can be replaced. Getting
// its end wrong cuts an operator's YAML at the wrong offset, so the quote rules
// are YAML's own: inside double quotes a backslash escapes the next character,
// and inside single quotes it does not, where a doubled quote is one literal
// quote. The old scan applied backslash escaping to both and looked one byte
// BEFORE the span to decide.
func TestYamlFlowSpanEndsAtTheClosingBrace(t *testing.T) {
	const trailing = "  # keep me"
	for _, want := range []struct {
		name string
		line string
	}{
		{name: "plain", line: `mcp_servers: {a: {command: x}}  # keep me`},
		{name: "brace inside single quotes", line: `mcp_servers: {a: {command: 'x}y'}}  # keep me`},
		{name: "brace inside double quotes", line: `mcp_servers: {a: {command: "x}y"}}  # keep me`},
		{name: "doubled single quote", line: `mcp_servers: {a: {command: 'don''t'}}  # keep me`},
		{name: "trailing escaped backslash", line: `mcp_servers: {a: {command: "x\\"}}  # keep me`},
	} {
		t.Run(want.name, func(t *testing.T) {
			var document yaml.Node
			if err := yaml.Unmarshal([]byte(want.line+"\n"), &document); err != nil {
				t.Fatalf("the fixture is not YAML: %v", err)
			}
			servers := document.Content[0].Content[1]
			lines, offsets := splitLines(want.line)
			first, last := yamlFlowSpan(servers, want.line, lines, offsets)

			span := want.line[first:last]
			if span[0] != '{' || span[len(span)-1] != '}' {
				t.Errorf("span %q does not stop at the closing brace", span)
			}
			// The operator's trailing comment is not part of the mapping, and a
			// scan that runs to end of line would replace it away.
			if got := want.line[last:]; got != trailing {
				t.Errorf("span swallowed %q: it is %q", got, span)
			}
		})
	}
}

func TestYamlFlowSpanCoversMultilineMappings(t *testing.T) {
	const before = "mcp_servers: {\n  roca: {command: old, args: [mcp, serve]}\n}\nafter: keep\n"
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(before), &document); err != nil {
		t.Fatal(err)
	}
	servers := document.Content[0].Content[1]
	lines, offsets := splitLines(before)
	first, last := yamlFlowSpan(servers, before, lines, offsets)
	if got := before[first:last]; !strings.HasSuffix(got, "\n}") {
		t.Fatalf("span = %q", got)
	}
	if got := before[last:]; got != "\nafter: keep\n" {
		t.Fatalf("trailing content = %q", got)
	}
}

// A flow mapping that already holds somebody else's server took the block-map
// path, which appends block YAML lines under a value written inline. The result
// is not the operator's file with one server added: it is a document that no
// longer parses, and their server is gone with it.
func TestAFlowMappingWithOtherServersIsNotSilentlyBroken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	const before = "mcp_servers: {other: {command: theirs, args: [x]}}\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Install(RuntimeHermes, path, "roca")

	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err != nil {
		// A refusal is a fine answer, as long as it names the remedy and the
		// operator's bytes are untouched.
		if string(after) != before {
			t.Errorf("it refused AND edited the file:\n%s", after)
		}
		if !strings.Contains(err.Error(), "mcp_servers") {
			t.Errorf("the refusal does not name the key: %v", err)
		}
		return
	}
	// If it did edit, the result has to still be YAML and still hold their server.
	var document map[string]any
	if err := yaml.Unmarshal(after, &document); err != nil {
		t.Fatalf("the edit left the file unparseable: %v\n%s", err, after)
	}
	servers, _ := document["mcp_servers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Errorf("the operator's own server was lost:\n%s", after)
	}
	// The declaration belongs UNDER the servers key. Written at the document's
	// top level it is a key Hermes never reads, and the command still said it
	// succeeded.
	if _, ok := servers[ServerName]; !ok {
		t.Errorf("the declaration did not land under mcp_servers:\n%s", after)
	}
	if _, stray := document[ServerName]; stray {
		t.Errorf("the declaration landed at the document's top level:\n%s", after)
	}
}
