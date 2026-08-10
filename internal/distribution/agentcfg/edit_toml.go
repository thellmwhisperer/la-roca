package agentcfg

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// The TOML editor, for Codex.
//
// Roca owns one table, `[mcp_servers.roca]`, and the edits are line edits over
// it. A table is the shape Codex writes and the shape an operator recognizes.
// Inline `mcp_servers = { ... }` is refused by name and with a remedy because corrupting somebody's
// config is worse than asking them to spell it the ordinary way.

func tomlDeclare(r runtime, text string, entry fields) (string, error) {
	if _, err := tomlDocument(r, text); err != nil {
		return "", err
	}
	body := renderTOML(entry)
	if block, ok := tomlBlock(text, r, ServerName); ok {
		// Replaced in place: the header stays where it is, and so does whatever
		// comment sits above it.
		return text[:block.bodyStart] + body + "\n" + text[block.end:], nil
	}
	separator := "\n"
	if text == "" || strings.HasSuffix(text, "\n\n") {
		separator = ""
	} else if !strings.HasSuffix(text, "\n") {
		separator = "\n\n"
	}
	return text + separator + tomlHeader(r, ServerName) + "\n" + body + "\n", nil
}

func tomlRemove(r runtime, text string, entries []string) (string, error) {
	if _, err := tomlDocument(r, text); err != nil {
		return "", err
	}
	for _, name := range entries {
		block, ok := tomlBlock(text, r, name)
		if !ok {
			continue
		}
		text = text[:block.start] + text[block.end:]
	}
	return text, nil
}

// block is where one table lives: from the first byte of the comment run
// attached above its header to the first byte of whatever follows it.
type block struct{ start, bodyStart, end int }

func tomlHeader(r runtime, name string) string {
	return "[" + r.serversKey + "." + name + "]"
}

// tomlBlock treats the contiguous comment run above `[mcp_servers.<name>]` as
// part of that table; a comment separated by a blank line remains unrelated.
func tomlBlock(text string, r runtime, name string) (block, bool) {
	header := tomlHeader(r, name)
	lines, offsets := splitLines(text)
	for i, line := range lines {
		if strings.TrimSpace(stripComment(line)) != header {
			continue
		}
		first := i
		for first > 0 && strings.HasPrefix(strings.TrimSpace(lines[first-1]), "#") {
			first--
		}
		// And then the blank lines that separate the table from what is above it,
		// because they are the separator installing added and withdrawing has to
		// give the file back exactly as it found it. In that order and not in one
		// pass: a comment above a blank line is on the other side of the
		// separator, so it is unrelated content and stays where it is.
		for first > 0 && strings.TrimSpace(lines[first-1]) == "" {
			first--
		}
		// The table ends after its last line with content in it. The blank lines
		// and the comment run that follow belong to whatever comes next, so a
		// separator the operator wrote between two of their own tables survives
		// Roca's table having stood between them.
		last := i
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "[") {
				break
			}
			if strings.TrimSpace(lines[j]) != "" {
				last = j
			}
		}
		return block{
			start: offsets[first], bodyStart: offsets[i] + len(line),
			end: offsets[last] + len(lines[last]),
		}, true
	}
	return block{}, false
}

// tomlDocument reads the file and refuses, before a byte is touched, the shape
// this editor does not know how to edit without risking the operator's file,
// naming the remedy.
func tomlDocument(r runtime, text string) (map[string]any, error) {
	var document map[string]any
	if _, err := toml.Decode(text, &document); err != nil {
		return nil, err
	}
	if _, ok := document[r.serversKey]; !ok {
		return document, nil
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(stripComment(line))
		if strings.HasPrefix(trimmed, r.serversKey) && strings.Contains(trimmed, "=") {
			return nil, fmt.Errorf(
				"%s is written as an inline table, and this version only edits "+
					"`[%s.<name>]` tables: rewrite it as tables and run the command again",
				r.serversKey, r.serversKey)
		}
	}
	return document, nil
}

// stripComment drops a trailing comment, respecting the quotes a value may
// carry a `#` inside.
func stripComment(line string) string {
	inString := byte(0)
	for i := 0; i < len(line); i++ {
		switch {
		case inString != 0 && line[i] == inString:
			inString = 0
		case inString == 0 && (line[i] == '"' || line[i] == '\''):
			inString = line[i]
		case inString == 0 && line[i] == '#':
			return line[:i]
		}
	}
	return line
}

// splitLines returns every line with its line ending and the offset each one
// starts at, so a line edit is a byte edit.
func splitLines(text string) ([]string, []int) {
	var lines []string
	var offsets []int
	for start := 0; start < len(text); {
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			end = len(text) - start - 1
		}
		lines = append(lines, text[start:start+end+1])
		offsets = append(offsets, start)
		start += end + 1
	}
	return lines, offsets
}
