// Package jsonid encodes memory identifiers so JavaScript clients keep every
// digit. JSON numbers above 2^53 round silently; a decimal string does not.
package jsonid

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// MaxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER (2^53 - 1).
const MaxSafeInteger = 1<<53 - 1

// Unsafe reports whether n cannot be a JSON number that JavaScript will keep.
func Unsafe(n int64) bool {
	return n < -MaxSafeInteger || n > MaxSafeInteger
}

// IdentityName reports whether a JSON key or SQL column holds a memory id.
func IdentityName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "id", "rowid", "supersedes":
		return true
	}
	return strings.HasSuffix(n, "_id")
}

// Decimal is a memory id in a JSON object. It marshals as a decimal string and
// unmarshals from either a string or a JSON number so existing scripts keep
// working. The MCP schema advertises string or integer so existing numeric scripts keep working.
type Decimal string

// Int64 returns the identifier, or 0 when empty.
func (d Decimal) Int64() int64 {
	n, _ := ParseText(string(d))
	return n
}

func (d *Decimal) UnmarshalJSON(data []byte) error {
	n, err := ParseJSON(data)
	if err != nil {
		return err
	}
	if n == 0 {
		*d = ""
		return nil
	}
	*d = Decimal(strconv.FormatInt(n, 10))
	return nil
}

// Ints is a list of memory ids. JSON encodes each element as a decimal string
// and still accepts a JSON number per element.
type Ints []int64

func (ids Ints) MarshalJSON() ([]byte, error) {
	if ids == nil {
		return []byte("null"), nil
	}
	texts := make([]string, len(ids))
	for i, id := range ids {
		texts[i] = strconv.FormatInt(id, 10)
	}
	return json.Marshal(texts)
}

func (ids *Ints) UnmarshalJSON(data []byte) error {
	if string(bytes.TrimSpace(data)) == "null" {
		*ids = nil
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(Ints, len(raw))
	for i, item := range raw {
		n, err := ParseJSON(item)
		if err != nil {
			return err
		}
		out[i] = n
	}
	*ids = out
	return nil
}

// IntVar is a pflag.Value that parses a decimal memory id, including the
// numeric form existing scripts already pass.
type IntVar int64

func (v IntVar) String() string { return strconv.FormatInt(int64(v), 10) }

func (v *IntVar) Set(s string) error {
	n, err := ParseText(s)
	if err != nil {
		return err
	}
	*v = IntVar(n)
	return nil
}

func (IntVar) Type() string { return "id" }

// Int reads a memory id from a JSON cell that may be a string or an integer.
func Int(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case json.Number:
		n, err := ParseText(string(v))
		return n, err == nil
	case string:
		if strings.TrimSpace(v) == "" {
			return 0, false
		}
		n, err := ParseText(v)
		return n, err == nil
	default:
		return 0, false
	}
}

// ParseText parses a decimal identifier. Empty input is 0, the "no id" value.
func ParseText(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("memory id %q is not an integer", s)
	}
	return n, nil
}

// ParseJSON parses a JSON string or number as a memory id.
func ParseJSON(data []byte) (int64, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return 0, nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return 0, fmt.Errorf("memory id is not a decimal string")
		}
		return ParseText(s)
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return 0, fmt.Errorf("memory id is not an integer")
	}
	return ParseText(string(n))
}

// Cell rewrites one named SQL/JSON cell: identity columns and integers outside
// the JavaScript safe range become decimal strings, and a metadata JSON blob
// has the same rewrite applied inside it.
func Cell(column string, value any) any {
	identity := IdentityName(column)
	rewritten := rewrite(value, identity)
	if identity || strings.EqualFold(strings.TrimSpace(column), "metadata") {
		return rewriteJSONText(rewritten)
	}
	return rewritten
}

// RewriteMap walks a JSON object and stringifies identity values and unsafe
// integers, including nested objects and arrays.
func RewriteMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, child := range value {
		out[key] = rewrite(child, IdentityName(key))
	}
	return out
}

func rewrite(value any, identity bool) any {
	switch v := value.(type) {
	case nil:
		return nil
	case json.Number:
		return rewriteDigits(string(v), identity)
	case int:
		return rewriteInt(int64(v), identity)
	case int8:
		return rewriteInt(int64(v), identity)
	case int16:
		return rewriteInt(int64(v), identity)
	case int32:
		return rewriteInt(int64(v), identity)
	case int64:
		return rewriteInt(v, identity)
	case uint:
		return rewriteUint(uint64(v), identity)
	case uint32:
		return rewriteUint(uint64(v), identity)
	case uint64:
		return rewriteUint(v, identity)
	case float32:
		return rewriteFloat(float64(v), identity)
	case float64:
		return rewriteFloat(v, identity)
	case map[string]any:
		return RewriteMap(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = rewrite(item, false)
		}
		return out
	default:
		return value
	}
}

func rewriteInt(n int64, identity bool) any {
	if identity || Unsafe(n) {
		return strconv.FormatInt(n, 10)
	}
	return n
}

func rewriteUint(n uint64, identity bool) any {
	if n > uint64(math.MaxInt64) {
		return strconv.FormatUint(n, 10)
	}
	return rewriteInt(int64(n), identity)
}

func rewriteFloat(n float64, identity bool) any {
	if math.Trunc(n) != n {
		return n
	}
	if n > float64(math.MaxInt64) || n < float64(math.MinInt64) {
		return strconv.FormatFloat(n, 'f', 0, 64)
	}
	return rewriteInt(int64(n), identity)
}

func rewriteDigits(digits string, identity bool) any {
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return json.Number(digits)
	}
	return rewriteInt(n, identity)
}

func rewriteJSONText(value any) any {
	var raw string
	switch typed := value.(type) {
	case string:
		raw = typed
	case []byte:
		raw = string(typed)
	default:
		return value
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return value
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return value
	}
	rewritten := rewrite(parsed, false)
	encoded, err := json.Marshal(rewritten)
	if err != nil {
		return value
	}
	return string(encoded)
}
