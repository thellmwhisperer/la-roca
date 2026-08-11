package agentcfg

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// The YAML editor, for Hermes.
//
// yaml.v3 is used to find things, never to write them: a decode-and-reserialize
// round trip normalizes quoting, drops comments and reorders nothing an
// operator asked to be reordered. What it gives us is the line every node
// starts on, and from there the block a member owns is a line range, and
// editing it is editing bytes.

func yamlDeclare(r runtime, text string, entry fields) (string, error) {
	document, err := yamlDocument(text)
	if err != nil {
		return "", err
	}
	lines, offsets := splitLines(text)

	key := indexOf(document, r.serversKey)
	if key < 0 {
		// This runtime has never declared a server here, so the whole map goes
		// in at the end of the document.
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return text + r.serversKey + ":\n" + indent + ServerName + ":\n" +
			renderYAML(entry, indent+indent) + "\n", nil
	}
	servers := document.Content[key+1]
	keyLine := document.Content[key].Line - 1
	pad := indentOfLine(lines, keyLine) + indent
	rendered := pad + ServerName + ":\n" + renderYAML(entry, pad) + "\n"
	if servers.Kind == yaml.ScalarNode && servers.Tag == "!!null" && servers.Value == "" {
		return text[:offsets[keyLine]+len(lines[keyLine])] +
			rendered + tail(text, offsets, lines, keyLine), nil
	}
	if servers.Kind != yaml.MappingNode {
		return "", fmt.Errorf("%s must be a mapping", r.serversKey)
	}
	if servers.Style == yaml.FlowStyle && (len(servers.Content) == 0 ||
		(len(servers.Content) == 2 && servers.Content[0].Value == ServerName)) {
		line, first, last := yamlFlowSpan(servers, lines)
		return text[:offsets[line]+first] + renderYAMLFlow(entry) +
			text[offsets[line]+last:], nil
	}
	// A flow mapping holding somebody else's server is refused instead of edited,
	// the same way the TOML editor refuses an inline table. The block path below
	// appends lines under a value written inline, which put the declaration at the
	// document's TOP level: a key the runtime never reads, reported as a success.
	if servers.Style == yaml.FlowStyle {
		return "", fmt.Errorf(
			"%s is written as a flow mapping with entries already in it, and this "+
				"version only edits block mappings: rewrite it as a block mapping "+
				"and run the command again", r.serversKey)
	}

	// The new entry lines up with the servers already declared, and with the
	// key's own indentation plus one step when there are none.
	if len(servers.Content) > 0 {
		pad = indentOfLine(lines, servers.Content[0].Line-1)
	}
	rendered = pad + ServerName + ":\n" + renderYAML(entry, pad) + "\n"

	if i := indexOf(servers, ServerName); i >= 0 {
		first, last := spanOf(servers, i, lines)
		return text[:offsets[first]] + rendered + tail(text, offsets, lines, last), nil
	}
	// Appended at the end of the map, after its last member, or right under the
	// key itself when it has none.
	after := servers.Line - 1
	if n := len(servers.Content); n >= 2 {
		_, after = spanOf(servers, n-2, lines)
	}
	return text[:offsets[after]+len(lines[after])] +
		rendered + tail(text, offsets, lines, after), nil
}

func yamlRemove(r runtime, text string, entries []string) (string, error) {
	document, err := yamlDocument(text)
	if err != nil {
		return "", err
	}
	servers := mappingMember(document, r.serversKey)
	if servers == nil || servers.Kind != yaml.MappingNode {
		return text, nil
	}
	lines, offsets := splitLines(text)
	if servers.Style == yaml.FlowStyle && len(servers.Content) == 2 &&
		servers.Content[0].Value == ServerName {
		line, first, last := yamlFlowSpan(servers, lines)
		return text[:offsets[line]+first] + "{}" + text[offsets[line]+last:], nil
	}
	var firsts, lasts []int
	for _, name := range entries {
		if i := indexOf(servers, name); i >= 0 {
			f, l := spanOf(servers, i, lines)
			firsts, lasts = append(firsts, f), append(lasts, l)
		}
	}
	// Remove in reverse so earlier byte offsets stay valid — same principle as JSON.
	for i := len(firsts) - 1; i >= 0; i-- {
		text = text[:offsets[firsts[i]]] + tail(text, offsets, lines, lasts[i])
	}
	return text, nil
}

func renderYAMLFlow(entry fields) string {
	parts := make([]string, len(entry))
	for i, field := range entry {
		if values, ok := field.value.([]string); ok {
			quoted := make([]string, len(values))
			for j, value := range values {
				quoted[j] = yamlScalar(value)
			}
			parts[i] = field.key + ": [" + strings.Join(quoted, ", ") + "]"
		} else {
			parts[i] = field.key + ": " + yamlScalar(field.value)
		}
	}
	return "{" + ServerName + ": {" + strings.Join(parts, ", ") + "}}"
}

func yamlFlowSpan(node *yaml.Node, lines []string) (int, int, int) {
	line, first := node.Line-1, node.Column-1
	depth, quote := 0, byte(0)
	for i := first; i < len(lines[line]); i++ {
		char := lines[line][i]
		if quote != 0 {
			// YAML's own rules, and they differ per quote style. Inside double
			// quotes a backslash escapes the next character; inside single quotes
			// it does not, and a doubled quote is one literal quote. Deciding it
			// with a look at the PREVIOUS byte also read outside the span, and
			// treating a single-quoted scalar as backslash-escaped left the quote
			// open so the scan ran past the mapping and swallowed whatever the
			// operator had written after it.
			switch {
			case quote == '"' && char == '\\':
				i++
			case quote == '\'' && char == '\'' &&
				i+1 < len(lines[line]) && lines[line][i+1] == '\'':
				i++
			case char == quote:
				quote = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
		} else if char == '{' {
			depth++
		} else if char == '}' {
			depth--
			if depth == 0 {
				return line, first, i + 1
			}
		}
	}
	return line, first, len(lines[line])
}

func yamlDecode(_ runtime, text string) (map[string]any, error) {
	var document map[string]any
	return document, yaml.Unmarshal([]byte(text), &document)
}

// yamlDocument reads the document's root mapping, refusing anything else before
// a single byte is written.
func yamlDocument(text string) (*yaml.Node, error) {
	var document yaml.Node
	if strings.TrimSpace(text) != "" {
		if err := yaml.Unmarshal([]byte(text), &document); err != nil {
			return nil, err
		}
	}
	if len(document.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode}, nil
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the configuration is not a YAML mapping")
	}
	return root, nil
}

// indexOf is where one member's key sits in the mapping's flat content, or -1.
// Everything here is indexed from the key and not from the value, because a
// block mapping's value starts on the line of its own first child, so the key's
// line is where the member starts in the text.
func indexOf(mapping *yaml.Node, key string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func mappingMember(mapping *yaml.Node, key string) *yaml.Node {
	if i := indexOf(mapping, key); i >= 0 {
		return mapping.Content[i+1]
	}
	return nil
}

// spanOf is the lines the i-th member of a mapping owns, from the line its key
// sits on to the last line of its value. A YAML node declares where it begins
// and never where it ends: the last line is the one before the next member's
// (blank lines and comments above that one belong to it), or, for the last
// member, the last line indented under it.
func spanOf(mapping *yaml.Node, i int, lines []string) (int, int) {
	first := mapping.Content[i].Line - 1
	if i+2 < len(mapping.Content) {
		last := mapping.Content[i+2].Line - 2
		for last > first {
			trimmed := strings.TrimSpace(lines[last])
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				break
			}
			last--
		}
		return first, last
	}
	pad := len(indentOfLine(lines, first))
	last := first
	for line := first + 1; line < len(lines); line++ {
		trimmed := strings.TrimSpace(lines[line])
		if trimmed == "" {
			continue
		}
		if len(indentOfLine(lines, line)) <= pad {
			break
		}
		last = line
	}
	return first, last
}

func indentOfLine(lines []string, index int) string {
	if index < 0 || index >= len(lines) {
		return ""
	}
	return lines[index][:len(lines[index])-len(strings.TrimLeft(lines[index], " \t"))]
}

// tail is everything from the line after last onwards.
func tail(text string, offsets []int, lines []string, last int) string {
	if last+1 >= len(lines) {
		return ""
	}
	return text[offsets[last+1]:]
}
