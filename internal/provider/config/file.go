package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

type ChangeKind string

const (
	SetValue         ChangeKind = "set"
	PrependUnique    ChangeKind = "prepend-unique"
	ReplaceTable     ChangeKind = "replace-table"
	ReplaceListValue ChangeKind = "replace-list-value"
	RemoveListValue  ChangeKind = "remove-list-value"
	DeleteTable      ChangeKind = "delete-table"
	DeleteValue      ChangeKind = "delete-value"
)

type Field struct {
	Key       string
	Value     any
	ValueFrom string
	Fallback  any
}

// Change is one declarative, surgical TOML edit. A reconciliation proposal
// carries these as data so the runner never needs feature-specific write code.
type Change struct {
	Kind    ChangeKind
	Table   string
	Key     string
	Value   any
	Old     string
	Default []string
	Fields  []Field
}

// ApplyText applies a set of declared changes without reserializing the
// operator's document. The whole result is decoded before it can be written.
func ApplyText(text string, changes []Change) (string, error) {
	var document map[string]any
	if strings.TrimSpace(text) != "" {
		if _, err := toml.Decode(text, &document); err != nil {
			return "", fmt.Errorf("the configuration is not valid TOML: %w", err)
		}
	}
	if document == nil {
		document = map[string]any{}
	}
	updated := text
	for _, change := range changes {
		switch change.Kind {
		case SetValue:
			updated = setTableValueText(updated, "["+change.Table+"]", change.Key,
				tomlLiteral(change.Value))
		case PrependUnique:
			values := stringListAt(document, change.Table, change.Key)
			if values == nil {
				values = append([]string(nil), change.Default...)
			}
			value := strings.TrimSpace(fmt.Sprint(change.Value))
			values = prependUnique(values, value)
			updated = setTableValueText(updated, "["+change.Table+"]", change.Key,
				tomlLiteral(values))
		case ReplaceTable:
			fields := make([]Field, len(change.Fields))
			copy(fields, change.Fields)
			for i := range fields {
				if fields[i].ValueFrom != "" {
					fields[i].Value = scalarAt(document, fields[i].ValueFrom)
					if fields[i].Value == nil || strings.TrimSpace(fmt.Sprint(fields[i].Value)) == "" {
						fields[i].Value = fields[i].Fallback
					}
				}
			}
			updated = replaceTableText(updated, change.Table, fields)
		case ReplaceListValue, RemoveListValue:
			values := stringListAt(document, change.Table, change.Key)
			if values == nil {
				continue
			}
			kept := make([]string, 0, len(values))
			seen := make(map[string]bool, len(values))
			old := normalizeProviderName(change.Old)
			for _, current := range values {
				if normalizeProviderName(current) == old {
					if change.Kind == ReplaceListValue {
						replacement := strings.TrimSpace(fmt.Sprint(change.Value))
						normalized := normalizeProviderName(replacement)
						if normalized != "" && !seen[normalized] {
							kept = append(kept, replacement)
							seen[normalized] = true
						}
					}
					continue
				}
				normalized := normalizeProviderName(current)
				if normalized == "" || seen[normalized] {
					continue
				}
				kept = append(kept, current)
				seen[normalized] = true
			}
			if len(kept) == 0 {
				updated = deleteTableValueText(updated, change.Table, change.Key)
			} else {
				updated = setTableValueText(updated, "["+change.Table+"]", change.Key, tomlLiteral(kept))
			}
		case DeleteTable:
			updated = deleteTableText(updated, change.Table)
		case DeleteValue:
			updated = deleteTableValueText(updated, change.Table, change.Key)
		default:
			return "", fmt.Errorf("unknown configuration change %q", change.Kind)
		}
		if _, err := toml.Decode(updated, &document); err != nil {
			return "", fmt.Errorf("configuration change for %s produced invalid TOML: %w", change.Table, err)
		}
	}
	return updated, nil
}

func RedactProviderSecrets(text string) (string, error) {
	var document map[string]any
	if strings.TrimSpace(text) == "" {
		return text, nil
	}
	if _, err := toml.Decode(text, &document); err != nil {
		return "", fmt.Errorf("the configuration is not valid TOML: %w", err)
	}
	changes := redactProviderSecretDocument(document)
	redacted, err := ApplyText(text, changes)
	if err != nil {
		return "", err
	}
	if err := verifyProviderSecretsRedacted(redacted); err == nil {
		return redacted, nil
	}
	var encoded strings.Builder
	if err := toml.NewEncoder(&encoded).Encode(document); err != nil {
		return "", fmt.Errorf("encode redacted configuration: %w", err)
	}
	redacted = encoded.String()
	if err := verifyProviderSecretsRedacted(redacted); err != nil {
		return "", err
	}
	return redacted, nil
}

func redactProviderSecretDocument(document map[string]any) []Change {
	models, _ := document["models"].(map[string]any)
	changes := make([]Change, 0, len(models))
	for name, value := range models {
		table, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if _, present := table["api_key"]; present {
			delete(table, "api_key")
			changes = append(changes, Change{Kind: DeleteValue, Table: "models." + name, Key: "api_key"})
		}
	}
	return changes
}

func verifyProviderSecretsRedacted(text string) error {
	var document map[string]any
	if _, err := toml.Decode(text, &document); err != nil {
		return fmt.Errorf("verify redacted configuration: %w", err)
	}
	models, _ := document["models"].(map[string]any)
	for name, value := range models {
		if table, ok := value.(map[string]any); ok {
			if _, present := table["api_key"]; present {
				return fmt.Errorf("models.%s.api_key survived provider-secret redaction", name)
			}
		}
	}
	return nil
}

func deleteTableValueText(text, table, key string) string {
	want := strings.ToLower(strings.TrimSpace(table)) + "." + key
	return deleteKeyLines(text, func(candidate string) bool { return candidate == want })
}

// deleteKeyLines removes every assignment whose fully qualified key the caller
// claims, wherever the operator declared it: under its own table header, or as a
// dotted key written in an outer scope.
func deleteKeyLines(text string, claimed func(string) bool) string {
	var kept strings.Builder
	scope := ""
	for _, line := range strings.SplitAfter(text, "\n") {
		clean := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if strings.HasPrefix(clean, "[") {
			scope = headerScope(clean)
		} else if candidate, _ := documentKey(scope, line); candidate != "" && claimed(candidate) {
			continue
		}
		kept.WriteString(line)
	}
	return kept.String()
}

func deleteTableText(text, table string) string {
	want := strings.ToLower(strings.TrimSpace(table))
	start, end, offset := -1, len(text), 0
	for _, line := range strings.SplitAfter(text, "\n") {
		candidate := tableKey(strings.TrimSpace(strings.SplitN(line, "#", 2)[0]))
		if start < 0 && candidate == want {
			start = offset
		} else if start >= 0 && (candidate != "" || strings.HasPrefix(strings.TrimSpace(line), "[[")) {
			end = offset
			break
		}
		offset += len(line)
	}
	if start >= 0 {
		for end < len(text) && text[end] == '\n' && start > 0 && text[start-1] == '\n' {
			end++
		}
		text = text[:start] + text[end:]
	}
	// The same table spelled as dotted keys has no header to cut out, so its keys
	// have to go one by one or the retired credential survives the retirement.
	return deleteKeyLines(text, func(candidate string) bool {
		return candidate == want || strings.HasPrefix(candidate, want+".")
	})
}

func prependUnique(values []string, value string) []string {
	out := []string{value}
	want := normalizeProviderName(value)
	for _, current := range values {
		if normalizeProviderName(current) != want {
			out = append(out, current)
		}
	}
	return out
}

func normalizeProviderName(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-")
}

func tomlLiteral(value any) string {
	switch typed := value.(type) {
	case string:
		return strconv.Quote(typed)
	case []string:
		quoted := make([]string, len(typed))
		for i, item := range typed {
			quoted[i] = strconv.Quote(item)
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	case bool, int, int64, float64:
		return fmt.Sprint(typed)
	default:
		raw, _ := json.Marshal(value)
		return string(raw)
	}
}

func stringListAt(document map[string]any, table, key string) []string {
	value := valueAt(document, table+"."+key)
	if value == nil {
		return nil
	}
	return readStrings(value)
}

func scalarAt(document map[string]any, path string) any { return valueAt(document, path) }

func valueAt(document map[string]any, path string) any {
	var current any = document
	for _, part := range strings.Split(path, ".") {
		table, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = table[part]
	}
	return current
}

func replaceTableText(text, table string, fields []Field) string {
	header := "[" + table + "]"
	var body strings.Builder
	body.WriteString(header + "\n")
	for _, field := range fields {
		body.WriteString(field.Key + " = " + tomlLiteral(field.Value) + "\n")
	}
	block := body.String()
	want := strings.ToLower(table)
	start, end, offset := -1, len(text), 0
	for _, line := range strings.SplitAfter(text, "\n") {
		clean := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		candidate := tableKey(clean)
		if start < 0 && candidate == want {
			start = offset
		} else if start >= 0 && (candidate != "" || strings.HasPrefix(clean, "[[")) {
			end = offset
			break
		}
		offset += len(line)
	}
	if start < 0 {
		// A table the operator spelled as dotted keys has no header to replace, and
		// leaving those keys behind would declare the table twice.
		text = deleteKeyLines(text, func(candidate string) bool {
			return candidate == want || strings.HasPrefix(candidate, want+".")
		})
		if text == "" {
			return block
		}
		separator := "\n"
		if !strings.HasSuffix(text, "\n") {
			separator = "\n\n"
		}
		return text + separator + block
	}
	if end < len(text) && !strings.HasSuffix(block, "\n\n") {
		block += "\n"
	}
	return text[:start] + block + text[end:]
}

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

// headerScope is the table a header line opens. An array-of-tables header opens
// a table these edits never claim, so it scopes to a name no key can match.
func headerScope(clean string) string {
	if scope := tableKey(clean); scope != "" {
		return scope
	}
	return clean
}

// documentKey is the fully qualified key an assignment line declares inside the
// table scope open at that line, and the index of its `=`. The table part is
// compared like a header, the final key exactly as the operator wrote it.
func documentKey(scope, line string) (string, int) {
	path, eq := tomlAssignment(line)
	if path == "" {
		return "", -1
	}
	if scope != "" {
		path = scope + "." + path
	}
	if cut := strings.LastIndex(path, "."); cut > 0 {
		path = strings.ToLower(path[:cut]) + path[cut:]
	}
	return path, eq
}

func setTableValueText(text, header, key, value string) string {
	// The operator wrote this file, and TOML spells one key several ways:
	// `[models.xai]`, `[ models.xai ]` and `[models."xai"]` are the same table, and
	// a top-level `models.xai.model = …` declares the same key as `model` under any
	// of them. Comparing the raw line text matched only the first spelling, so an
	// edit to any of the others appended a SECOND declaration of a table that
	// already existed and left the configuration unparseable.
	want := tableKey(header)
	target, childPrefix := want+"."+key, want+"."
	inTable, dotted, firstChild, scope, offset := -1, -1, -1, "", 0
	for _, line := range strings.SplitAfter(text, "\n") {
		clean := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if strings.HasPrefix(clean, "[") {
			scope = headerScope(clean)
			if scope == want && inTable < 0 {
				inTable = offset + len(line)
			} else if firstChild < 0 && strings.HasPrefix(scope, childPrefix) {
				firstChild = offset
			}
			offset += len(line)
			continue
		}
		candidate, eq := documentKey(scope, line)
		if candidate == target {
			at := offset + eq + 1
			for at < len(text) && (text[at] == ' ' || text[at] == '\t') {
				at++
			}
			return text[:at] + value + text[at+tomlValueEnd(text[at:]):]
		}
		// A scope shorter than the table means the table is written into the key
		// itself, which is where a new key of that table has to go as well.
		if dotted < 0 && len(scope) < len(want) && strings.HasPrefix(candidate, childPrefix) {
			dotted = offset
		}
		offset += len(line)
	}
	block := header + "\n" + key + " = " + value + "\n"
	switch {
	case inTable == len(text):
		return text + "\n" + key + " = " + value + "\n"
	case inTable >= 0:
		return text[:inTable] + key + " = " + value + "\n" + text[inTable:]
	case dotted >= 0:
		return text[:dotted] + want + "." + key + " = " + value + "\n" + text[dotted:]
	case firstChild >= 0:
		return text[:firstChild] + block + "\n" + text[firstChild:]
	case text == "":
		return block
	}
	separator := "\n"
	if !strings.HasSuffix(text, "\n") {
		separator = "\n\n"
	}
	return text + separator + block
}

// tomlAssignment is the key an assignment line declares, relative to the table
// it sits in, and the index of its `=`. A dotted key reaches into a nested
// table, so the answer is the whole path: `models.xai.api_key = …` written at
// the top of a file declares the same key as `api_key` under `[models.xai]`.
func tomlAssignment(line string) (string, int) {
	quote := byte(0)
	for index := 0; index < len(line); index++ {
		char := line[index]
		if quote != 0 {
			if quote == '"' && char == '\\' {
				index++
				continue
			}
			if char == quote {
				quote = 0
			}
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
		case '#':
			return "", -1
		case '=':
			raw := strings.TrimSpace(line[:index])
			if raw == "" {
				return "", -1
			}
			var document map[string]any
			if _, err := toml.Decode(raw+" = 0", &document); err != nil {
				return "", -1
			}
			var path []string
			for current := document; len(current) == 1; {
				key := sortedKeys(current)[0]
				path = append(path, key)
				nested, dotted := current[key].(map[string]any)
				if !dotted {
					return strings.Join(path, "."), index
				}
				current = nested
			}
			return "", -1
		}
	}
	return "", -1
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

// FileConfig is the operator's configuration file. It hangs off the data
// directory, so an adopted database keeps its configuration next to the
// imported data.
const (
	FileConfig = "config.toml"
	EnvConfig  = "ROCA_CONFIG"
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
	Path     string
	Exists   bool
	Models   ModelsConfig
	Query    QueryConfig
	Features FeaturesConfig
	// Warnings are what this build did not understand, each one naming the key,
	// the file and the remedy.
	Warnings []string

	// defaults holds the supported loose scalar keys.
	defaults map[string]any
}

// QueryConfig bounds execution of SQL that passed the read-only gate.
type QueryConfig struct {
	TimeoutMS  int  `toml:"timeout_ms"`
	TimeoutSet bool `toml:"-"`
}

// FeaturesConfig contains operational escape hatches for security behaviour.
// StrictInput defaults on; false opts out of the experimental signature gate.
type FeaturesConfig struct {
	StrictInput bool `toml:"strict_input"`
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
	TableName string   `toml:"-"`
	BaseURL   string   `toml:"base_url"`
	Command   []string `toml:"command"`
	Model     string   `toml:"model"`
	// ResponseFormat declares whether command stdout is plain text or a JSON
	// envelope whose result field is the answer.
	ResponseFormat string `toml:"response_format"`
	// TimeoutSeconds bounds one local-binary invocation. It is separate from
	// the millisecond-wide cascade budget because command startup is measured
	// in seconds and has a deliberately generous default.
	TimeoutSeconds int `toml:"timeout_seconds"`
	// RetiredCredential says a legacy provider table still carries a key field.
	// The value is never retained; reconciliation uses only this marker.
	RetiredCredential bool `toml:"-"`
	// KeepAlive is how long the local model stays loaded.
	KeepAlive string `toml:"keep_alive"`
	// Think turns a local reasoning model's thinking back on. It is off by
	// default because thinking is neither the SQL nor the summary asked of the
	// model, and on qwen3.5 it is the difference between an interpretation that
	// answers in seconds and one that answers in minutes.
	Think bool `toml:"think"`
	// Values preserves every scalar for command-template substitution. Tuning
	// belongs to the provider table and does not need a release that knows its
	// vocabulary before it can reach the local CLI.
	Values map[string]string `toml:"-"`
}

var knownModelsKeys = map[string]bool{
	"order": true, "interpret_order": true, "timeout_ms": true, "probe_ms": true,
}

// knownProviderKeys is this build's vocabulary inside a provider table. The
// retired authentication keys are listed because they get their own, more
// specific warning and must not also be reported as unknown.
var knownProviderKeys = map[string]bool{
	"base_url": true, "command": true, "model": true, "response_format": true,
	"timeout_seconds": true, "keep_alive": true, "think": true, "preset": true,
	"api_key": true, "api_key_env": true,
}

var knownQueryKeys = map[string]bool{"timeout_ms": true}
var knownFeaturesKeys = map[string]bool{"strict_input": true}

func KnownProviderKey(key string) bool { return knownProviderKeys[key] }

func UnknownKeyWarning(key, path string) string { return unknownKey(key, path) }

// LoadFile reads the config. A file that is not there is a machine with
// defaults, not a failure.
func LoadFile(path string) (File, error) {
	file := File{Path: path, Features: FeaturesConfig{StrictInput: true}}

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
	for name, provider := range file.Models.Providers {
		for _, placeholder := range CommandPlaceholders(provider.Command) {
			if placeholder == "prompt" {
				continue
			}
			if _, exists := provider.Values[placeholder]; !exists {
				return file, fmt.Errorf(
					"provider %q command in %s uses unknown placeholder {%s}; declare %s under models.%s",
					name, path, placeholder, placeholder, name)
			}
		}
	}
	query, _ := document["query"].(map[string]any)
	file.Query = readQuery(query, path, &file.Warnings)
	features, _ := document["features"].(map[string]any)
	file.Features = readFeatures(features, path, &file.Warnings)
	return file, nil
}

var commandPlaceholder = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_-]*)\}`)

// CommandPlaceholders returns the template keys named by an argv declaration.
func CommandPlaceholders(command []string) []string {
	var placeholders []string
	for _, argument := range command {
		for _, match := range commandPlaceholder.FindAllStringSubmatch(argument, -1) {
			placeholders = append(placeholders, match[1])
		}
	}
	return placeholders
}

func readQuery(section map[string]any, path string, warnings *[]string) QueryConfig {
	var query QueryConfig
	for _, key := range sortedKeys(section) {
		switch key {
		case "timeout_ms":
			// Only a value that really decoded as a number is a setting. The
			// presence of the key is not: `timeout_ms = "5000"` decodes as zero,
			// and a zero that was never written is an execution bound removed by a
			// typo. What did not decode keeps the default and says so.
			milliseconds, ok := readNumber(section[key])
			if !ok {
				*warnings = append(*warnings,
					invalidValue("query.timeout_ms", path, "a whole number of milliseconds"))
				continue
			}
			query.TimeoutMS = milliseconds
			query.TimeoutSet = true
		default:
			if !knownQueryKeys[key] {
				*warnings = append(*warnings, unknownKey("query."+key, path))
			}
		}
	}
	return query
}

func readFeatures(section map[string]any, path string, warnings *[]string) FeaturesConfig {
	features := FeaturesConfig{StrictInput: true}
	for _, key := range sortedKeys(section) {
		switch key {
		case "strict_input":
			// The gate stays on for anything that is not a boolean, because a
			// misspelled opt-out must not be an opt-out. The operator is told, or
			// the escape hatch silently does nothing.
			strict, ok := section[key].(bool)
			if !ok {
				*warnings = append(*warnings,
					invalidValue("features.strict_input", path, "true or false"))
				continue
			}
			features.StrictInput = strict
		default:
			if !knownFeaturesKeys[key] {
				*warnings = append(*warnings, unknownKey("features."+key, path))
			}
		}
	}
	return features
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
			name := normalizeProviderName(key)
			provider := readProvider(table, name, path, warnings)
			provider.TableName = key
			models.Providers[name] = provider
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

func readProvider(table map[string]any, name, path string, warnings *[]string) ProviderConfig {
	cfg := ProviderConfig{Values: make(map[string]string, len(table))}
	cfg.Command = readStrings(table["command"])
	for _, key := range sortedKeys(table) {
		text, _ := table[key].(string)
		if key != "command" && key != "api_key" && key != "api_key_env" && key != "preset" && key != "base_url" {
			cfg.Values[key] = templateString(table[key])
		}
		switch key {
		case "base_url":
			cfg.BaseURL = text
			if name != "ollama" {
				*warnings = append(*warnings, retiredProviderKey(name, key, path))
			}
		case "command":
		case "model":
			cfg.Model = text
		case "response_format":
			cfg.ResponseFormat = text
		case "timeout_seconds":
			cfg.TimeoutSeconds = readInt(table[key])
		case "preset":
			if len(cfg.Command) > 0 {
				cfg.Values[key] = templateString(table[key])
				continue
			}
			cfg.RetiredCredential = true
			*warnings = append(*warnings, retiredProviderKey(name, key, path))
		case "api_key", "api_key_env":
			cfg.RetiredCredential = true
			*warnings = append(*warnings, retiredProviderKey(name, key, path))
		case "keep_alive":
			cfg.KeepAlive = text
		case "think":
			cfg.Think, _ = table[key].(bool)
		}
	}
	return cfg
}

func retiredProviderKey(provider, key, path string) string {
	return fmt.Sprintf(
		"models.%s.%s in %s belongs to a retired HTTP/credential transport: it is ignored; models authenticate through their own local CLIs",
		provider, key, path)
}

func templateString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool, int64, float64:
		return fmt.Sprint(typed)
	default:
		raw, _ := json.Marshal(value)
		return string(raw)
	}
}

// unknownKey is the warning shape: the key, the file and the exact remedy.
func unknownKey(key, path string) string {
	return fmt.Sprintf(
		"this version does not understand the key %s of %s: it is ignored. "+
			"Remove that line, or check `roca doctor` for the keys this version does understand",
		key, path)
}

// invalidValue is the warning for a key this version understands written with
// a value it cannot read. It names what was expected and says which behaviour
// applies meanwhile, because the alternative is a setting that quietly is not
// the one the operator wrote.
func invalidValue(key, path, want string) string {
	return fmt.Sprintf(
		"the key %s of %s is not %s: it is ignored and the built-in default applies. "+
			"Correct that line, or check `roca doctor` for the values this version accepts",
		key, path, want)
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
	number, _ := readNumber(value)
	return number
}

// readNumber also says whether the value really was one. A caller that treats
// the presence of a key as the setting needs that answer: without it, a value
// TOML decoded as text is indistinguishable from a written zero.
func readNumber(value any) (int, bool) {
	switch typed := value.(type) {
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	}
	return 0, false
}

func sortedKeys(table map[string]any) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
