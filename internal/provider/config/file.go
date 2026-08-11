package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

// SetProviderModel changes only a model assignment and preserves the remaining TOML.
func SetProviderModel(path, provider, model string) error {
	provider, model = strings.TrimSpace(strings.ToLower(provider)), strings.TrimSpace(model)
	if provider == "" || model == "" || strings.ContainsAny(provider, "[]#.= \t\r\n") {
		return fmt.Errorf("provider and model must be non-empty TOML values")
	}
	return editFile(path, func(text string) string {
		return setTableValueText(text, "[models."+provider+"]", "model", strconv.Quote(model))
	})
}

func SetModelOrder(path string, providers []string) error {
	quoted := make([]string, 0, len(providers))
	for _, name := range providers {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "" || strings.ContainsAny(name, "[]#.= \t\r\n") {
			return fmt.Errorf("provider order contains an invalid name")
		}
		quoted = append(quoted, strconv.Quote(name))
	}
	return editFile(path, func(text string) string {
		return setTableValueText(text, "[models]", "order", "["+strings.Join(quoted, ", ")+"]")
	})
}

func editFile(path string, update func(string) string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read the configuration at %s: %w", path, err)
	}
	text := string(raw)
	if text != "" {
		var document map[string]any
		if _, err := toml.Decode(text, &document); err != nil {
			return fmt.Errorf("the configuration at %s is not valid TOML: %w", path, err)
		}
	}
	updated := update(text)
	if err := securefile.Write(path, []byte(updated), 0o600, 0o700); err != nil {
		return fmt.Errorf("write the configuration at %s: %w", path, err)
	}
	return nil
}

// tableKey is the canonical form of a TOML table header: the dotted key, lower
// case, with the brackets, the surrounding whitespace and any quoting of a
// component removed. It is empty for a line that is not a plain table header, so
// an array-of-tables header and ordinary key lines never compare equal to one.
func tableKey(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") ||
		strings.HasPrefix(line, "[[") {
		return ""
	}
	inner := strings.TrimSpace(line[1 : len(line)-1])
	if inner == "" {
		return ""
	}
	var parts []string
	var current strings.Builder
	quote := rune(0)
	for _, r := range inner {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
		case r == '.':
			parts = append(parts, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if quote != 0 {
		return "" // an unterminated quote is not a header this may rewrite
	}
	parts = append(parts, strings.TrimSpace(current.String()))
	for _, part := range parts {
		if part == "" {
			return ""
		}
	}
	return strings.ToLower(strings.Join(parts, "."))
}

func setTableValueText(text, header, key, value string) string {
	start, end, firstChild := -1, len(text), -1
	// The operator wrote this file, and TOML spells one table several ways:
	// `[models.xai]`, `[ models.xai ]` and `[models."xai"]` are the same table.
	// Comparing the raw line text matched only the first, so an edit to either of
	// the others appended a SECOND table for the same key and left the
	// configuration unparseable.
	want := tableKey(header)
	childPrefix := want + "."
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		clean := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		candidate := tableKey(clean)
		if candidate != "" && candidate == want {
			start = offset + len(line)
		} else if firstChild < 0 && candidate != "" && strings.HasPrefix(candidate, childPrefix) {
			firstChild = offset
		}
		if start >= 0 && offset >= start && strings.HasPrefix(clean, "[") {
			end = offset
			break
		}
		offset += len(line)
	}
	if start == len(text) {
		return text + "\n" + key + " = " + value + "\n"
	}
	if start < 0 {
		block := header + "\n" + key + " = " + value + "\n"
		if firstChild >= 0 {
			return text[:firstChild] + block + "\n" + text[firstChild:]
		}
		if text == "" {
			return block
		}
		separator := "\n"
		if !strings.HasSuffix(text, "\n") {
			separator = "\n\n"
		}
		return text + separator + block
	}
	offset = 0
	for _, line := range strings.SplitAfter(text[start:end], "\n") {
		eq := strings.IndexByte(line, '=')
		if eq < 0 || strings.TrimSpace(line[:eq]) != key {
			offset += len(line)
			continue
		}
		valueStart := eq + 1
		for valueStart < len(line) && (line[valueStart] == ' ' || line[valueStart] == '\t') {
			valueStart++
		}
		valueEnd := valueStart + tomlValueEnd(text[start+offset+valueStart:end])
		return text[:start+offset+valueStart] + value + text[start+offset+valueEnd:]
	}
	return text[:start] + key + " = " + value + "\n" + text[start:]
}

func tomlValueEnd(value string) int {
	depth, quote, comment := 0, byte(0), false
	for i := 0; i < len(value); i++ {
		char := value[i]
		if comment {
			comment = char != '\n'
			continue
		}
		if quote != 0 {
			if quote == '"' && char == '\\' {
				i++
				continue
			}
			if char == quote {
				quote = 0
				if depth == 0 {
					return i + 1
				}
			}
			continue
		}
		switch char {
		case '#':
			comment = true
		case '\'', '"':
			quote = char
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		case '\n':
			if depth == 0 {
				return len(strings.TrimRight(value[:i], " \t\r"))
			}
		}
	}
	return len(strings.TrimRight(value, " \t\r\n"))
}

// FileConfig is the operator's configuration file and DirCredentials is where
// subscription sessions live. Both hang off the data directory, so an adopted
// database keeps its configuration next to the imported data.
const (
	FileConfig     = "config.toml"
	DirCredentials = "credentials"
	EnvConfig      = "ROCA_CONFIG"
)

// File is the operator's config, already read.
//
// Two rules govern this whole file:
//
//   - **Data the operator persisted outlives the release that understood it.**
//     A key this build does not know is a warning that names the remedy, never a
//     command that does not run.
//   - **Every message to the operator names the key and the file, never a TOML
//     table.** A warning that says "invalid section" sends them to read code.
type File struct {
	// Path is where it was looked for, whether or not it was there.
	Path   string
	Exists bool
	Models ModelsConfig
	Query  QueryConfig
	// Warnings are what this build did not understand, each one naming the key,
	// the file and the remedy.
	Warnings []string

	// defaults holds the supported loose scalar keys.
	defaults map[string]any
}

// QueryConfig bounds execution of SQL that passed the read-only gate.
type QueryConfig struct {
	TimeoutMS int `toml:"timeout_ms"`
}

// ModelsConfig is the [models] section: which providers, in what order, with
// what budget.
type ModelsConfig struct {
	// Order is the provider order. Empty means the default order.
	Order []string `toml:"order"`
	// InterpretOrder is the provider order for the second inference, the one
	// that reads the result rows. Empty means the rows are interpreted by
	// whichever provider of Order served, which is the behaviour of an
	// installation that never heard of this key. Declaring it separates the two
	// inferences: the question and the schema go to Order, the rows go here.
	InterpretOrder []string `toml:"interpret_order"`
	// TimeoutMS bounds a model request. Zero is the adapter's default.
	TimeoutMS int `toml:"timeout_ms"`
	// ProbeMS bounds the availability question. Zero is the adapter's default.
	ProbeMS int `toml:"probe_ms"`
	// Providers is one table per provider, keyed by the name used in the order.
	Providers map[string]ProviderConfig `toml:"-"`
}

// ProviderConfig is one provider's table.
type ProviderConfig struct {
	// Preset fills in endpoint and model from what this build knows about a
	// named provider. Empty tries the provider's own name as a preset.
	Preset  string `toml:"preset"`
	BaseURL string `toml:"base_url"`
	Model   string `toml:"model"`
	// APIKey is the credential. It is read from here or from APIKeyEnv, never
	// from the database, and it never travels to any output.
	APIKey string `toml:"api_key"`
	// APIKeyEnv is the environment variable the credential lives in, for an
	// operator who would rather not write it on disk.
	APIKeyEnv string `toml:"api_key_env"`
	// KeepAlive is how long the local model stays loaded.
	KeepAlive string `toml:"keep_alive"`
}

// knownProviderKeys is what a provider table may carry. It is here and not
// derived from the struct because the warning has to name the key the operator
// wrote, not a field name.
var knownProviderKeys = map[string]bool{
	"preset": true, "base_url": true, "model": true,
	"api_key": true, "api_key_env": true, "keep_alive": true,
}

var knownModelsKeys = map[string]bool{
	"order": true, "interpret_order": true, "timeout_ms": true, "probe_ms": true,
}

var knownQueryKeys = map[string]bool{"timeout_ms": true}

// LoadFile reads the config. A file that is not there is a machine with
// defaults, not a failure.
func LoadFile(path string) (File, error) {
	file := File{Path: path}

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return file, nil
	}
	if err != nil {
		return file, fmt.Errorf("read the configuration at %s: %w", path, err)
	}
	file.Exists = true

	var document map[string]any
	if _, err := toml.Decode(string(raw), &document); err != nil {
		return file, fmt.Errorf("the configuration at %s is not valid TOML: %w", path, err)
	}

	file.defaults, _ = document["defaults"].(map[string]any)
	models, _ := document["models"].(map[string]any)
	file.Models = readModels(models, path, &file.Warnings)
	query, _ := document["query"].(map[string]any)
	file.Query = readQuery(query, path, &file.Warnings)
	return file, nil
}

func readQuery(section map[string]any, path string, warnings *[]string) QueryConfig {
	var query QueryConfig
	for _, key := range sortedKeys(section) {
		switch key {
		case "timeout_ms":
			query.TimeoutMS = readInt(section[key])
		default:
			if !knownQueryKeys[key] {
				*warnings = append(*warnings, unknownKey("query."+key, path))
			}
		}
	}
	return query
}

// readModels walks the [models] section by hand instead of letting a decoder
// impose a shape, so an unknown key can be reported by its own name.
func readModels(section map[string]any, path string, warnings *[]string) ModelsConfig {
	models := ModelsConfig{Providers: map[string]ProviderConfig{}}
	if section == nil {
		return models
	}

	for _, key := range sortedKeys(section) {
		value := section[key]
		if table, isTable := value.(map[string]any); isTable {
			models.Providers[strings.ToLower(key)] = readProvider(
				table, "models."+key, path, warnings)
			continue
		}
		switch key {
		case "order":
			models.Order = readStrings(value)
		case "interpret_order":
			models.InterpretOrder = readStrings(value)
		case "timeout_ms":
			models.TimeoutMS = readInt(value)
		case "probe_ms":
			models.ProbeMS = readInt(value)
		default:
			if !knownModelsKeys[key] {
				*warnings = append(*warnings, unknownKey("models."+key, path))
			}
		}
	}
	return models
}

func readProvider(table map[string]any, prefix, path string, warnings *[]string) ProviderConfig {
	var cfg ProviderConfig
	for _, key := range sortedKeys(table) {
		text, _ := table[key].(string)
		switch key {
		case "preset":
			cfg.Preset = text
		case "base_url":
			cfg.BaseURL = text
		case "model":
			cfg.Model = text
		case "api_key":
			cfg.APIKey = text
		case "api_key_env":
			cfg.APIKeyEnv = text
		case "keep_alive":
			cfg.KeepAlive = text
		default:
			if !knownProviderKeys[key] {
				*warnings = append(*warnings, unknownKey(prefix+"."+key, path))
			}
		}
	}
	return cfg
}

// unknownKey is the warning shape: the key, the file and the exact remedy.
func unknownKey(key, path string) string {
	return fmt.Sprintf(
		"this version does not understand the key %s of %s: it is ignored. "+
			"Remove that line, or check `roca doctor` for the keys this version does understand",
		key, path)
}

// Default resolves a loose key under [defaults].
func (f File) Default(key string) string {
	if value, ok := f.defaults[key]; ok {
		return asString(value)
	}
	return ""
}

// DefaultList resolves a loose list under [defaults].
//
// Three shapes are accepted, because all three are what operators actually write:
// a TOML array, a JSON array inside a string (which is how the equivalent
// environment variable has to be spelled), and a single path. A config with one
// root written as a bare string is a config, not a mistake.
func (f File) DefaultList(key string) []string {
	if value, ok := f.defaults[key]; ok {
		return asStrings(value)
	}
	return nil
}

func asStrings(value any) []string {
	switch typed := value.(type) {
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(asString(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil
		}
		if strings.HasPrefix(text, "[") {
			var decoded []string
			if err := json.Unmarshal([]byte(text), &decoded); err == nil {
				return trimAll(decoded)
			}
			return nil
		}
		return []string{text}
	}
	return nil
}

func trimAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case int64:
		return fmt.Sprint(typed)
	case float64:
		return fmt.Sprint(typed)
	case bool:
		return fmt.Sprint(typed)
	}
	return ""
}

func readStrings(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, strings.TrimSpace(text))
		}
	}
	return out
}

func readInt(value any) int {
	switch typed := value.(type) {
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	}
	return 0
}

func sortedKeys(table map[string]any) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
