package vector

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	defaultChunkTokens   = 250
	defaultOverlapTokens = 100
	chunkPolicyVersion   = "chunk-policy-v2"
	maxChunkContextRunes = 80
)

type tokenSpan struct {
	start int
	end   int
}

func tokenChunks(text string, size, overlap int) []string {
	if text == "" || size <= 0 || overlap < 0 || overlap >= size {
		return nil
	}
	spans := tokenSpans(text)
	if len(spans) == 0 {
		return nil
	}
	if len(spans) <= size {
		return []string{text}
	}
	var result []string
	for start := 0; start < len(spans); start += size - overlap {
		end := min(start+size, len(spans))
		result = append(result, text[spans[start].start:spans[end-1].end])
		if end == len(spans) {
			break
		}
	}
	return result
}

func tokenSpans(text string) []tokenSpan {
	var spans []tokenSpan
	start := -1
	index := 0
	for index < len(text) {
		r, width := utf8.DecodeRuneInString(text[index:])
		if unicode.IsSpace(r) {
			if start >= 0 {
				spans = append(spans, tokenSpan{start: start, end: index})
				start = -1
			}
		} else if start < 0 {
			start = index
		}
		index += width
	}
	if start >= 0 {
		spans = append(spans, tokenSpan{start: start, end: len(text)})
	}
	return spans
}

func chunkHeader(title, occurredAt string) string {
	title = chunkContext(title)
	month := yearMonth(occurredAt)
	switch {
	case title != "" && month != "":
		return "[" + title + " · " + month + "] "
	case title != "":
		return "[" + title + "] "
	case month != "":
		return "[" + month + "] "
	default:
		return ""
	}
}

func chunkContext(value string) string {
	value = cleanSessionField(value)
	runes := []rune(value)
	if len(runes) <= maxChunkContextRunes {
		return value
	}
	return strings.TrimSpace(string(runes[:maxChunkContextRunes-1])) + "…"
}

func yearMonth(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 7 && value[4] == '-' {
		year := value[:4]
		for _, r := range year {
			if r < '0' || r > '9' {
				return ""
			}
		}
		if value[5] < '0' || value[5] > '9' || value[6] < '0' || value[6] > '9' {
			return ""
		}
		return value[:7]
	}
	return ""
}

func (s sourceRow) header() string {
	title := chunkContext(s.title)
	if title == "" {
		title = chunkContext(s.project)
	}
	occurred := s.occurredAt
	if occurred == "" {
		occurred = s.createdAt
	}
	return chunkHeader(title, occurred)
}

func (s sourceRow) window() []string {
	if s.chunkSize > 0 {
		size, overlap := s.chunking()
		return chunks(s.text, size, overlap)
	}
	return tokenChunks(s.text, defaultChunkTokens, defaultOverlapTokens)
}

func expandColumnRows(row sourceRow, columns []string, values map[string]any) []sourceRow {
	if len(columns) == 0 {
		if strings.TrimSpace(row.text) == "" {
			return nil
		}
		return []sourceRow{row}
	}
	parts := make([]string, 0, len(columns))
	out := make([]sourceRow, 0, len(columns))
	for _, column := range columns {
		text := strings.TrimSpace(stringValue(values[column]))
		if text == "" {
			continue
		}
		parts = append(parts, text)
		item := row
		item.column = column
		item.text = text
		out = append(out, item)
	}
	sep := "\n\n"
	if row.kind == "sessions" {
		sep = "\n"
	}
	rowText := strings.Join(parts, sep)
	for i := range out {
		out[i].rowText = rowText
	}
	return out
}
