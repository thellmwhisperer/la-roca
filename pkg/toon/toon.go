// Package toon owns the TOON string primitives shared by the core and the
// vector plugin. The core's axi package and the plugin's status renderer both
// paint rows and help lines; both route through here so the quoting and
// escaping rules have one owner.
package toon

import (
	"fmt"
	"regexp"
	"strings"
)

// String normalises a value to a single line and quotes it when the TOON
// grammar requires it.
func String(value string) string {
	value = SingleLine(value)
	if !NeedsQuotes(value) {
		return value
	}
	return Quote(value)
}

// SingleLine collapses runs of whitespace to single spaces.
func SingleLine(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
}

var number = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

// NeedsQuotes reports whether a single-line value must be quoted in TOON:
// empties, literals, leading dashes or hashes, structural characters, control
// characters, or anything that parses as a number.
func NeedsQuotes(value string) bool {
	if value == "" || value == "true" || value == "false" || value == "null" ||
		strings.Trim(value, " \t") != value || strings.ContainsAny(value, ",:\"\\[]{}\n\r\t") ||
		strings.HasPrefix(value, "-") || strings.HasPrefix(value, "#") {
		return true
	}
	for _, r := range value {
		if r < 0x20 {
			return true
		}
	}
	return number.MatchString(value)
}

// Key quotes a column or field name when it is not a bare TOON identifier.
func Key(key string) string {
	if key == "" || !asciiLetter(key[0]) && key[0] != '_' {
		return Quote(key)
	}
	for i := 1; i < len(key); i++ {
		if !asciiLetter(key[i]) && (key[i] < '0' || key[i] > '9') && key[i] != '_' && key[i] != '.' {
			return Quote(key)
		}
	}
	return key
}

func asciiLetter(b byte) bool { return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' }

// Quote wraps value in double quotes, escaping backslashes, double quotes, and
// all control characters below 0x20 as \uXXXX.
func Quote(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			out.WriteString(`\\`)
		case '"':
			out.WriteString(`\"`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

// RenderHelp paints the deterministic next-step lines that follow a row table.
// Empty input paints nothing.
func RenderHelp(lines ...string) string {
	if len(lines) == 0 {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "help[%d]:", len(lines))
	for _, line := range lines {
		out.WriteString("\n  - ")
		out.WriteString(String(line))
	}
	return out.String()
}
