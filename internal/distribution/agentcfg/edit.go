package agentcfg

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// indent is the two-space step JSON and YAML both write by hand.
const indent = "  "

// editor is the one contract the three formats answer:
//
//   - declare leaves exactly one Roca entry in the file, replacing the value in
//     place when it is already there so that a comment sitting above it stays
//     above it.
//   - remove takes out every entry it is given, and nothing else.
//   - decode reads the whole file with the format's own decoder. Reading what
//     is declared preserves nothing and so needs no byte ranges, which makes it
//     the one operation the three formats can answer the same way.
//
// JSON and JSONC share an editor: they differ only in whether the comments are
// blanked out of the view its scanner reads.
type editor struct {
	declare func(runtime, string, fields) (string, error)
	remove  func(runtime, string, []string) (string, error)
	decode  func(runtime, string) (map[string]any, error)
}

var editors = map[string]editor{
	kindTOML:  {tomlDeclare, tomlRemove, tomlDocument},
	kindYAML:  {yamlDeclare, yamlRemove, yamlDecode},
	kindJSON:  {jsonDeclare, jsonRemove, jsonDecode},
	kindJSONC: {jsonDeclare, jsonRemove, jsonDecode},
}

func declare(r runtime, text, executable string) (string, error) {
	return editors[r.kind].declare(r, text, r.entry(executable))
}

// withdraw removes the entry Roca owns.
func withdraw(r runtime, text string) (string, error) {
	return editors[r.kind].remove(r, text, []string{ServerName})
}

func withdrawCreated(r runtime, text string, created []string) (string, error) {
	if (r.kind == kindJSON || r.kind == kindJSONC) && len(r.parents) > 0 {
		return jsonRemoveCreated(r, text, []string{ServerName}, created)
	}
	return withdraw(r, text)
}

// installed answers what an operator wants to know at a glance: is Roca
// declared here, and which binary is this agent about to launch.
func installed(r runtime, text string) (string, bool, error) {
	if strings.TrimSpace(text) == "" {
		return "", false, nil
	}
	document, err := editors[r.kind].decode(r, text)
	if err != nil {
		return "", false, err
	}
	container := document
	for _, parent := range r.parents {
		raw, present := container[parent]
		if !present {
			return "", false, nil
		}
		next, ok := raw.(map[string]any)
		if !ok {
			return "", false, fmt.Errorf("%s must be an object", parent)
		}
		container = next
	}
	rawServers, present := container[r.serversKey]
	if !present {
		return "", false, nil
	}
	servers, ok := rawServers.(map[string]any)
	if !ok {
		if len(r.parents) > 0 {
			return "", false, fmt.Errorf("%s must be an object", r.serversKey)
		}
		return "", false, nil
	}
	entry, ok := servers[ServerName].(map[string]any)
	if !ok {
		return "", false, nil
	}
	arguments, _ := entry["args"].([]any)
	return commandLine(entry["command"], arguments), true, nil
}

// commandLine is the line the entry launches, in the two spellings the runtimes
// use: a command with its `args` apart, and OpenCode's single array holding both.
func commandLine(command any, arguments []any) string {
	var parts []string
	if list, ok := command.([]any); ok {
		for _, item := range list {
			parts = append(parts, fmt.Sprint(item))
		}
	} else if command != nil {
		parts = append(parts, fmt.Sprint(command))
	}
	for _, argument := range arguments {
		parts = append(parts, fmt.Sprint(argument))
	}
	return strings.Join(parts, " ")
}

// renderJSON renders one entry as a JSON object whose continuation lines line
// up under pad. Short arrays stay on one line, which is how these files are
// written by hand.
func renderJSON(entry fields, pad string) string {
	lines := make([]string, len(entry))
	for i, f := range entry {
		lines[i] = pad + indent + quote(f.key) + ": " + jsonScalar(f.value)
	}
	return "{\n" + strings.Join(lines, ",\n") + "\n" + pad + "}"
}

// jsonScalar is encoding/json's spelling of a leaf: shared by the JSON object
// and the TOML table body, which happen to want the same quotes and brackets.
func jsonScalar(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func quote(value string) string { return strconv.Quote(value) }

// renderTOML renders one entry as the body of its own table.
func renderTOML(entry fields) string {
	lines := make([]string, len(entry))
	for i, f := range entry {
		lines[i] = f.key + " = " + jsonScalar(f.value)
	}
	return strings.Join(lines, "\n")
}

// renderYAML renders one entry as a block mapping indented under pad.
func renderYAML(entry fields, pad string) string {
	pad += indent
	var lines []string
	for _, f := range entry {
		list, ok := f.value.([]string)
		if !ok {
			lines = append(lines, pad+f.key+": "+yamlScalar(f.value))
			continue
		}
		lines = append(lines, pad+f.key+":")
		for _, item := range list {
			lines = append(lines, pad+indent+"- "+yamlScalar(item))
		}
	}
	return strings.Join(lines, "\n")
}

// yamlScalar quotes only what needs quoting. A bare `roca` reads better than a
// quoted one, and an operator opening this file should recognize what they
// asked for.
func yamlScalar(value any) string {
	text, ok := value.(string)
	if !ok {
		return fmt.Sprint(value)
	}
	if text == "" || strings.ContainsAny(text, " :#{}[],&*?|<>=!%@`\"'\n\t") {
		return quote(text)
	}
	return text
}
