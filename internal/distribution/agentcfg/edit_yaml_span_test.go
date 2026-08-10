package agentcfg

import (
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
			line, first, last := yamlFlowSpan(servers, []string{want.line})

			span := want.line[first:last]
			if line != 0 {
				t.Fatalf("line = %d, want 0", line)
			}
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
