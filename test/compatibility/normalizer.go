package compatibility

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	correlationPattern = regexp.MustCompile(`\bqf_[[:alnum:]]+\b`)
	durationPattern    = regexp.MustCompile(`\b[0-9]+(?:\.[0-9]+)?\s*(?:ms|µs|s)\b`)
	timestampPattern   = regexp.MustCompile(`\b[0-9]{4}-[0-9]{2}-[0-9]{2}[T ][0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2})?\b`)
	buildFieldPattern  = regexp.MustCompile(`(?m)^(version|source_sha): .+$`)
)

type Normalizer struct {
	Home string
}

func (n Normalizer) JSON(raw []byte) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("decode oracle JSON: %w", err)
	}
	value = n.normalizeValue("", value)
	var normalized bytes.Buffer
	encoder := json.NewEncoder(&normalized)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return "", fmt.Errorf("encode normalized oracle JSON: %w", err)
	}
	return normalized.String(), nil
}

func (n Normalizer) Text(raw string) string {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	text = n.normalizeString(text)
	text = durationPattern.ReplaceAllString(text, "<duration>")
	text = buildFieldPattern.ReplaceAllString(text, "$1: <$1>")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return strings.TrimSpace(text) + "\n"
}

func (n Normalizer) normalizeValue(key string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			typed[childKey] = n.normalizeValue(childKey, child)
		}
		return typed
	case []any:
		for index, child := range typed {
			typed[index] = n.normalizeValue(key, child)
		}
		return typed
	case string:
		switch {
		case key == "created_at" || key == "updated_at" || key == "timestamp":
			return "<timestamp>"
		case key == "correlation_id":
			return "<correlation_id>"
		case key == "version":
			return "<version>"
		case key == "source_sha":
			return "<source_sha>"
		default:
			return n.normalizeString(typed)
		}
	default:
		if strings.HasSuffix(key, "_ms") {
			return "<duration_ms>"
		}
		return value
	}
}

func (n Normalizer) normalizeString(value string) string {
	if n.Home != "" {
		value = strings.ReplaceAll(value, n.Home, "<home>")
	}
	value = correlationPattern.ReplaceAllString(value, "<correlation_id>")
	return timestampPattern.ReplaceAllString(value, "<timestamp>")
}
